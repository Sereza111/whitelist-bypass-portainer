package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if got.Mode != "vk" || got.Resources != "default" || got.VideoReliability != "auto" || got.KCPProfile != "auto" || got.DisplayName != "Headless" {
		t.Fatalf("unexpected defaults: %#v", got)
	}
	if _, err := m.normalizeRequest(sessionRequest{Mode: "unknown"}); err == nil {
		t.Fatal("unsupported mode accepted")
	}
}

func TestEncodeMobileInvite(t *testing.T) {
	profile := clientProfile{
		ID: "client-mobile-1", Name: "Телефон", RecoveryKey: "abcdefghijklmnopqrstuvwxyz012345",
	}
	status := sessionStatus{SessionLink: "https://vk.com/call/join/example", Generation: 7}
	invite, err := encodeMobileInvite(profile, status)
	if err != nil {
		t.Fatal(err)
	}
	encoded, ok := strings.CutPrefix(invite, "wlb://import?data=")
	if !ok {
		t.Fatalf("unexpected invite URI: %q", invite)
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var payload mobileInvitePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || payload.Name != profile.Name || payload.Profile != profile.ID ||
		payload.Provider != "vk" || payload.Key != profile.RecoveryKey || payload.Generation != 7 || payload.Link != status.SessionLink {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestValidMobileInviteLink(t *testing.T) {
	tests := []struct {
		provider string
		link     string
		want     bool
	}{
		{"vk", "https://vk.com/call/join/example", true},
		{"telemost", "https://telemost.yandex.ru/j/123456789", true},
		{"wbstream", "wbstream://2c9fd4f0-7d64-4d25-a9a2-111111111111", true},
		{"dion", "dion://demo-room_42", true},
		{"dion", "https://dion.vc/event/demo-room_42", true},
		{"vk", "http://vk.com/call/join/example", false},
		{"telemost", "https://evil.example/j/123456789", false},
		{"wbstream", "wbstream://room/path", false},
		{"dion", "dion://room?token=secret", false},
		{"unknown", "https://vk.com/call/join/example", false},
	}
	for _, test := range tests {
		if got := validMobileInviteLink(test.provider, test.link); got != test.want {
			t.Errorf("validMobileInviteLink(%q, %q)=%v want %v", test.provider, test.link, got, test.want)
		}
	}
}

func TestEncodeMobileInviteForEveryProvider(t *testing.T) {
	tests := map[string]string{
		"vk":       "https://vk.com/call/join/example",
		"telemost": "https://telemost.yandex.ru/j/123456789",
		"wbstream": "wbstream://2c9fd4f0-7d64-4d25-a9a2-111111111111",
		"dion":     "dion://demo-room_42",
	}
	for provider, link := range tests {
		profile := clientProfile{
			ID: "client-" + provider, Name: provider, RecoveryKey: "abcdefghijklmnopqrstuvwxyz012345",
			Config: sessionRequest{Mode: provider},
		}
		invite, err := encodeMobileInvite(profile, sessionStatus{SessionLink: link})
		if err != nil {
			t.Fatalf("%s invite: %v", provider, err)
		}
		encoded, _ := strings.CutPrefix(invite, "wlb://import?data=")
		body, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var payload mobileInvitePayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Provider != provider || payload.Link != link {
			t.Fatalf("unexpected %s payload: %#v", provider, payload)
		}
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
	registerControlAPIRoutes(mux, cp, nil, nil, "admin", testPanelPassword, t.TempDir())

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

	fakeManager := newManagerAt(t.TempDir())
	fakeManager.state = "waiting-for-client"
	fakeManager.link = "https://vk.com/call/join/example"
	fakeSession := &managedSession{
		ID: "session-mobile", ClientID: created.ID, ClientName: created.Name,
		CreatedAt: time.Now().UTC(), Manager: fakeManager, Generation: 3,
	}
	cp.mu.Lock()
	cp.sessions[fakeSession.ID] = fakeSession
	cp.mu.Unlock()
	response = controlAPIRequest(t, mux, http.MethodPost, "/api/profiles/"+created.ID+"/mobile-invite", "{}")
	if response.Code != http.StatusCreated {
		t.Fatalf("mobile invite status=%d body=%s", response.Code, response.Body.String())
	}
	var invite mobileInviteResponse
	if err := json.Unmarshal(response.Body.Bytes(), &invite); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(invite.URL, "/join/") || !invite.ExpiresAt.After(time.Now()) {
		t.Fatalf("unexpected mobile invite: %#v", invite)
	}
	landingRequest := httptest.NewRequest(http.MethodGet, invite.URL, nil)
	landingResponse := httptest.NewRecorder()
	mux.ServeHTTP(landingResponse, landingRequest)
	if landingResponse.Code != http.StatusOK || !strings.Contains(landingResponse.Body.String(), "wlb://import?data=") {
		t.Fatalf("mobile landing status=%d body=%s", landingResponse.Code, landingResponse.Body.String())
	}
	if strings.Contains(landingResponse.Body.String(), created.RecoveryKey) || strings.Contains(landingResponse.Body.String(), fakeManager.link) {
		t.Fatal("mobile landing exposed unencoded profile secrets")
	}
	cp.mu.Lock()
	delete(cp.sessions, fakeSession.ID)
	cp.mu.Unlock()

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
	registerControlAPIRoutes(mux, cp, login, nil, "admin", testPanelPassword, t.TempDir())

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
	registerControlAPIRoutes(mux, cp, nil, nil, "admin", testPanelPassword, t.TempDir())
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
	registerControlAPIRoutes(mux, cp, nil, nil, "admin", testPanelPassword, t.TempDir())
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
	if err != nil || !strings.Contains(string(persisted), `"schema": 4`) {
		t.Fatalf("migrated schema was not persisted: err=%v body=%s", err, persisted)
	}
}

func TestControlPlaneMigratesSchemaTwoToCurrent(t *testing.T) {
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
	if err != nil || !strings.Contains(string(persisted), `"schema": 4`) || !strings.Contains(string(persisted), `"settings"`) {
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
	peerLines := []string{
		"headless: === TUNNEL CONNECTED ===",
		"[health] peer recovery attempt 1/3: offer timeout",
		"kcptunnel: sent=10 wait_snd=2",
	}
	if state := deriveRuntimeState("running", peerLines); state != "degraded" {
		t.Fatalf("peer recovery state=%q", state)
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

func TestWBDevicePairingAndCallExchangeOnlyInvite(t *testing.T) {
	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, "managed-secrets", "cookies-wbstream.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy-account-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	login := newWBLoginManager(dataDir)
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy managed WB credentials were not removed: %v", err)
	}
	mux := http.NewServeMux()
	registerWBLoginRoutes(mux, login, "admin", testPanelPassword)

	start := httptest.NewRequest(http.MethodPost, "/api/wb-login/device/start", strings.NewReader(`{}`))
	start.Host = "panel.example.test"
	start.Header.Set("Origin", "https://panel.example.test")
	start.Header.Set("X-Forwarded-Proto", "https")
	start.SetBasicAuth("admin", testPanelPassword)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, start)
	if response.Code != http.StatusCreated {
		t.Fatalf("device pairing start=%d body=%s", response.Code, response.Body.String())
	}
	var started struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	landing, err := url.Parse(started.URL)
	if err != nil || landing.Scheme != "https" || landing.Host != "panel.example.test" || landing.Path != "/wb-device" || landing.Fragment == "" {
		t.Fatalf("unexpected device landing URL %q", started.URL)
	}
	landingPage := httptest.NewRequest(http.MethodGet, "/wb-device", nil)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, landingPage)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/wb-device.js") || strings.Contains(response.Body.String(), "Bearer ") {
		t.Fatalf("public device landing=%d body=%s", response.Code, response.Body.String())
	}

	qr := httptest.NewRequest(http.MethodGet, "/api/wb-login/device/qr", nil)
	qr.SetBasicAuth("admin", testPanelPassword)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, qr)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Body.Len() < 100 {
		t.Fatalf("device QR=%d type=%q size=%d", response.Code, response.Header().Get("Content-Type"), response.Body.Len())
	}

	pair := httptest.NewRequest(http.MethodPost, "/api/wb-creator/pair", strings.NewReader(`{"deviceId":"11111111-2222-4333-8444-555555555555","name":"Test Android"}`))
	pair.Header.Set("Authorization", "Bearer "+landing.Fragment)
	pair.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, pair)
	if response.Code != http.StatusCreated {
		t.Fatalf("device pair=%d body=%s", response.Code, response.Body.String())
	}
	var paired struct {
		CreatorID    string `json:"creatorId"`
		DeviceSecret string `json:"deviceSecret"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &paired); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(dataDir, "wb-creator.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), paired.DeviceSecret) || strings.Contains(strings.ToLower(string(stored)), "cookie") {
		t.Fatalf("raw device secret or WB browser data was persisted: %s", stored)
	}
	status := controlAPIRequest(t, mux, http.MethodGet, "/api/wb-login", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"managed":true`) {
		t.Fatalf("unexpected paired status: %d %s", status.Code, status.Body.String())
	}
	for _, secret := range []string{landing.Fragment, paired.DeviceSecret} {
		if strings.Contains(status.Body.String(), secret) {
			t.Fatalf("device pairing secret leaked through status API")
		}
	}

	callResult := make(chan wbCallResult, 1)
	go func() {
		link, err := login.requestCall(context.Background(), "client-test", "Test profile")
		callResult <- wbCallResult{link: link, err: err}
	}()
	deadline := time.Now().Add(time.Second)
	for login.nextCall() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	poll := httptest.NewRequest(http.MethodPost, "/api/wb-creator/commands/next", nil)
	poll.Header.Set("Authorization", "Bearer "+paired.DeviceSecret)
	poll.Header.Set("X-WLB-Creator-ID", paired.CreatorID)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, poll)
	if response.Code != http.StatusOK {
		t.Fatalf("creator poll=%d body=%s", response.Code, response.Body.String())
	}
	var command wbCallRequest
	if err := json.Unmarshal(response.Body.Bytes(), &command); err != nil {
		t.Fatal(err)
	}
	invite := httptest.NewRequest(http.MethodPost, "/api/wb-creator/commands/"+command.ID+"/invite", strings.NewReader(`{"inviteLink":"https://stream.wb.ru/room/room_123"}`))
	invite.Header.Set("Authorization", "Bearer "+paired.DeviceSecret)
	invite.Header.Set("X-WLB-Creator-ID", paired.CreatorID)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, invite)
	if response.Code != http.StatusOK {
		t.Fatalf("invite submit=%d body=%s", response.Code, response.Body.String())
	}
	result := <-callResult
	if result.err != nil || result.link != "wbstream://room_123" {
		t.Fatalf("unexpected call result: %#v", result)
	}
}

