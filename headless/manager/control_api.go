package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type providerStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type overviewResponse struct {
	BuildVersion     string           `json:"buildVersion"`
	BuildCommit      string           `json:"buildCommit"`
	BuildTime        string           `json:"buildTime"`
	MaxSessions      int              `json:"maxSessions"`
	ActiveSessions   int              `json:"activeSessions"`
	ClientCount      int              `json:"clientCount"`
	Providers        []providerStatus `json:"providers"`
	RecoveryDelivery bool             `json:"recoveryDelivery"`
}

type recoverySettingsResponse struct {
	Recipient          string     `json:"recipient"`
	EffectiveRecipient string     `json:"effectiveRecipient"`
	Source             string     `json:"source"`
	Configured         bool       `json:"configured"`
	VerifiedAt         *time.Time `json:"verifiedAt,omitempty"`
	ServerAccountID    string     `json:"serverAccountId,omitempty"`
	SameAccount        bool       `json:"sameAccount"`
}

type recoverySettingsInput struct {
	Recipient string `json:"recipient"`
}

type mobileInvitePayload struct {
	Version    int    `json:"v"`
	Name       string `json:"name"`
	Provider   string `json:"provider,omitempty"`
	Profile    string `json:"profile"`
	Key        string `json:"key"`
	Generation int    `json:"generation"`
	Link       string `json:"link"`
	SyncURL    string `json:"syncUrl,omitempty"`
}

type clientInviteInput struct {
	AfterGeneration int `json:"afterGeneration"`
}

type clientInviteResponse struct {
	Provider   string `json:"provider"`
	Generation int    `json:"generation"`
	Link       string `json:"link"`
}

type mobileInviteResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type mobileInviteRecord struct {
	URI       string
	ExpiresAt time.Time
}

var recoveryMessageSender = sendVKTestMessage

