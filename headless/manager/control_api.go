package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

var recoveryMessageSender = sendVKTestMessage

func registerControlAPIRoutes(mux *http.ServeMux, cp *controlPlane, vkLogin *vkLoginManager, username, password, secretsDir string) {
	protect := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, handler)
	}
	mutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
	}
	var recoveryTestMu sync.Mutex
	recoveryTests := make(map[string]time.Time)
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
			ClientCount: len(cp.listProfiles()), Providers: inspectProviders(secretsDir, cp.managedSecretsDir),
			RecoveryDelivery: cp.recoveryConfigured(),
		})
	}))

	mux.Handle("GET /api/providers", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, inspectProviders(secretsDir, cp.managedSecretsDir))
	}))
	mux.Handle("GET /api/profiles", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, cp.listProfiles())
	}))
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

func inspectProviders(secretsDir, managedSecretsDir string) []providerStatus {
	providers := []providerStatus{
		{ID: "vk", Name: "VK Video"},
		{ID: "telemost", Name: "Telemost"},
		{ID: "wbstream", Name: "WB Stream"},
		{ID: "dion", Name: "Dion"},
	}
	files := map[string]string{
		"vk": "cookies-vk.json", "telemost": "cookies-yandex.json",
		"wbstream": "cookies-wbstream.json", "dion": "cookies-dion.json",
	}
	for index := range providers {
		providers[index].Configured = fileReady(filepath.Join(managedSecretsDir, files[providers[index].ID])) ||
			fileReady(filepath.Join(secretsDir, files[providers[index].ID]))
	}
	return providers
}

func isActiveState(state string) bool {
	switch strings.ToLower(state) {
	case "starting", "running", "link-ready", "waiting-for-client", "connected", "degraded", "recovering", "stopping":
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
