package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	wbPairingLifetime = 10 * time.Minute
	wbCallLifetime    = 10 * time.Minute
)

type wbLoginStatus struct {
	State         string     `json:"state"`
	Message       string     `json:"message"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	Managed       bool       `json:"managed"`
	Mounted       bool       `json:"mounted"`
	DevicePairing bool       `json:"devicePairing"`
	CreatorName   string     `json:"creatorName,omitempty"`
	LastSeenAt    *time.Time `json:"lastSeenAt,omitempty"`
	PendingCalls  int        `json:"pendingCalls"`
}

type wbDevicePairing struct {
	tokenHash  [32]byte
	landingURL string
	expiresAt  time.Time
}

type wbCreatorBinding struct {
	CreatorID  string     `json:"creatorId"`
	DeviceID   string     `json:"deviceId"`
	Name       string     `json:"name"`
	SecretHash string     `json:"secretHash"`
	PairedAt   time.Time  `json:"pairedAt"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

type wbCallRequest struct {
	ID          string    `json:"id"`
	ProfileID   string    `json:"profileId"`
	ProfileName string    `json:"profileName"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	delivered   bool
	result      chan wbCallResult
}

type wbCallResult struct {
	link string
	err  error
}

type wbLoginManager struct {
	mu sync.Mutex

	dataDir   string
	stateFile string
	binding   *wbCreatorBinding
	pairing   *wbDevicePairing
	pending   map[string]*wbCallRequest
	events    *eventLog
}

func (login *wbLoginManager) setEventLog(events *eventLog) {
	login.mu.Lock()
	login.events = events
	login.mu.Unlock()
}

func (login *wbLoginManager) addEvent(level, message, reference string) {
	login.mu.Lock()
	events := login.events
	login.mu.Unlock()
	if events != nil {
		events.add(level, "wb-creator", message, reference)
	}
}

func newWBLoginManager(dataDir string) *wbLoginManager {
	login := &wbLoginManager{
		dataDir: dataDir, stateFile: filepath.Join(dataDir, "wb-creator.json"),
		pending: make(map[string]*wbCallRequest),
	}
	// Device-assisted WB never consumes account credentials on the server.
	// Remove the managed copy left by pre-creator versions during migration.
	_ = os.Remove(filepath.Join(dataDir, "managed-secrets", "cookies-wbstream.json"))
	_ = login.load()
	return login
}

func (login *wbLoginManager) load() error {
	body, err := os.ReadFile(login.stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var binding wbCreatorBinding
	if json.Unmarshal(body, &binding) != nil || !validCreatorID(binding.CreatorID) ||
		!validCreatorID(binding.DeviceID) || len(binding.SecretHash) != sha256.Size*2 {
		return errors.New("invalid WB creator binding")
	}
	login.binding = &binding
	return nil
}

func (login *wbLoginManager) saveLocked() error {
	if login.binding == nil {
		err := os.Remove(login.stateFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(login.dataDir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(login.binding, "", "  ")
	if err != nil {
		return err
	}
	tmp := login.stateFile + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, login.stateFile)
}

func (login *wbLoginManager) configured() bool {
	login.mu.Lock()
	defer login.mu.Unlock()
	return login.binding != nil
}

func (login *wbLoginManager) status() wbLoginStatus {
	login.mu.Lock()
	defer login.mu.Unlock()
	login.expireLocked(time.Now().UTC())
	status := wbLoginStatus{State: "idle", Message: "Привяжи Android creator", PendingCalls: len(login.pending)}
	if login.binding != nil {
		status.State = "ready"
		status.Message = "Android creator привязан; Manager ждёт готовые ссылки приглашений"
		status.Managed = true
		status.CreatorName = login.binding.Name
		status.LastSeenAt = login.binding.LastSeenAt
	}
	if login.pairing != nil {
		status.State = "device"
		status.Message = "Отсканируй QR на Android — он привяжет creator к Manager"
		status.ExpiresAt = &login.pairing.expiresAt
		status.DevicePairing = true
	}
	if len(login.pending) > 0 && login.binding != nil {
		status.State = "waiting-call"
		status.Message = "Запрошен новый WB-звонок; Android должен передать приглашение"
	}
	return status
}

func (login *wbLoginManager) expireLocked(now time.Time) {
	if login.pairing != nil && !login.pairing.expiresAt.After(now) {
		login.pairing = nil
	}
	for id, request := range login.pending {
		if !request.ExpiresAt.After(now) {
			delete(login.pending, id)
			select {
			case request.result <- wbCallResult{err: errors.New("Android creator did not return an invite in time")}:
			default:
			}
		}
	}
}

func (login *wbLoginManager) startDevicePairing(origin string) (wbLoginStatus, string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" {
		return login.status(), "", errors.New("WB phone pairing requires the public HTTPS panel URL")
	}
	token := randomSecret()
	expires := time.Now().UTC().Add(wbPairingLifetime)
	landingURL := strings.TrimRight(origin, "/") + "/wb-device#" + token
	login.mu.Lock()
	login.pairing = &wbDevicePairing{tokenHash: sha256.Sum256([]byte(token)), landingURL: landingURL, expiresAt: expires}
	login.mu.Unlock()
	login.addEvent("info", "Created Android creator pairing", "")
	return login.status(), landingURL, nil
}

func (login *wbLoginManager) pairingQRCode() ([]byte, bool) {
	login.mu.Lock()
	defer login.mu.Unlock()
	login.expireLocked(time.Now().UTC())
	if login.pairing == nil {
		return nil, false
	}
	body, err := qrcode.Encode(login.pairing.landingURL, qrcode.Medium, 360)
	return body, err == nil && len(body) > 0
}

func (login *wbLoginManager) pairDevice(bearer, deviceID, name string) (string, string, error) {
	presented := sha256.Sum256([]byte(strings.TrimSpace(bearer)))
	deviceID = strings.TrimSpace(deviceID)
	name = strings.TrimSpace(name)
	if !validCreatorID(deviceID) {
		return "", "", errors.New("invalid creator device id")
	}
	if name == "" {
		name = "Android creator"
	}
	if len([]rune(name)) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return "", "", errors.New("invalid creator name")
	}
	login.mu.Lock()
	defer login.mu.Unlock()
	login.expireLocked(time.Now().UTC())
	if login.pairing == nil || subtle.ConstantTimeCompare(presented[:], login.pairing.tokenHash[:]) != 1 {
		return "", "", errors.New("WB phone pairing token is invalid or expired")
	}
	secret := randomSecret()
	hash := sha256.Sum256([]byte(secret))
	creatorID := randomID("creator")
	login.binding = &wbCreatorBinding{
		CreatorID: creatorID, DeviceID: deviceID, Name: name,
		SecretHash: hex.EncodeToString(hash[:]), PairedAt: time.Now().UTC(),
	}
	if err := login.saveLocked(); err != nil {
		login.binding = nil
		return "", "", err
	}
	login.pairing = nil
	if login.events != nil {
		login.events.add("info", "wb-creator", "Paired Android creator", creatorID)
	}
	return creatorID, secret, nil
}

func (login *wbLoginManager) authenticate(creatorID, secret string) bool {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.binding == nil || subtle.ConstantTimeCompare([]byte(login.binding.CreatorID), []byte(strings.TrimSpace(creatorID))) != 1 {
		return false
	}
	want, err := hex.DecodeString(login.binding.SecretHash)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return false
	}
	now := time.Now().UTC()
	login.binding.LastSeenAt = &now
	_ = login.saveLocked()
	return true
}

func (login *wbLoginManager) nextCall() *wbCallRequest {
	login.mu.Lock()
	defer login.mu.Unlock()
	login.expireLocked(time.Now().UTC())
	requests := make([]*wbCallRequest, 0, len(login.pending))
	for _, request := range login.pending {
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].CreatedAt.Before(requests[j].CreatedAt) })
	if len(requests) == 0 {
		return nil
	}
	if !requests[0].delivered {
		requests[0].delivered = true
		if login.events != nil {
			login.events.add("info", "wb-creator", "Delivered call request to Android creator", requests[0].ProfileID)
		}
	}
	copy := *requests[0]
	copy.result = nil
	return &copy
}

func (login *wbLoginManager) requestCall(ctx context.Context, profileID, profileName string) (string, error) {
	login.mu.Lock()
	login.expireLocked(time.Now().UTC())
	if login.binding == nil {
		login.mu.Unlock()
		return "", errors.New("Android WB creator is not paired")
	}
	now := time.Now().UTC()
	request := &wbCallRequest{
		ID: randomID("call"), ProfileID: profileID, ProfileName: profileName,
		CreatedAt: now, ExpiresAt: now.Add(wbCallLifetime), result: make(chan wbCallResult, 1),
	}
	login.pending[request.ID] = request
	login.mu.Unlock()
	login.addEvent("info", "Requested a new WB call from Android creator", profileID)

	timer := time.NewTimer(wbCallLifetime)
	defer timer.Stop()
	select {
	case result := <-request.result:
		return result.link, result.err
	case <-ctx.Done():
		login.cancelRequest(request.ID)
		return "", ctx.Err()
	case <-timer.C:
		login.cancelRequest(request.ID)
		return "", errors.New("Android creator did not return an invite in time")
	}
}

func (login *wbLoginManager) cancelRequest(id string) {
	login.mu.Lock()
	delete(login.pending, id)
	login.mu.Unlock()
}

func (login *wbLoginManager) submitInvite(requestID, raw string) (string, string, error) {
	link, err := normalizeWBInvite(raw)
	if err != nil {
		return "", "", err
	}
	login.mu.Lock()
	login.expireLocked(time.Now().UTC())
	request, ok := login.pending[requestID]
	if ok {
		delete(login.pending, requestID)
	}
	login.mu.Unlock()
	if !ok {
		return "", "", errors.New("call request is unknown or expired")
	}
	request.result <- wbCallResult{link: link}
	login.addEvent("info", "Accepted Android WB invitation", request.ProfileID)
	return link, request.ProfileID, nil
}

func normalizeWBInvite(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 1 || len(raw) > 2048 {
		return "", errors.New("invalid WB invite link")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid WB invite link")
	}
	roomID := ""
	if strings.EqualFold(parsed.Scheme, "wbstream") && parsed.Path == "" {
		roomID = parsed.Host
	} else if strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "stream.wb.ru") {
		parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
		if len(parts) == 2 && parts[0] == "room" {
			roomID = parts[1]
		}
	}
	if !safeInviteID(roomID) {
		return "", errors.New("invalid WB invite link")
	}
	return "wbstream://" + roomID, nil
}

func validCreatorID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func (login *wbLoginManager) cancelLogin(message string) wbLoginStatus {
	login.mu.Lock()
	login.pairing = nil
	login.mu.Unlock()
	status := login.status()
	status.Message = message
	return status
}

func (login *wbLoginManager) removeManagedCredentials() (wbLoginStatus, error) {
	login.mu.Lock()
	login.binding = nil
	for id, request := range login.pending {
		delete(login.pending, id)
		request.result <- wbCallResult{err: errors.New("Android WB creator was unpaired")}
	}
	err := login.saveLocked()
	login.mu.Unlock()
	return login.status(), err
}

func registerWBLoginRoutes(mux *http.ServeMux, login *wbLoginManager, username, password string, controlPlanes ...*controlPlane) {
	var cp *controlPlane
	if len(controlPlanes) > 0 {
		cp = controlPlanes[0]
	}
	protect := func(handler http.HandlerFunc) http.Handler { return requireAuth(username, password, handler) }
	mutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
	}
	mux.Handle("GET /api/wb-login", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, login.status())
	}))
	mux.Handle("POST /api/wb-login/device/start", mutate(func(w http.ResponseWriter, r *http.Request) {
		origin, err := wbPublicPanelOrigin(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		status, landingURL, err := login.startDevicePairing(origin)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"url": landingURL, "expiresAt": status.ExpiresAt, "status": status})
	}))
	mux.Handle("GET /api/wb-login/device/qr", protect(func(w http.ResponseWriter, _ *http.Request) {
		body, ok := login.pairingQRCode()
		if !ok {
			http.Error(w, "WB phone pairing is not active", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	mux.HandleFunc("POST /api/wb-creator/pair", func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := requestBearer(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "pairing token required"})
			return
		}
		var input struct {
			DeviceID string `json:"deviceId"`
			Name     string `json:"name"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		creatorID, secret, err := login.pairDevice(bearer, input.DeviceID, input.Name)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"creatorId": creatorID, "deviceSecret": secret})
	})
	mux.HandleFunc("POST /api/wb-creator/commands/next", func(w http.ResponseWriter, r *http.Request) {
		if !authenticateCreatorRequest(login, r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "creator authentication required"})
			return
		}
		request := login.nextCall()
		if request == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, request)
	})
	mux.HandleFunc("POST /api/wb-creator/commands/{id}/invite", func(w http.ResponseWriter, r *http.Request) {
		if !authenticateCreatorRequest(login, r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "creator authentication required"})
			return
		}
		var input struct {
			InviteLink string `json:"inviteLink"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		link, profileID, err := login.submitInvite(r.PathValue("id"), input.InviteLink)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		response := map[string]any{"inviteLink": link}
		if cp != nil {
			if profile, ok := cp.profile(profileID); ok && profile.Config.Mode == "wbstream" {
				response["clientProfile"] = map[string]any{
					"name": profile.Name, "profile": profile.ID, "key": profile.RecoveryKey,
					"generation": profile.RecoveryGeneration, "provider": "wbstream",
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, response)
	})
	mux.Handle("POST /api/wb-login/cancel", mutate(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, login.cancelLogin("Привязка Android отменена"))
	}))
	mux.Handle("DELETE /api/wb-login/credentials", mutate(func(w http.ResponseWriter, _ *http.Request) {
		status, err := login.removeManagedCredentials()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)
	}))
	registerWBDeviceLandingRoutes(mux)
}

func requestBearer(r *http.Request) (string, bool) {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func authenticateCreatorRequest(login *wbLoginManager, r *http.Request) bool {
	secret, ok := requestBearer(r)
	return ok && login.authenticate(r.Header.Get("X-WLB-Creator-ID"), secret)
}

func wbPublicPanelOrigin(r *http.Request) (string, error) {
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if proto == "" && r.TLS != nil {
		proto = "https"
	}
	if proto != "https" {
		return "", errors.New("open the panel through HTTPS before pairing a phone")
	}
	host := strings.TrimSpace(r.Host)
	parsed, err := url.Parse("https://" + host)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || strings.ContainsAny(host, "\\/\r\n\x00") {
		return "", errors.New("invalid public panel host")
	}
	return parsed.String(), nil
}

func registerWBDeviceLandingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /wb-device", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>WB · Whitelist Bypass</title><link rel="stylesheet" href="/wb-device.css"></head><body><main><p class="eyebrow">DEVICE CREATOR PAIRING</p><h1>Привязка Android creator</h1><p id="status">Проверяю одноразовую привязку…</p><a id="open" hidden>Открыть в Whitelist Bypass</a><small>QR не содержит cookies или WB-токены. Код действует 10 минут.</small></main><script src="/wb-device.js"></script></body></html>`)
	})
	mux.HandleFunc("GET /wb-device.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = io.WriteString(w, `'use strict';(()=>{const token=location.hash.slice(1);const status=document.getElementById('status');const open=document.getElementById('open');if(!/^[A-Za-z0-9_-]{32,128}$/.test(token)){status.textContent='QR-код повреждён или уже недействителен.';return;}const target='wlb://wb-login?server='+encodeURIComponent(location.origin)+'&token='+encodeURIComponent(token);open.href=target;open.hidden=false;status.textContent='Открываю привязку creator в приложении…';setTimeout(()=>{location.href=target;},300);})();`)
	})
	mux.HandleFunc("GET /wb-device.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, `:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#09080b;color:#e9e2d8;font:16px system-ui,sans-serif}main{width:min(560px,calc(100% - 32px));padding:36px;border:1px solid #8f6b28;background:#141116}h1{font:500 34px Georgia,serif;margin:8px 0 18px}.eyebrow,small{color:#b3945a;font:11px ui-monospace,monospace;letter-spacing:.13em}a{display:block;margin:24px 0;padding:15px;text-align:center;background:#d7a547;color:#17100a;text-decoration:none;font-weight:700}small{display:block;line-height:1.6}`)
	})
}

func providerCredentialFileReady(provider, path string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "vk":
		return cookieFileContains(path, []string{"remixsid", "remixsid6"}, false)
	case "telemost":
		return cookieFileContains(path, []string{"Session_id"}, true)
	case "dion":
		return cookieFileContains(path, []string{"vc-refresh-token", "vc-access-token"}, true)
	default:
		return false
	}
}

func cookieFileContains(path string, requiredNames []string, requireAll bool) bool {
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 2<<20 {
		return false
	}
	var cookies []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if json.Unmarshal(body, &cookies) != nil {
		return false
	}
	required := make(map[string]bool, len(requiredNames))
	for _, name := range requiredNames {
		required[name] = false
	}
	for _, cookie := range cookies {
		if _, ok := required[cookie.Name]; ok && cookie.Value != "" {
			required[cookie.Name] = true
		}
	}
	matched := 0
	for _, ready := range required {
		if ready {
			matched++
		} else if requireAll {
			return false
		}
	}
	return matched > 0
}
