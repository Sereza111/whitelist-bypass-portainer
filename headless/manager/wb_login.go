package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const (
	wbLoginLifetime = 8 * time.Minute
	wbLoginURL      = "https://stream.wb.ru/login"
	wbStreamOrigin  = "https://stream.wb.ru"
)

var digitsOnly = regexp.MustCompile(`\D`)

type wbLoginStatus struct {
	State            string     `json:"state"`
	Message          string     `json:"message"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	BrowserAvailable bool       `json:"browserAvailable"`
	Managed          bool       `json:"managed"`
	Mounted          bool       `json:"mounted"`
}

type wbLoginManager struct {
	mu       sync.Mutex
	actionMu sync.Mutex

	managedDir string
	mountedDir string
	browser    string
	state      string
	message    string
	expiresAt  *time.Time
	cancel     context.CancelFunc
	browserCtx context.Context
	deviceID   string
	generation uint64
}

func newWBLoginManager(managedDir, mountedDir, browser string) *wbLoginManager {
	login := &wbLoginManager{
		managedDir: managedDir,
		mountedDir: mountedDir,
		browser:    browser,
		state:      "idle",
		message:    "Подключи серверный WB Stream без экспорта cookies",
	}
	if wbCookieFileReady(login.managedCookiePath()) {
		login.state = "ready"
		login.message = "Серверный WB Stream подключён через панель"
	}
	return login
}

func (login *wbLoginManager) managedCookiePath() string {
	return filepath.Join(login.managedDir, "cookies-wbstream.json")
}

func (login *wbLoginManager) status() wbLoginStatus {
	login.mu.Lock()
	defer login.mu.Unlock()
	return login.statusLocked()
}

func (login *wbLoginManager) statusLocked() wbLoginStatus {
	managed := wbCookieFileReady(login.managedCookiePath())
	mounted := wbCookieFileReady(filepath.Join(login.mountedDir, "cookies-wbstream.json"))
	state, message := login.state, login.message
	if state == "idle" && !managed && mounted {
		state = "mounted"
		message = "Используется импортированный cookies-wbstream.json"
	}
	return wbLoginStatus{
		State: state, Message: message, ExpiresAt: login.expiresAt,
		BrowserAvailable: login.browser != "", Managed: managed, Mounted: mounted,
	}
}

func (login *wbLoginManager) start() (wbLoginStatus, error) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.browser == "" {
		return login.statusLocked(), errors.New("WB login browser is unavailable in this image")
	}
	if login.state == "starting" || login.state == "phone" || login.state == "code" || login.state == "authorizing" {
		return login.statusLocked(), nil
	}
	if login.cancel != nil {
		login.cancel()
	}
	login.generation++
	generation := login.generation
	ctx, cancel := context.WithTimeout(context.Background(), wbLoginLifetime)
	login.cancel = cancel
	expires := time.Now().UTC().Add(wbLoginLifetime)
	login.state = "starting"
	login.message = "Открываю защищённый вход WB Stream…"
	login.expiresAt = &expires
	login.browserCtx = nil
	login.deviceID = ""
	go login.run(ctx, generation)
	return login.statusLocked(), nil
}

func (login *wbLoginManager) run(ctx context.Context, generation uint64) {
	sessionRoot := filepath.Join(login.managedDir, ".wb-login")
	if err := os.MkdirAll(sessionRoot, 0o700); err != nil {
		login.fail(generation, "Не удалось подготовить закрытое хранилище")
		return
	}
	profileDir, err := os.MkdirTemp(sessionRoot, "browser-")
	if err != nil {
		login.fail(generation, "Не удалось создать временную сессию браузера")
		return
	}
	defer os.RemoveAll(profileDir)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(login.browser),
		chromedp.UserDataDir(profileDir),
		chromedp.WindowSize(1200, 900),
		chromedp.UserAgent(vkLoginUserAgent),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocatorCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	defer browserCancel()

	startupCtx, startupCancel := context.WithTimeout(browserCtx, 45*time.Second)
	defer startupCancel()
	if err := chromedp.Run(startupCtx,
		network.Enable(),
		chromedp.Navigate(wbLoginURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.WaitVisible("button", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(`(() => { const b = [...document.querySelectorAll('button')].find(x => x.textContent.trim() === 'Войти'); if (b) b.click(); })()`, nil),
		chromedp.WaitVisible(`input[placeholder="000 000 00 00"]`, chromedp.ByQuery),
	); err != nil {
		login.failFromContext(ctx, generation, "WB Stream не открыл форму входа")
		return
	}

	deviceID := ""
	_ = chromedp.Run(browserCtx, chromedp.Evaluate(`localStorage.getItem('wb_auth_api_device_id') || ''`, &deviceID))
	if deviceID == "" {
		deviceID = newWBDeviceID()
		encodedDeviceID, _ := json.Marshal(deviceID)
		_ = chromedp.Run(browserCtx, chromedp.Evaluate(fmt.Sprintf(`localStorage.setItem('wb_auth_api_device_id', %s)`, encodedDeviceID), nil))
	}
	login.update(generation, func() {
		login.browserCtx = browserCtx
		login.deviceID = deviceID
		login.state = "phone"
		login.message = "Введи номер телефона WB — он не сохраняется на сервере"
	})
	<-ctx.Done()
	login.mu.Lock()
	if login.generation == generation {
		login.browserCtx = nil
		login.deviceID = ""
		login.cancel = nil
		if login.state != "ready" && login.state != "failed" {
			login.state = "failed"
			login.message = "Время входа истекло — начни заново"
			login.expiresAt = nil
		}
	}
	login.mu.Unlock()
}

func (login *wbLoginManager) submitPhone(phone string) (wbLoginStatus, error) {
	digits := digitsOnly.ReplaceAllString(phone, "")
	if len(digits) == 11 && (digits[0] == '7' || digits[0] == '8') {
		digits = digits[1:]
	}
	if len(digits) != 10 {
		return login.status(), errors.New("enter the 10-digit Russian phone number without +7")
	}
	login.actionMu.Lock()
	defer login.actionMu.Unlock()
	ctx, generation, err := login.actionContext("phone")
	if err != nil {
		return login.status(), err
	}
	encodedPhone, _ := json.Marshal(digits)
	setPhoneScript := fmt.Sprintf(`(phone => { const i = document.querySelector('input[placeholder="000 000 00 00"]'); if (!i) throw new Error('phone input unavailable'); const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set; i.focus(); setter.call(i, phone); i.dispatchEvent(new InputEvent('input', {bubbles:true, data: phone, inputType:'insertText'})); i.dispatchEvent(new Event('change', {bubbles:true})); })(%s)`, encodedPhone)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(setPhoneScript, nil),
		chromedp.Click(`input[type="checkbox"]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`(() => { const b = [...document.querySelectorAll('button')].find(x => x.textContent.trim() === 'Продолжить'); if (!b || b.disabled) throw new Error('continue unavailable'); b.click(); })()`, nil),
	); err != nil {
		return login.status(), errors.New("WB Stream did not accept the phone form")
	}
	login.update(generation, func() {
		login.state = "code"
		login.message = "Введи одноразовый код из SMS или приложения Wildberries"
	})
	return login.status(), nil
}