func registerControlAPIRoutes(mux *http.ServeMux, cp *controlPlane, vkLogin *vkLoginManager, wbLogin *wbLoginManager, username, password, secretsDir string) {
	protect := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, handler)
	}
	mutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
	}
	var recoveryTestMu sync.Mutex
	recoveryTests := make(map[string]time.Time)
	var mobileInviteMu sync.Mutex
	mobileInvites := make(map[string]mobileInviteRecord)
	recoveryView := func(profileID string) recoverySettingsResponse {
		recipient, source := cp.effectiveRecoveryRecipient(profileID)
		cp.mu.Lock()
		configured := cp.settings.RecoveryRecipient
		verified := cp.settings.RecoveryVerifiedAt
		if profileID != "" {
			if profile, ok := cp.profiles[profileID]; ok {
				if profile.RecoveryRecipient != nil {
					configured = *profile.RecoveryRecipient
				}
				verified = profile.RecoveryVerifiedAt
			}
		}
		cp.mu.Unlock()
		accountID := ""
		if vkLogin != nil {
			accountID = vkLogin.status().AccountID
		}
		return recoverySettingsResponse{
			Recipient: configured, EffectiveRecipient: recipient, Source: source,
			Configured: recipient != "", VerifiedAt: verified, ServerAccountID: accountID,
			SameAccount: accountID != "" && accountID == recipient,
		}
	}
	testRecovery := func(w http.ResponseWriter, profileID string) {
		recipient, source := cp.effectiveRecoveryRecipient(profileID)
		if recipient == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recovery recipient is not configured"})
			return
		}
		cookiePath := filepath.Join(cp.managedSecretsDir, "cookies-vk.json")
		if !fileReady(cookiePath) {
			cookiePath = filepath.Join(secretsDir, "cookies-vk.json")
		}
		if !fileReady(cookiePath) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "server VK is not configured"})
			return
		}
		key := profileID
		if key == "" {
			key = "global"
		}
		recoveryTestMu.Lock()
		if elapsed := time.Since(recoveryTests[key]); elapsed < 15*time.Second {
			recoveryTestMu.Unlock()
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "wait before sending another test"})
			return
		}
		recoveryTests[key] = time.Now()
		recoveryTestMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		message := fmt.Sprintf("Whitelist Bypass · test delivery\nProfile: %s\nTime: %s", key, time.Now().UTC().Format(time.RFC3339))
		if err := recoveryMessageSender(ctx, cookiePath, recipient, message); err != nil {
			cp.events.add("error", "recovery", "VK test message failed", profileID)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "VK did not deliver the test message"})
			return
		}
		if profileID == "" {
			_ = cp.markGlobalRecoveryVerified()
		} else {
			_ = cp.markProfileRecoveryVerified(profileID)
		}
		cp.events.add("info", "recovery", "VK test message delivered", profileID)
		writeJSON(w, http.StatusOK, map[string]any{
			"delivered": true, "recipient": recipient, "source": source, "timestamp": time.Now().UTC(),
		})
	}

	mux.Handle("GET /api/overview", protect(func(w http.ResponseWriter, _ *http.Request) {
		sessions := cp.listSessions()
		active := 0
		for _, session := range sessions {
			if isActiveState(session.Status.State) {
				active++
			}
		}
		writeJSON(w, http.StatusOK, overviewResponse{
			BuildVersion: Version, BuildCommit: BuildCommit, BuildTime: BuildTime,
			MaxSessions: cp.maxSessions, ActiveSessions: active,
			ClientCount: len(cp.listProfiles()), Providers: inspectProviders(secretsDir, cp.managedSecretsDir, wbLogin),
			RecoveryDelivery: cp.recoveryConfigured(),
		})
	}))

	mux.Handle("GET /api/providers", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, inspectProviders(secretsDir, cp.managedSecretsDir, wbLogin))
	}))
	mux.Handle("GET /api/profiles", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, cp.listProfiles())
	}))
	mobileInvite := func(profileID, syncURL string) (string, error) {
		profile, ok := cp.profile(profileID)
		if !ok {
			return "", os.ErrNotExist
		}
		for _, session := range cp.listSessions() {
			if session.ClientID == profileID && session.Status.SessionLink != "" {
				return encodeMobileInvite(profile, session.Status, syncURL)
			}
		}
		return "", errors.New("start the profile and wait for its call link first")
	}
	mux.Handle("POST /api/profiles/{id}/mobile-invite", mutate(func(w http.ResponseWriter, r *http.Request) {
		origin, originErr := wbPublicPanelOrigin(r)
		if originErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "open the panel through HTTPS before pairing a client"})
			return
		}
		profileID := r.PathValue("id")
		syncURL := strings.TrimRight(origin, "/") + "/api/client-profiles/" + url.PathEscape(profileID) + "/invite"
		invite, err := mobileInvite(profileID, syncURL)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "client profile not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		now := time.Now().UTC()
		expiresAt := now.Add(15 * time.Minute)
		token := randomSecret()
		mobileInviteMu.Lock()
		for candidate, record := range mobileInvites {
			if !record.ExpiresAt.After(now) {
				delete(mobileInvites, candidate)
			}
		}
		mobileInvites[token] = mobileInviteRecord{URI: invite, ExpiresAt: expiresAt}
		mobileInviteMu.Unlock()
		writeJSON(w, http.StatusCreated, mobileInviteResponse{URL: "/join/" + token, ExpiresAt: expiresAt})
	}))
	mux.HandleFunc("POST /api/client-profiles/{id}/invite", func(w http.ResponseWriter, r *http.Request) {
		profile, ok := cp.profile(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "client profile not found"})
			return
		}
		secret, hasBearer := requestBearer(r)
		want := sha256.Sum256([]byte(profile.RecoveryKey))
		got := sha256.Sum256([]byte(secret))
		if !hasBearer || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "client profile authentication required"})
			return
		}
		if !profile.Enabled || (profile.ExpiresAt != nil && time.Now().After(*profile.ExpiresAt)) {
			writeJSON(w, http.StatusGone, map[string]string{"error": "client profile is unavailable"})
			return
		}
		var input clientInviteInput
		if !decodeRequest(w, r, &input) {
			return
		}
		if !profileInviteReady(profile) || profile.InviteGeneration <= input.AfterGeneration {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		provider := strings.ToLower(strings.TrimSpace(profile.Config.Mode))
		if !validMobileInviteLink(provider, profile.CurrentInvite) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "profile invite is unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, clientInviteResponse{
			Provider: provider, Generation: profile.InviteGeneration, Link: profile.CurrentInvite,
		})
	})
	mux.HandleFunc("GET /join/{token}", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		mobileInviteMu.Lock()
		record, ok := mobileInvites[r.PathValue("token")]
		if ok && !record.ExpiresAt.After(now) {
			delete(mobileInvites, r.PathValue("token"))
			ok = false
		}
		mobileInviteMu.Unlock()
		if !ok {
			http.Error(w, "Ссылка подключения недействительна или истекла", http.StatusGone)
			return
		}
		escapedURI := html.EscapeString(record.URI)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="0;url=%s"><title>Whitelist Bypass</title></head><body><main><h1>Whitelist Bypass</h1><p>Открываю профиль в Android-приложении.</p><p><a href="%s">Открыть в Whitelist Bypass</a></p><p>Ссылка действует до %s.</p></main></body></html>`,
			escapedURI, escapedURI, record.ExpiresAt.Format(time.RFC3339))
	})
	mux.Handle("POST /api/profiles/{id}/duplicate", mutate(func(w http.ResponseWriter, r *http.Request) {
		profile, err := cp.duplicateProfile(r.PathValue("id"))
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "client profile not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, profile)
	}))
	mux.Handle("POST /api/profiles/{id}/recovery/test", mutate(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := cp.profile(r.PathValue("id")); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "client profile not found"})
			return
		}
		testRecovery(w, r.PathValue("id"))
	}))
	mux.Handle("POST /api/profiles", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input profileInput
		if !decodeRequest(w, r, &input) {
			return
		}
		profile, err := cp.createProfile(input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, profile)
	}))
	mux.Handle("PATCH /api/profiles/{id}", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input profileInput
		if !decodeRequest(w, r, &input) {
			return
		}
		profile, err := cp.updateProfile(r.PathValue("id"), input)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "client profile not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, profile)
	}))
	mux.Handle("DELETE /api/profiles/{id}", mutate(func(w http.ResponseWriter, r *http.Request) {
		err := cp.deleteProfile(r.PathValue("id"))
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "client profile not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("GET /api/sessions", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, cp.listSessions())
	}))
	mux.Handle("POST /api/sessions", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input sessionInput
		if !decodeRequest(w, r, &input) {
			return
		}
		session, err := cp.startSession(input)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, session)
	}))
	mux.Handle("POST /api/sessions/{id}/wb-invite", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			InviteLink string `json:"inviteLink"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		cp.mu.Lock()
		managed, ok := cp.sessions[r.PathValue("id")]
		if ok {
			// Copy the immutable request fields while holding the control-plane lock;
			// Manager status has its own lock and is read after releasing this one.
			managedConfig := managed.Config
			cp.mu.Unlock()
			if !strings.EqualFold(managed.Manager.status().State, "waiting-for-creator") ||
				!strings.EqualFold(managedConfig.Mode, "wbstream") {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "WB session is not waiting for a creator invitation"})
				return
			}
			profileID := strings.TrimSpace(managedConfig.RecoveryProfile)
			if profileID == "" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "WB session has no managed profile"})
				return
			}
			if _, err := wbLogin.submitPendingInvite(profileID, input.InviteLink); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			cp.events.add("info", "wb-creator", "Accepted a manual WB bootstrap invitation", profileID)
			writeJSON(w, http.StatusAccepted, map[string]string{"state": "starting"})
			return
		}
		cp.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
	}))
	mux.Handle("GET /api/sessions/{id}", protect(func(w http.ResponseWriter, r *http.Request) {
		session, ok := cp.session(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusOK, session)
	}))
	mux.Handle("POST /api/sessions/{id}/stop", mutate(func(w http.ResponseWriter, r *http.Request) {
		session, err := cp.stopSession(r.PathValue("id"))
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, session)
	}))
	mux.Handle("DELETE /api/sessions/{id}", mutate(func(w http.ResponseWriter, r *http.Request) {
		err := cp.deleteSession(r.PathValue("id"))
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("GET /api/settings/recovery", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, recoveryView(""))
	}))
	mux.Handle("PATCH /api/settings/recovery", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input recoverySettingsInput
		if !decodeRequest(w, r, &input) {
			return
		}
		if err := cp.setGlobalRecoveryRecipient(input.Recipient); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, recoveryView(""))
	}))
	mux.Handle("POST /api/settings/recovery/test", mutate(func(w http.ResponseWriter, _ *http.Request) {
		testRecovery(w, "")
	}))
	mux.Handle("GET /api/events", protect(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 || limit > 200 {
			limit = 100
		}
		writeJSON(w, http.StatusOK, cp.events.list(limit))
	}))
}

