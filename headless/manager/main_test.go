package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

const testPanelPassword = "long-test-password"

func TestNormalizeRequest(t *testing.T) {
	m := newManager()
	got, err := m.normalizeRequest(sessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "vk" || got.Resources != "default" || got.VideoReliability != "auto" || got.KCPProfile != "balanced" || got.DisplayName != "Headless" {
		t.Fatalf("unexpected defaults: %#v", got)
	}
	if _, err := m.normalizeRequest(sessionRequest{Mode: "unknown"}); err == nil {
		t.Fatal("unsupported mode accepted")
	}
}

func TestLogRingRedactsJoinLink(t *testing.T) {
	ring := newLogRing(10)
	_, _ = ring.Write([]byte("join_link: https://example.test/secret\nnormal event\n"))
	body := strings.Join(ring.snapshot(), "\n")
	if strings.Contains(body, "secret") {
		t.Fatalf("join link leaked into logs: %s", body)
	}
	if !strings.Contains(body, "normal event") {
		t.Fatalf("normal log missing: %s", body)
	}
}

func TestRequireAuth(t *testing.T) {
	handler := requireAuth("admin", testPanelPassword, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetBasicAuth("admin", testPanelPassword)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authenticated status=%d", response.Code)
	}
}

func TestControlAPIProfileLifecycle(t *testing.T) {
	cp, err := newControlPlane(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerControlAPIRoutes(mux, cp, nil, "admin", testPanelPassword, t.TempDir())

	created := clientProfile{}
	response := controlAPIRequest(t, mux, http.MethodPost, "/api/profiles", `{
		"name":"Phone","enabled":true,"maxSessions":2,
		"config":{"mode":"vk","resources":"default","displayName":"Phone","videoReliability":"auto","kcpProfile":"balanced"}
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Phone" || created.MaxSessions != 2 {
		t.Fatalf("unexpected created profile: %#v", created)
	}
	if !created.AutoRestart || len(created.RecoveryKey) < 32 {
		t.Fatalf("recovery defaults missing: %#v", created)
	}

	response = controlAPIRequest(t, mux, http.MethodGet, "/api/profiles", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), created.ID) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}

	response = controlAPIRequest(t, mux, http.MethodPatch, "/api/profiles/"+created.ID, `{
		"name":"Phone locked","enabled":false,"maxSessions":1,
		"config":{"mode":"vk","resources":"moderate","displayName":"Phone","videoReliability":"auto","kcpProfile":"stable"}
	}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("patch status=%d body=%s", response.Code, response.Body.String())
	}

	response = controlAPIRequest(t, mux, http.MethodPost, "/api/profiles/"+created.ID+"/duplicate", "")
	if response.Code != http.StatusCreated {
		t.Fatalf("duplicate status=%d body=%s", response.Code, response.Body.String())
	}
	var duplicate clientProfile
	if err := json.Unmarshal(response.Body.Bytes(), &duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID == created.ID || duplicate.RecoveryKey == created.RecoveryKey || !strings.Contains(duplicate.Name, "copy") {
		t.Fatalf("duplicate did not receive an independent identity: %#v", duplicate)
	}

	response = controlAPIRequest(t, mux, http.MethodDelete, "/api/profiles/"+created.ID, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNormalizeVKRecipient(t *testing.T) {
	for input, want := range map[string]string{
		"123":                    "123",
		" https://vk.com/id42/ ": "42",
		"VK.com/id9001":          "9001",
	} {
		got, err := normalizeVKRecipient(input)
		if err != nil || got != want {
			t.Fatalf("normalize %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "id123", "vk.com/durov", "-123", "0", "123abc"} {
		if got, err := normalizeVKRecipient(input); err == nil {
			t.Fatalf("invalid recipient %q accepted as %q", input, got)
		}
	}
}

func TestRecoveryRecipientPrecedence(t *testing.T) {
	t.Setenv("VK_PEER_ID", "101")
	cp, err := newControlPlane(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, source := cp.effectiveRecoveryRecipient(""); got != "101" || source != "env" {
		t.Fatalf("env fallback = %q/%q", got, source)
	}
	if err := cp.setGlobalRecoveryRecipient("vk.com/id202"); err != nil {
		t.Fatal(err)
	}
	if got, source := cp.effectiveRecoveryRecipient(""); got != "202" || source != "panel" {
		t.Fatalf("panel override = %q/%q", got, source)
	}
	override := "https://vk.com/id303"
	enabled := true
	profile, err := cp.createProfile(profileInput{
		Name: "Phone", Enabled: &enabled, MaxSessions: 1, Config: sessionRequest{Mode: "vk"},
		RecoveryRecipient: optionalString{Present: true, Value: &override},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, source := cp.effectiveRecoveryRecipient(profile.ID); got != "303" || source != "profile" {
		t.Fatalf("profile override = %q/%q", got, source)
	}
	if err := cp.setGlobalRecoveryRecipient(""); err != nil {
		t.Fatal(err)
	}
	if got, source := cp.effectiveRecoveryRecipient(""); got != "101" || source != "env" {
		t.Fatalf("cleared panel should reveal env fallback, got %q/%q", got, source)
	}
}

func TestRecoverySettingsAndTestDeliveryAPI(t *testing.T) {
	cp, err := newControlPlane(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cp.managedSecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cp.managedSecretsDir, "cookies-vk.json"), []byte(`[{"name":"sid","value":"cookie-secret"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	login := &vkLoginManager{state: "ready", accountID: "456"}
	mux := http.NewServeMux()
	registerControlAPIRoutes(mux, cp, login, "admin", testPanelPassword, t.TempDir())

	response := controlAPIRequest(t, mux, http.MethodPatch, "/api/settings/recovery", `{"recipient":"https://vk.com/id456"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source":"panel"`) || !strings.Contains(response.Body.String(), `"sameAccount":true`) {
		t.Fatalf("recovery patch status=%d body=%s", response.Code, response.Body.String())
	}

	oldSender := recoveryMessageSender
	defer func() { recoveryMessageSender = oldSender }()
	var deliveredRecipient, deliveredMessage string
	recoveryMessageSender = func(_ context.Context, cookiePath, recipient, message string) error {
		deliveredRecipient, deliveredMessage = recipient, message
		if !strings.HasSuffix(cookiePath, "cookies-vk.json") {
			t.Fatalf("unexpected cookie path %q", cookiePath)
		}
		return nil
	}
	response = controlAPIRequest(t, mux, http.MethodPost, "/api/settings/recovery/test", "")
	if response.Code != http.StatusOK || deliveredRecipient != "456" {
		t.Fatalf("test delivery status=%d recipient=%q body=%s", response.Code, deliveredRecipient, response.Body.String())
	}
	if strings.Contains(deliveredMessage, "cookie-secret") || strings.Contains(deliveredMessage, "recoveryKey") || strings.Contains(deliveredMessage, "http") {
		t.Fatalf("test message contains sensitive material: %q", deliveredMessage)
	}
	response = controlAPIRequest(t, mux, http.MethodPost, "/api/settings/recovery/test", "")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("test delivery rate limit status=%d body=%s", response.Code, response.Body.String())
	}
	response = controlAPIRequest(t, mux, http.MethodGet, "/api/settings/recovery", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"verifiedAt"`) {
		t.Fatalf("verified recovery settings missing: status=%d body=%s", response.Code, response.Body.String())
	}
	response = controlAPIRequest(t, mux, http.MethodGet, "/api/events", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "VK test message delivered") || strings.Contains(response.Body.String(), "cookie-secret") {
		t.Fatalf("unsafe or missing event response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRecoveryTestFailureDoesNotLeakSenderError(t *testing.T) {
	cp, err := newControlPlane(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.setGlobalRecoveryRecipient("777"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cp.managedSecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cp.managedSecretsDir, "cookies-vk.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerControlAPIRoutes(mux, cp, nil, "admin", testPanelPassword, t.TempDir())
	oldSender := recoveryMessageSender
	defer func() { recoveryMessageSender = oldSender }()
	recoveryMessageSender = func(context.Context, string, string, string) error {
		return errors.New("token-super-secret call-link-super-secret")
	}
	response := controlAPIRequest(t, mux, http.MethodPost, "/api/settings/recovery/test", "")
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "super-secret") {
		t.Fatalf("sender error leaked: status=%d body=%s", response.Code, response.Body.String())
	}
	response = controlAPIRequest(t, mux, http.MethodGet, "/api/events", "")
	if strings.Contains(response.Body.String(), "super-secret") {
		t.Fatalf("sender error leaked through events: %s", response.Body.String())
	}
}

func TestControlAPIRejectsCrossOriginMutation(t *testing.T) {
	cp, err := newControlPlane(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerControlAPIRoutes(mux, cp, nil, "admin", testPanelPassword, t.TempDir())
	request := httptest.NewRequest(http.MethodPost, "/api/profiles", strings.NewReader(`{"name":"Phone"}`))
	request.SetBasicAuth("admin", testPanelPassword)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", response.Code)
	}
}

func controlAPIRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.SetBasicAuth("admin", testPanelPassword)
	if method != http.MethodGet {
		request.Header.Set("Origin", "http://example.test")
		request.Host = "example.test"
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestControlPlaneProfilePersistence(t *testing.T) {
	dataDir := t.TempDir()
	cp, err := newControlPlane(dataDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	created, err := cp.createProfile(profileInput{
		Name: "Laptop", Enabled: &enabled, MaxSessions: 2,
		Config: sessionRequest{Mode: "vk", KCPProfile: "fast", ExistingLink: "https://example.test/secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Config.Resources != "default" || created.Config.KCPProfile != "fast" || created.Config.ExistingLink != "" {
		t.Fatalf("unexpected profile: %#v", created)
	}
	reloaded, err := newControlPlane(dataDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	profiles := reloaded.listProfiles()
	if len(profiles) != 1 || profiles[0].ID != created.ID {
		t.Fatalf("profile did not survive reload: %#v", profiles)
	}
	info, err := os.Stat(filepath.Join(dataDir, "control-plane.json"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("state file missing: info=%v err=%v", info, err)
	}
}

func TestControlPlaneMigratesRecoveryDefaults(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC()
	legacy := controlPlaneSnapshot{
		Schema: 1,
		Profiles: []clientProfile{{
			ID: "client-legacy", Name: "Legacy phone", Enabled: true, MaxSessions: 1,
			Config:    sessionRequest{Mode: "vk", Resources: "default", DisplayName: "Phone", VideoReliability: "auto", KCPProfile: "balanced"},
			CreatedAt: now, UpdatedAt: now,
		}},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "control-plane.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := newControlPlane(dataDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	profiles := cp.listProfiles()
	if len(profiles) != 1 || !profiles[0].AutoRestart || len(profiles[0].RecoveryKey) < 32 {
		t.Fatalf("legacy recovery migration failed: %#v", profiles)
	}
	persisted, err := os.ReadFile(filepath.Join(dataDir, "control-plane.json"))
	if err != nil || !strings.Contains(string(persisted), `"schema": 3`) {
		t.Fatalf("migrated schema was not persisted: err=%v body=%s", err, persisted)
	}
}

func TestControlPlaneMigratesSchemaTwoToThree(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Now().UTC()
	body := fmt.Sprintf(`{"schema":2,"profiles":[{"id":"client-v2","name":"V2","enabled":true,"maxSessions":1,"config":{"mode":"vk","resources":"default","displayName":"V2","videoReliability":"auto","kcpProfile":"balanced"},"createdAt":%q,"updatedAt":%q,"autoRestart":true,"recoveryKey":"existing-key","recoveryGeneration":2}]}`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dataDir, "control-plane.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := newControlPlane(dataDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	profiles := cp.listProfiles()
	if len(profiles) != 1 || profiles[0].RecoveryKey != "existing-key" {
		t.Fatalf("schema two profile changed unexpectedly: %#v", profiles)
	}
	persisted, err := os.ReadFile(filepath.Join(dataDir, "control-plane.json"))
	if err != nil || !strings.Contains(string(persisted), `"schema": 3`) || !strings.Contains(string(persisted), `"settings"`) {
		t.Fatalf("schema two migration not persisted: err=%v body=%s", err, persisted)
	}
}

func TestControlPlaneEnforcesClientAndServerLimits(t *testing.T) {
	cp, err := newControlPlane(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	profile, err := cp.createProfile(profileInput{
		Name: "Laptop", Enabled: &enabled, MaxSessions: 1,
		Config: sessionRequest{Mode: "vk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cp.sessions["active"] = &managedSession{
		ID: "active", ClientID: profile.ID, ClientName: profile.Name,
		CreatedAt: time.Now(), Manager: &manager{state: "running", logs: newLogRing(10)},
	}
	if _, err := cp.startSession(sessionInput{ClientID: profile.ID}); err == nil || !strings.Contains(err.Error(), "server session limit") {
		t.Fatalf("expected server limit, got %v", err)
	}
	if err := cp.deleteProfile(profile.ID); err == nil || !strings.Contains(err.Error(), "active sessions") {
		t.Fatalf("expected active-session delete guard, got %v", err)
	}

	cp.maxSessions = 2
	if _, err := cp.startSession(sessionInput{ClientID: profile.ID}); err == nil || !strings.Contains(err.Error(), "client session limit") {
		t.Fatalf("expected client limit, got %v", err)
	}
}

func TestControlPlaneRejectsDisabledAndExpiredProfiles(t *testing.T) {
	cp, err := newControlPlane(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	profile, err := cp.createProfile(profileInput{
		Name: "Disabled", Enabled: &disabled, MaxSessions: 1,
		Config: sessionRequest{Mode: "vk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cp.startSession(sessionInput{ClientID: profile.ID}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled rejection, got %v", err)
	}

	past := time.Now().Add(-time.Minute)
	enabled := true
	expired, err := cp.createProfile(profileInput{
		Name: "Expired", Enabled: &enabled, MaxSessions: 1, ExpiresAt: &past,
		Config: sessionRequest{Mode: "vk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cp.startSession(sessionInput{ClientID: expired.ID}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiration rejection, got %v", err)
	}
}

func TestLatestMetricsAndRuntimeState(t *testing.T) {
	lines := []string{
		"headless: === TUNNEL CONNECTED ===",
		"METRICS tx_kbps=12.5 rx_kbps=44.0 kcp_wait_snd=7",
	}
	metrics := latestMetrics(lines)
	if metrics["rx_kbps"] != "44.0" || metrics["kcp_wait_snd"] != "7" {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
	if state := deriveRuntimeState("running", lines); state != "connected" {
		t.Fatalf("state=%q", state)
	}
	lines = append(lines, "kcptunnel: stalled wait_snd=1024")
	if state := deriveRuntimeState("running", lines); state != "degraded" {
		t.Fatalf("stalled state=%q", state)
	}
}

func TestRecoveryDelayIsBounded(t *testing.T) {
	if recoveryDelay(1) != 2*time.Second || recoveryDelay(4) != 30*time.Second {
		t.Fatalf("unexpected early recovery delays")
	}
	if recoveryDelay(100) != 5*time.Minute {
		t.Fatalf("recovery delay is not capped")
	}
}

func TestVKLoginCookieExportAndManagedPrecedence(t *testing.T) {
	managedDir := t.TempDir()
	mountedDir := t.TempDir()
	login := newVKLoginManager(managedDir, mountedDir)
	cookies := []*network.Cookie{
		{Name: "remixsid6", Value: "auth-value", Domain: ".vk.com", Path: "/", Secure: true, HTTPOnly: true},
		{Name: "remixuid", Value: "12345", Domain: ".vk.com", Path: "/"},
		{Name: "empty", Value: "", Domain: ".vk.com", Path: "/"},
	}
	if !hasVKAuthCookie(cookies) {
		t.Fatal("VK auth cookie was not detected")
	}
	if header := cookieHeader(cookies); !strings.Contains(header, "remixsid6=auth-value") || strings.Contains(header, "empty=") {
		t.Fatalf("unexpected cookie header: %q", header)
	}
	if err := login.saveCookies(cookies); err != nil {
		t.Fatal(err)
	}
	if !fileReady(filepath.Join(managedDir, "cookies-vk.json")) {
		t.Fatal("managed VK cookies were not written")
	}

	binsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binsDir, "headless-vk-creator"), []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	mgr := newManagerAt(t.TempDir())
	mgr.binsDir = binsDir
	mgr.secretsDir = mountedDir
	mgr.managedSecretsDir = managedDir
	cmd, err := mgr.commandFor(sessionRequest{Mode: "vk", Resources: "default", VideoReliability: "auto", KCPProfile: "balanced"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), filepath.Join(managedDir, "cookies-vk.json")) {
		t.Fatalf("Creator did not prefer panel-managed cookies: %v", cmd.Args)
	}
}

func TestVKLoginAPINeverReturnsCookies(t *testing.T) {
	managedDir := t.TempDir()
	mountedDir := t.TempDir()
	secret := `[{"name":"remixsid6","value":"must-not-leak"}]`
	if err := os.WriteFile(filepath.Join(managedDir, "cookies-vk.json"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	login := newVKLoginManager(managedDir, mountedDir)
	mux := http.NewServeMux()
	registerVKLoginRoutes(mux, login, "admin", testPanelPassword)

	response := controlAPIRequest(t, mux, http.MethodGet, "/api/vk-login", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"managed":true`) {
		t.Fatalf("unexpected QR status: code=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "must-not-leak") || strings.Contains(response.Body.String(), "remixsid") {
		t.Fatalf("VK cookies leaked through status API: %s", response.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/vk-login/start", strings.NewReader(`{}`))
	request.SetBasicAuth("admin", testPanelPassword)
	request.Header.Set("Origin", "https://attacker.example")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin QR start status=%d", response.Code)
	}
}