func (login *wbLoginManager) submitCode(code string) (wbLoginStatus, error) {
	digits := digitsOnly.ReplaceAllString(code, "")
	if len(digits) < 4 || len(digits) > 8 {
		return login.status(), errors.New("enter the 4–8 digit one-time code")
	}
	login.actionMu.Lock()
	defer login.actionMu.Unlock()
	ctx, generation, err := login.actionContext("code")
	if err != nil {
		return login.status(), err
	}
	login.update(generation, func() {
		login.state = "authorizing"
		login.message = "Проверяю WB-сессию…"
	})
	encodedCode, _ := json.Marshal(digits)
	setCodeScript := fmt.Sprintf(`(code => { const i = [...document.querySelectorAll('input')].find(x => x.type !== 'checkbox' && x.offsetParent !== null); if (!i) throw new Error('code input unavailable'); const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set; i.focus(); setter.call(i, code); i.dispatchEvent(new InputEvent('input', {bubbles:true, data: code, inputType: 'insertText'})); i.dispatchEvent(new Event('change', {bubbles:true})); })(%s)`, encodedCode)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const inputs = [...document.querySelectorAll('input')].filter(x => x.type !== 'checkbox' && x.offsetParent !== null); const i = inputs[0]; if (!i) throw new Error('code input unavailable'); i.focus(); i.value = ''; i.dispatchEvent(new Event('input', {bubbles:true})); })()`, nil),
		chromedp.Evaluate(setCodeScript, nil),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`(() => { const b = [...document.querySelectorAll('button')].find(x => ['Продолжить','Подтвердить','Войти'].includes(x.textContent.trim()) && !x.disabled); if (b) b.click(); })()`, nil),
	); err != nil {
		login.returnToCode(generation, "Не удалось отправить одноразовый код")
		return login.status(), errors.New("WB Stream code form is unavailable")
	}

	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		cookies, cookieErr := wbBrowserCookies(ctx)
		if cookieErr == nil && hasWBCredentials(cookies) {
			deviceID := login.deviceIDFor(generation)
			if deviceID == "" {
				_ = chromedp.Run(ctx, chromedp.Evaluate(`localStorage.getItem('wb_auth_api_device_id') || ''`, &deviceID))
			}
			if deviceID == "" {
				deviceID = newWBDeviceID()
			}
			if validateErr := validateWBCookies(ctx, cookies, deviceID); validateErr == nil {
				if saveErr := login.saveCookies(cookies, deviceID); saveErr != nil {
					login.fail(generation, "Не удалось сохранить WB-сессию")
					return login.status(), saveErr
				}
				login.succeed(generation)
				return login.status(), nil
			}
		}
		select {
		case <-ctx.Done():
			return login.status(), errors.New("WB login session ended")
		case <-time.After(time.Second):
		}
	}
	login.returnToCode(generation, "Код не принят или истёк — запроси новый и повтори")
	return login.status(), errors.New("WB Stream did not confirm the one-time code")
}

func (login *wbLoginManager) actionContext(want string) (context.Context, uint64, error) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.state != want || login.browserCtx == nil {
		return nil, login.generation, fmt.Errorf("WB login is not waiting for %s", want)
	}
	return login.browserCtx, login.generation, nil
}

func (login *wbLoginManager) returnToCode(generation uint64, message string) {
	login.update(generation, func() {
		login.state = "code"
		login.message = message
	})
}

func (login *wbLoginManager) deviceIDFor(generation uint64) string {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.generation != generation {
		return ""
	}
	return login.deviceID
}

func wbBrowserCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		cookies, err = network.GetCookies().WithURLs([]string{
			"https://stream.wb.ru/", "https://auth-stream.wb.ru/", "https://www.wildberries.ru/",
		}).Do(actionCtx)
		return err
	}))
	return cookies, err
}

func hasWBCredentials(cookies []*network.Cookie) bool {
	required := map[string]bool{"wbx-refresh": false, "x_wbaas_token": false, "wbx-validation-key": false}
	for _, cookie := range cookies {
		if _, ok := required[cookie.Name]; ok && cookie.Value != "" {
			required[cookie.Name] = true
		}
	}
	return required["wbx-refresh"] && required["x_wbaas_token"] && required["wbx-validation-key"]
}

func validateWBCookies(parent context.Context, cookies []*network.Cookie, deviceID string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://auth-stream.wb.ru/v2/auth/slide-v3", bytes.NewReader(nil))
	if err != nil {
		return err
	}
	request.Header.Set("wb-apptype", "web")
	request.Header.Set("deviceId", deviceID)
	request.Header.Set("X-Request-ID", newWBDeviceID())
	request.Header.Set("Origin", wbStreamOrigin)
	request.Header.Set("Referer", wbStreamOrigin+"/")
	request.Header.Set("Cookie", wbCookieHeader(cookies))
	request.Header.Set("User-Agent", vkLoginUserAgent)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("WB validation status %d", response.StatusCode)
	}
	var result struct {
		Payload struct {
			AccessToken string `json:"access_token"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Payload.AccessToken == "" {
		return errors.New("WB validation returned no access token")
	}
	return nil
}