func TestWBDevicePairingRequiresHTTPSAndBearer(t *testing.T) {
	login := newWBLoginManager(t.TempDir())
	mux := http.NewServeMux()
	registerWBLoginRoutes(mux, login, "admin", testPanelPassword)

	start := httptest.NewRequest(http.MethodPost, "/api/wb-login/device/start", strings.NewReader(`{}`))
	start.Host = "panel.example.test"
	start.Header.Set("Origin", "http://panel.example.test")
	start.SetBasicAuth("admin", testPanelPassword)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, start)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("HTTP device pairing start=%d", response.Code)
	}

	upload := httptest.NewRequest(http.MethodPost, "/api/wb-creator/pair", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, upload)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated device upload=%d", response.Code)
	}
}

func TestWBInviteValidationAndCookieFreeCommand(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"wbstream://room_123", "wbstream://room_123"},
		{"https://stream.wb.ru/room/room-456", "wbstream://room-456"},
		{"https://evil.example/room/room_123", ""},
		{"https://stream.wb.ru/room/room_123?token=secret", ""},
		{"https://user@stream.wb.ru/room/room_123", ""},
	} {
		got, err := normalizeWBInvite(test.input)
		if test.want == "" && err == nil {
			t.Fatalf("unsafe invite accepted: %q", test.input)
		}
		if test.want != "" && (err != nil || got != test.want) {
			t.Fatalf("invite %q: got=%q err=%v", test.input, got, err)
		}
	}
	binsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binsDir, "headless-wbstream-creator"), []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	mgr := newManagerAt(t.TempDir())
	mgr.binsDir = binsDir
	cmd, err := mgr.commandFor(sessionRequest{
		Mode: "wbstream", ExistingLink: "wbstream://room_123", Resources: "default",
		VideoReliability: "auto", KCPProfile: "auto", DeviceInvite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	if strings.Contains(args, "--cookies") || !strings.Contains(args, "--room wbstream://room_123") {
		t.Fatalf("WB command must use only the invite, got: %s", args)
	}
}
