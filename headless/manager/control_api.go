package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type providerStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type overviewResponse struct {
	BuildVersion   string           `json:"buildVersion"`
	BuildCommit    string           `json:"buildCommit"`
	BuildTime      string           `json:"buildTime"`
	MaxSessions    int              `json:"maxSessions"`
	ActiveSessions int              `json:"activeSessions"`
	ClientCount    int              `json:"clientCount"`
	Providers      []providerStatus `json:"providers"`
}

func registerControlAPIRoutes(mux *http.ServeMux, cp *controlPlane, username, password, secretsDir string) {
	protect := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, handler)
	}
	mutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
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
			ClientCount: len(cp.listProfiles()), Providers: inspectProviders(secretsDir),
		})
	}))

	mux.Handle("GET /api/providers", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, inspectProviders(secretsDir))
	}))
	mux.Handle("GET /api/profiles", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, cp.listProfiles())
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

func inspectProviders(secretsDir string) []providerStatus {
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
		info, err := os.Stat(filepath.Join(secretsDir, files[providers[index].ID]))
		providers[index].Configured = err == nil && !info.IsDir() && info.Size() > 0
	}
	return providers
}

func isActiveState(state string) bool {
	switch strings.ToLower(state) {
	case "starting", "running", "link-ready", "waiting-for-client", "connected", "degraded", "stopping":
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