func encodeMobileInvite(profile clientProfile, status sessionStatus, syncURLs ...string) (string, error) {
	if profile.ID == "" || profile.RecoveryKey == "" || status.SessionLink == "" {
		return "", errors.New("mobile invite is incomplete")
	}
	provider := strings.ToLower(strings.TrimSpace(profile.Config.Mode))
	if provider == "" {
		provider = "vk"
	}
	if !validMobileInviteLink(provider, status.SessionLink) {
		return "", fmt.Errorf("session link does not match provider %q", provider)
	}
	version, syncURL := 1, ""
	if len(syncURLs) > 0 {
		syncURL = strings.TrimSpace(syncURLs[0])
		if syncURL != "" {
			parsed, parseErr := url.Parse(syncURL)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
				return "", errors.New("mobile invite sync URL is invalid")
			}
			version = 2
		}
	}
	payload, err := json.Marshal(mobileInvitePayload{
		Version: version, Name: profile.Name, Provider: provider, Profile: profile.ID, Key: profile.RecoveryKey,
		Generation: status.Generation, Link: status.SessionLink, SyncURL: syncURL,
	})
	if err != nil {
		return "", err
	}
	return "wlb://import?data=" + base64.RawURLEncoding.EncodeToString(payload), nil
}

func validMobileInviteLink(provider, raw string) bool {
	if len(raw) < 1 || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	switch provider {
	case "vk":
		return scheme == "https" && (host == "vk.com" || strings.HasSuffix(host, ".vk.com")) &&
			strings.HasPrefix(parsed.EscapedPath(), "/call")
	case "telemost":
		return scheme == "https" && host == "telemost.yandex.ru" &&
			strings.HasPrefix(parsed.EscapedPath(), "/j/")
	case "wbstream":
		_, err := normalizeWBInvite(raw)
		return err == nil
	case "dion":
		if scheme == "dion" {
			return parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path == "" && safeInviteID(parsed.Host)
		}
		if scheme != "https" || host != "dion.vc" {
			return false
		}
		return safeInviteID(strings.TrimPrefix(strings.Trim(parsed.EscapedPath(), "/"), "event/")) &&
			strings.HasPrefix(parsed.EscapedPath(), "/event/")
	default:
		return false
	}
}

func safeInviteID(value string) bool {
	if len(value) < 3 || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func inspectProviders(secretsDir, managedSecretsDir string, wbLogin *wbLoginManager) []providerStatus {
	providers := []providerStatus{
		{ID: "vk", Name: "VK Video"},
		{ID: "telemost", Name: "Telemost"},
		{ID: "wbstream", Name: "WB Stream"},
		{ID: "dion", Name: "Dion"},
	}
	files := map[string]string{
		"vk": "cookies-vk.json", "telemost": "cookies-yandex.json",
		"dion": "cookies-dion.json",
	}
	for index := range providers {
		if providers[index].ID == "wbstream" {
			providers[index].Configured = wbLogin != nil && wbLogin.configured()
			continue
		}
		providers[index].Configured = providerCredentialFileReady(providers[index].ID, filepath.Join(managedSecretsDir, files[providers[index].ID])) ||
			providerCredentialFileReady(providers[index].ID, filepath.Join(secretsDir, files[providers[index].ID]))
	}
	return providers
}

func isActiveState(state string) bool {
	switch strings.ToLower(state) {
	case "starting", "waiting-for-creator", "running", "link-ready", "waiting-for-client", "connected", "degraded", "recovering", "stopping":
		return true
	default:
		return false
	}
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}