func wbCookieHeader(cookies []*network.Cookie) string {
	allow := map[string]bool{"wbx-refresh": true, "x_wbaas_token": true, "_wbauid": true, "wbx-validation-key": true}
	parts := make([]string, 0, len(allow))
	seen := make(map[string]bool)
	for _, cookie := range cookies {
		if allow[cookie.Name] && cookie.Value != "" && !seen[cookie.Name] {
			seen[cookie.Name] = true
			parts = append(parts, cookie.Name+"="+cookie.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func (login *wbLoginManager) saveCookies(cookies []*network.Cookie, deviceID string) error {
	if err := os.MkdirAll(login.managedDir, 0o700); err != nil {
		return err
	}
	stored := make([]vkStoredCookie, 0, len(cookies)+1)
	seen := make(map[string]bool)
	allow := map[string]bool{"wbx-refresh": true, "x_wbaas_token": true, "_wbauid": true, "wbx-validation-key": true}
	for _, cookie := range cookies {
		if !allow[cookie.Name] || cookie.Value == "" || seen[cookie.Name] {
			continue
		}
		seen[cookie.Name] = true
		stored = append(stored, vkStoredCookie{
			Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path,
			Expires: cookie.Expires, HTTPOnly: cookie.HTTPOnly, Secure: cookie.Secure,
		})
	}
	stored = append(stored, vkStoredCookie{Name: "__wb_device_id", Value: deviceID, Domain: "stream.wb.ru", Path: "/", Secure: true})
	body, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	tmp := login.managedCookiePath() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, login.managedCookiePath())
}

func wbCookieFileReady(path string) bool {
	return cookieFileContains(path, []string{"__wb_device_id", "wbx-refresh", "x_wbaas_token", "wbx-validation-key"}, true)
}

func providerCredentialFileReady(provider, path string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "vk":
		return cookieFileContains(path, []string{"remixsid", "remixsid6"}, false)
	case "telemost":
		return cookieFileContains(path, []string{"Session_id"}, true)
	case "wbstream":
		return wbCookieFileReady(path)
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

func newWBDeviceID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		fallback := randomSecret()
		if len(fallback) > 36 {
			fallback = fallback[:36]
		}
		return fallback
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

func (login *wbLoginManager) cancelLogin(message string) wbLoginStatus {
	login.mu.Lock()
	defer login.mu.Unlock()
	login.generation++
	if login.cancel != nil {
		login.cancel()
	}
	login.cancel = nil
	login.browserCtx = nil
	login.deviceID = ""
	login.state = "idle"
	login.message = message
	login.expiresAt = nil
	return login.statusLocked()
}

func (login *wbLoginManager) removeManagedCredentials() (wbLoginStatus, error) {
	login.cancelLogin("Серверный WB Stream отключён")
	err := os.Remove(login.managedCookiePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return login.status(), err
	}
	return login.status(), nil
}

func (login *wbLoginManager) succeed(generation uint64) {
	login.mu.Lock()
	if login.generation != generation {
		login.mu.Unlock()
		return
	}
	cancel := login.cancel
	login.state = "ready"
	login.message = "Серверный WB Stream сохранён — можно запускать WB-профили"
	login.expiresAt = nil
	login.browserCtx = nil
	login.deviceID = ""
	login.cancel = nil
	login.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (login *wbLoginManager) failFromContext(ctx context.Context, generation uint64, message string) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	login.fail(generation, message)
}

func (login *wbLoginManager) fail(generation uint64, message string) {
	login.update(generation, func() {
		login.state = "failed"
		login.message = message
		login.expiresAt = nil
		login.browserCtx = nil
		login.deviceID = ""
		login.cancel = nil
	})
}

func (login *wbLoginManager) update(generation uint64, fn func()) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.generation != generation {
		return
	}
	fn()
}

func registerWBLoginRoutes(mux *http.ServeMux, login *wbLoginManager, username, password string) {
	protect := func(handler http.HandlerFunc) http.Handler { return requireAuth(username, password, handler) }
	mutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
	}
	mux.Handle("GET /api/wb-login", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, login.status())
	}))
	mux.Handle("POST /api/wb-login/start", mutate(func(w http.ResponseWriter, _ *http.Request) {
		status, err := login.start()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusAccepted, status)
	}))
	mux.Handle("POST /api/wb-login/phone", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Phone string `json:"phone"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		status, err := login.submitPhone(input.Phone)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusOK, status)
	}))
	mux.Handle("POST /api/wb-login/code", mutate(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Code string `json:"code"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		status, err := login.submitCode(input.Code)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusOK, status)
	}))
	mux.Handle("POST /api/wb-login/cancel", mutate(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, login.cancelLogin("Вход WB отменён"))
	}))
	mux.Handle("DELETE /api/wb-login/credentials", mutate(func(w http.ResponseWriter, _ *http.Request) {
		status, err := login.removeManagedCredentials()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)
	}))
}
