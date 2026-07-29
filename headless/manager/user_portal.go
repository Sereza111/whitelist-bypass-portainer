package main

import (
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type userProfileView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Enabled     bool           `json:"enabled"`
	MaxSessions int            `json:"maxSessions"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
	Config      sessionRequest `json:"config"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	AutoRestart bool           `json:"autoRestart"`
}

type userSessionStatus struct {
	State        string     `json:"state"`
	Mode         string     `json:"mode"`
	StartedAt    time.Time  `json:"startedAt,omitempty"`
	SessionLink  string     `json:"sessionLink,omitempty"`
	ExitError    string     `json:"exitError,omitempty"`
	Generation   int        `json:"generation,omitempty"`
	RestartCount int        `json:"restartCount,omitempty"`
	NextRetryAt  *time.Time `json:"nextRetryAt,omitempty"`
}

type userSessionView struct {
	ID         string            `json:"id"`
	ClientID   string            `json:"clientId"`
	ClientName string            `json:"clientName"`
	CreatedAt  time.Time         `json:"createdAt"`
	Status     userSessionStatus `json:"status"`
}

func registerUserPortalRoutes(mux *http.ServeMux, cp *controlPlane, users *userAccountStore, wbLogin *wbLoginManager, username, password, secretsDir string) {
	var loginMu sync.Mutex
	loginAttempts := make(map[string]struct {
		Count   int
		ResetAt time.Time
	})
	servePortal := func(w http.ResponseWriter, _ *http.Request) {
		body, err := fs.ReadFile(webFiles, "web/portal.html")
		if err != nil {
			http.Error(w, "portal unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}
	mux.HandleFunc("GET /portal", servePortal)
	mux.HandleFunc("GET /portal/", servePortal)
	mux.HandleFunc("GET /signup", servePortal)
	mux.HandleFunc("GET /signup/", servePortal)
	mux.HandleFunc("GET /portal.css", func(w http.ResponseWriter, _ *http.Request) {
		body, err := fs.ReadFile(webFiles, "web/portal.css")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /portal.js", func(w http.ResponseWriter, _ *http.Request) {
		body, err := fs.ReadFile(webFiles, "web/portal.js")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(body)
	})

	adminProtect := func(handler http.HandlerFunc) http.Handler { return requireAuth(username, password, handler) }
	adminMutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
	}
	userProtect := func(handler func(http.ResponseWriter, *http.Request, userAccount)) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := users.authenticate(r)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user authentication required"})
				return
			}
			handler(w, r, user)
		})
	}
	userMutate := func(handler func(http.ResponseWriter, *http.Request, userAccount)) http.Handler {
		return sameOrigin(userProtect(handler))
	}

	mux.Handle("GET /api/users", adminProtect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, users.listUsers())
	}))
	mux.Handle("POST /api/users/invites", adminMutate(func(w http.ResponseWriter, r *http.Request) {
		origin, err := wbPublicPanelOrigin(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "open the panel through HTTPS before creating an invitation"})
			return
		}
		token, invite, err := users.createInvitation()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not persist invitation"})
			return
		}
		cp.events.add("info", "user", "Created a one-time user invitation", invite.ID)
		writeJSON(w, http.StatusCreated, map[string]any{
			"url":       strings.TrimRight(origin, "/") + "/signup#invite=" + url.QueryEscape(token),
			"expiresAt": invite.ExpiresAt,
		})
	}))
	mux.Handle("POST /api/users/{id}/disable", adminMutate(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Disabled bool `json:"disabled"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		user, err := users.setDisabled(r.PathValue("id"), input.Disabled)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update user"})
			return
		}
		writeJSON(w, http.StatusOK, user)
	}))

	mux.Handle("POST /api/user/register", sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Invite   string `json:"invite"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		user, err := users.register(input.Invite, input.Username, input.Password)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		setUserSessionCookie(w, r, users.newSession(user.ID))
		cp.events.add("info", "user", "Registered invited user", user.ID)
		writeJSON(w, http.StatusCreated, publicUser{ID: user.ID, Username: user.Username, CreatedAt: user.CreatedAt})
	})))
	mux.Handle("POST /api/user/login", sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		attemptKey := tokenDigest(strings.ToLower(strings.TrimSpace(input.Username)))
		loginMu.Lock()
		attempt := loginAttempts[attemptKey]
		if time.Now().After(attempt.ResetAt) {
			attempt = struct {
				Count   int
				ResetAt time.Time
			}{ResetAt: time.Now().Add(time.Minute)}
		}
		if attempt.Count >= 5 {
			loginMu.Unlock()
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts; wait one minute"})
			return
		}
		loginMu.Unlock()
		user, ok := users.login(input.Username, input.Password)
		if !ok {
			loginMu.Lock()
			attempt.Count++
			loginAttempts[attemptKey] = attempt
			loginMu.Unlock()
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
			return
		}
		loginMu.Lock()
		delete(loginAttempts, attemptKey)
		loginMu.Unlock()
		setUserSessionCookie(w, r, users.newSession(user.ID))
		writeJSON(w, http.StatusOK, publicUser{ID: user.ID, Username: user.Username, CreatedAt: user.CreatedAt})
	})))
	mux.Handle("POST /api/user/logout", userMutate(func(w http.ResponseWriter, r *http.Request, _ userAccount) {
		users.logout(r)
		clearUserSessionCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.Handle("GET /api/user/me", userProtect(func(w http.ResponseWriter, _ *http.Request, user userAccount) {
		writeJSON(w, http.StatusOK, publicUser{ID: user.ID, Username: user.Username, CreatedAt: user.CreatedAt})
	}))
	mux.Handle("GET /api/user/providers", userProtect(func(w http.ResponseWriter, _ *http.Request, _ userAccount) {
		writeJSON(w, http.StatusOK, inspectProviders(secretsDir, cp.managedSecretsDir, wbLogin))
	}))

	providerConfigured := func(mode string) bool {
		for _, provider := range inspectProviders(secretsDir, cp.managedSecretsDir, wbLogin) {
			if provider.ID == mode {
				return provider.Configured
			}
		}
		return false
	}
	normalizeUserInput := func(user userAccount, input profileInput) (profileInput, error) {
		mode := strings.ToLower(strings.TrimSpace(input.Config.Mode))
		if !providerConfigured(mode) {
			return profileInput{}, errors.New("selected provider is not configured by the administrator")
		}
		enabled, autoRestart := true, true
		input.Enabled = &enabled
		input.AutoRestart = &autoRestart
		input.MaxSessions = 1
		input.ExpiresAt = nil
		input.RecoveryRecipient = optionalString{}
		input.Config = sessionRequest{
			Mode: mode, Resources: "default", DisplayName: strings.TrimSpace(input.Name),
			VideoReliability: "auto", KCPProfile: "auto",
		}
		if input.Config.DisplayName == "" {
			input.Config.DisplayName = user.Username
		}
		return input, nil
	}
	mux.Handle("GET /api/user/profiles", userProtect(func(w http.ResponseWriter, _ *http.Request, user userAccount) {
		profiles := cp.listProfilesFor(user.ID, false)
		result := make([]userProfileView, 0, len(profiles))
		for _, profile := range profiles {
			result = append(result, profileForUser(profile))
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.Handle("POST /api/user/profiles", userMutate(func(w http.ResponseWriter, r *http.Request, user userAccount) {
		var input profileInput
		if !decodeRequest(w, r, &input) {
			return
		}
		input, err := normalizeUserInput(user, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		profile, err := cp.createProfileFor(user.ID, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, profileForUser(profile))
	}))
	mux.Handle("PATCH /api/user/profiles/{id}", userMutate(func(w http.ResponseWriter, r *http.Request, user userAccount) {
		var input profileInput
		if !decodeRequest(w, r, &input) {
			return
		}
		input, err := normalizeUserInput(user, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		profile, err := cp.updateProfileFor(user.ID, r.PathValue("id"), input, false)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "profile not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, profileForUser(profile))
	}))
	mux.Handle("DELETE /api/user/profiles/{id}", userMutate(func(w http.ResponseWriter, r *http.Request, user userAccount) {
		err := cp.deleteProfileFor(user.ID, r.PathValue("id"), false)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "profile not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("GET /api/user/sessions", userProtect(func(w http.ResponseWriter, _ *http.Request, user userAccount) {
		sessions := cp.listSessionsFor(user.ID, false)
		result := make([]userSessionView, 0, len(sessions))
		for _, session := range sessions {
			result = append(result, sessionForUser(session))
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.Handle("POST /api/user/sessions", userMutate(func(w http.ResponseWriter, r *http.Request, user userAccount) {
		var input sessionInput
		if !decodeRequest(w, r, &input) {
			return
		}
		input.Config = nil
		session, err := cp.startSessionFor(user.ID, input, false)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, sessionForUser(session))
	}))
	mux.Handle("POST /api/user/sessions/{id}/stop", userMutate(func(w http.ResponseWriter, r *http.Request, user userAccount) {
		session, err := cp.stopSessionFor(user.ID, r.PathValue("id"), false)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, sessionForUser(session))
	}))

	var inviteMu sync.Mutex
	invites := make(map[string]mobileInviteRecord)
	mux.Handle("POST /api/user/profiles/{id}/mobile-invite", userMutate(func(w http.ResponseWriter, r *http.Request, user userAccount) {
		origin, err := wbPublicPanelOrigin(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "open the portal through HTTPS"})
			return
		}
		profile, ok := cp.profileFor(user.ID, r.PathValue("id"), false)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "profile not found"})
			return
		}
		var encoded string
		for _, session := range cp.listSessionsFor(user.ID, false) {
			if session.ClientID == profile.ID && session.Status.SessionLink != "" {
				syncURL := strings.TrimRight(origin, "/") + "/api/client-profiles/" + url.PathEscape(profile.ID) + "/invite"
				encoded, err = encodeMobileInvite(profile, session.Status, syncURL)
				break
			}
		}
		if err != nil || encoded == "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "start the profile and wait for connection readiness first"})
			return
		}
		now, token := time.Now().UTC(), randomSecret()
		expires := now.Add(15 * time.Minute)
		inviteMu.Lock()
		for key, record := range invites {
			if !record.ExpiresAt.After(now) {
				delete(invites, key)
			}
		}
		invites[token] = mobileInviteRecord{URI: encoded, ExpiresAt: expires}
		inviteMu.Unlock()
		writeJSON(w, http.StatusCreated, mobileInviteResponse{URL: strings.TrimRight(origin, "/") + "/user-join/" + token, ExpiresAt: expires})
	}))
	mux.HandleFunc("GET /user-join/{token}", func(w http.ResponseWriter, r *http.Request) {
		inviteMu.Lock()
		record, ok := invites[r.PathValue("token")]
		if ok {
			delete(invites, r.PathValue("token"))
		}
		inviteMu.Unlock()
		if !ok || !record.ExpiresAt.After(time.Now()) {
			http.Error(w, "Ссылка недействительна или истекла", http.StatusGone)
			return
		}
		encoded := html.EscapeString(record.URI)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="0;url=%s"><title>Whitelist Bypass</title></head><body><main><h1>Whitelist Bypass</h1><p><a href="%s">Открыть подключение в приложении</a></p></main></body></html>`, encoded, encoded)
	})
}

func profileForUser(profile clientProfile) userProfileView {
	config := profile.Config
	config.ExistingLink = ""
	return userProfileView{
		ID: profile.ID, Name: profile.Name, Enabled: profile.Enabled, MaxSessions: profile.MaxSessions,
		ExpiresAt: profile.ExpiresAt, Config: config, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
		AutoRestart: profile.AutoRestart,
	}
}

func sessionForUser(session sessionView) userSessionView {
	status := session.Status
	return userSessionView{
		ID: session.ID, ClientID: session.ClientID, ClientName: session.ClientName, CreatedAt: session.CreatedAt,
		Status: userSessionStatus{
			State: status.State, Mode: status.Mode, StartedAt: status.StartedAt, SessionLink: status.SessionLink,
			ExitError: status.ExitError, Generation: status.Generation, RestartCount: status.RestartCount, NextRetryAt: status.NextRetryAt,
		},
	}
}
