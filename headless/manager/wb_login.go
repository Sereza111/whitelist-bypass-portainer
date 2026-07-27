package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	qrcode "github.com/skip2/go-qrcode"
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
	ScreenshotReady  bool       `json:"screenshotReady"`
	DevicePairing    bool       `json:"devicePairing"`
}

type wbDevicePairing struct {
	tokenHash  [32]byte
	landingURL string
	expiresAt  time.Time
}

type wbDeviceCredentials struct {
	DeviceID  string            `json:"deviceId"`
	UserAgent string            `json:"userAgent"`
	Cookies   map[string]string `json:"cookies"`
}

type wbValidationStatusError struct{ Status int }

func (err *wbValidationStatusError) Error() string {
	return fmt.Sprintf("WB validation status %d", err.Status)
}

type wbValidationSchemaError struct{ Schema string }

func (err *wbValidationSchemaError) Error() string {
	return "WB validation returned no access token (" + err.Schema + ")"
}

var wbCookieValidator = validateWBCookiesWithUserAgent

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
	screenshot []byte
	generation uint64
	pairing    *wbDevicePairing
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
	pairingActive := login.pairing != nil && login.pairing.expiresAt.After(time.Now().UTC())
	if login.state == "device" && !pairingActive {
		login.pairing = nil
		login.state = "failed"
		login.message = "QR-код истёк — создай новую привязку"
		login.expiresAt = nil
	}
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
		ScreenshotReady: len(login.screenshot) > 0, DevicePairing: pairingActive,
	}
}

func (login *wbLoginManager) startDevicePairing(origin string) (wbLoginStatus, string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" {
		return login.status(), "", errors.New("WB phone pairing requires the public HTTPS panel URL")
	}
	token := randomSecret()
	expires := time.Now().UTC().Add(10 * time.Minute)
	landingURL := strings.TrimRight(origin, "/") + "/wb-device#" + token
	login.mu.Lock()
	if login.cancel != nil {
		login.cancel()
	}
	login.generation++
	login.cancel = nil
	login.browserCtx = nil
	login.deviceID = ""
	login.screenshot = nil
	login.pairing = nil
	login.pairing = &wbDevicePairing{
		tokenHash: sha256.Sum256([]byte(token)), landingURL: landingURL, expiresAt: expires,
	}
	login.state = "device"
	login.message = "Отсканируй QR телефоном и войди в WB на мобильной сети"
	login.expiresAt = &expires
	status := login.statusLocked()
	login.mu.Unlock()
	return status, landingURL, nil
}

func (login *wbLoginManager) pairingQRCode() ([]byte, bool) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.pairing == nil || !login.pairing.expiresAt.After(time.Now().UTC()) {
		return nil, false
	}
	body, err := qrcode.Encode(login.pairing.landingURL, qrcode.Medium, 360)
	return body, err == nil && len(body) > 0
}

func (login *wbLoginManager) submitDeviceCredentials(ctx context.Context, bearer string, input wbDeviceCredentials) (wbLoginStatus, error) {
	login.actionMu.Lock()
	defer login.actionMu.Unlock()

	presented := sha256.Sum256([]byte(strings.TrimSpace(bearer)))
	login.mu.Lock()
	pairing := login.pairing
	generation := login.generation
	validPairing := pairing != nil && pairing.expiresAt.After(time.Now().UTC()) &&
		subtle.ConstantTimeCompare(presented[:], pairing.tokenHash[:]) == 1
	if validPairing {
		login.state = "authorizing"
		login.message = "Проверяю WB-сессию, полученную с телефона…"
	}
	login.mu.Unlock()
	if !validPairing {
		return login.status(), errors.New("WB phone pairing token is invalid or expired")
	}

	userAgent := strings.TrimSpace(input.UserAgent)
	if !validWBUserAgent(userAgent) {
		login.returnToDevice(generation, "Телефон передал неподдерживаемый браузерный профиль — создай новый QR")
		return login.status(), errors.New("invalid WB browser profile")
	}
	cookies, err := wbDeviceCookieSet(input)
	if err != nil {
		login.returnToDevice(generation, "Телефон не передал полную WB-сессию — продолжи вход и повтори")
		return login.status(), err
	}
	if err := wbCookieValidator(ctx, cookies, input.DeviceID, userAgent); err != nil {
		log.Printf("[wb-login] mobile session validation rejected: %v", err)
		login.returnToDevice(generation, "WB не принял мобильную сессию на сервере — повтори вход")
		var statusErr *wbValidationStatusError
		if errors.As(err, &statusErr) {
			return login.status(), fmt.Errorf("WB rejected the mobile session (upstream status %d)", statusErr.Status)
		}
		var schemaErr *wbValidationSchemaError
		if errors.As(err, &schemaErr) {
			return login.status(), errors.New(schemaErr.Error())
		}
		return login.status(), errors.New("WB rejected the mobile session before an upstream response")
	}
	if err := login.saveCookiesWithUserAgent(cookies, input.DeviceID, userAgent); err != nil {
		login.fail(generation, "Не удалось сохранить WB-сессию с телефона")
		return login.status(), err
	}
	login.succeed(generation)
	return login.status(), nil
}

func wbDeviceCookieSet(input wbDeviceCredentials) ([]*network.Cookie, error) {
	if !validWBDeviceID(input.DeviceID) {
		return nil, errors.New("invalid WB device id")
	}
	allowed := []string{"wbx-refresh", "x_wbaas_token", "_wbauid", "wbx-validation-key"}
	cookies := make([]*network.Cookie, 0, len(allowed))
	for _, name := range allowed {
		value := strings.TrimSpace(input.Cookies[name])
		if value == "" {
			if name == "_wbauid" {
				continue
			}
			return nil, errors.New("incomplete WB cookie set")
		}
		if len(value) > 8192 || strings.ContainsAny(value, ";\r\n\x00") {
			return nil, errors.New("invalid WB cookie value")
		}
		cookies = append(cookies, &network.Cookie{
			Name: name, Value: value, Domain: ".wb.ru", Path: "/", Secure: true,
			HTTPOnly: name == "wbx-refresh",
		})
	}
	return cookies, nil
}

func validWBDeviceID(value string) bool {
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

func validWBUserAgent(value string) bool {
	if len(value) < 20 || len(value) > 1024 {
		return false
	}
	return !strings.ContainsAny(value, "\r\n\x00")
}

func (login *wbLoginManager) returnToDevice(generation uint64, message string) {
	login.update(generation, func() {
		login.state = "device"
		login.message = message
	})
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
	login.screenshot = nil
	login.pairing = nil
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

	startupCtx, startupCancel := context.WithTimeout(browserCtx, 2*time.Minute)
	defer startupCancel()
	if err := chromedp.Run(startupCtx,
		network.Enable(),
		chromedp.Navigate(wbLoginURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		log.Printf("[wb-login] navigation failed: %v", err)
		login.captureFailure(browserCtx, generation)
		login.failFromContext(ctx, generation, "WB Stream не открыл форму входа")
		return
	}
	if err := waitForWBPhoneForm(startupCtx); err != nil {
		log.Printf("[wb-login] phone form timeout: %v", err)
		login.captureFailure(browserCtx, generation)
		login.failFromContext(ctx, generation, "WB Stream не открыл форму входа — открой диагностику и повтори")
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

func waitForWBPhoneForm(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var state string
		err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			const visible = (e) => !!e && e.offsetParent !== null;
			const phone = [...document.querySelectorAll('input')].find(e => visible(e) && (e.type === 'tel' || /000\s*000/.test(e.placeholder || '')));
			if (phone) return 'phone';
			const login = [...document.querySelectorAll('button')].find(e => visible(e) && e.textContent.trim() === 'Войти' && !e.disabled);
			if (login) { login.click(); return 'clicked'; }
			return 'waiting';
		})()`, &state))
		if err == nil && state == "phone" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (login *wbLoginManager) captureFailure(ctx context.Context, generation uint64) {
	var screenshot []byte
	captureCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if chromedp.Run(captureCtx, chromedp.FullScreenshot(&screenshot, 85)) != nil || len(screenshot) == 0 {
		return
	}
	login.update(generation, func() { login.screenshot = screenshot })
}

func (login *wbLoginManager) screenshotPNG() ([]byte, bool) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if len(login.screenshot) == 0 {
		return nil, false
	}
	return append([]byte(nil), login.screenshot...), true
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
	setPhoneScript := fmt.Sprintf(`(phone => { const i = [...document.querySelectorAll('input')].find(e => e.offsetParent !== null && (e.type === 'tel' || /000\s*000/.test(e.placeholder || ''))); if (!i) throw new Error('phone input unavailable'); const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set; i.focus(); setter.call(i, phone); i.dispatchEvent(new InputEvent('input', {bubbles:true, data: phone, inputType:'insertText'})); i.dispatchEvent(new Event('change', {bubbles:true})); })(%s)`, encodedPhone)
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
	return validateWBCookiesWithUserAgent(parent, cookies, deviceID, vkLoginUserAgent)
}

func validateWBCookiesWithUserAgent(parent context.Context, cookies []*network.Cookie, deviceID, userAgent string) error {
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
	request.Header.Set("User-Agent", userAgent)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode != http.StatusOK {
		return &wbValidationStatusError{Status: response.StatusCode}
	}
	accessToken, schema := wbAccessTokenFromBody(body)
	if accessToken == "" {
		return &wbValidationSchemaError{Schema: schema}
	}
	return nil
}

func wbAccessTokenFromBody(body []byte) (string, string) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return "", "invalid-json"
	}
	if token := firstJSONString(top, "access_token", "accessToken"); token != "" {
		return token, "top=" + joinedJSONKeys(top)
	}
	var payload map[string]json.RawMessage
	if raw := top["payload"]; len(raw) > 0 && json.Unmarshal(raw, &payload) == nil {
		if token := firstJSONString(payload, "access_token", "accessToken"); token != "" {
			return token, "top=" + joinedJSONKeys(top) + " payload=" + joinedJSONKeys(payload)
		}
	}
	return "", "top=" + joinedJSONKeys(top) + " payload=" + joinedJSONKeys(payload)
}

func firstJSONString(values map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw := values[key]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != "" {
			return value
		}
	}
	return ""
}

func joinedJSONKeys(values map[string]json.RawMessage) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if len(key) <= 64 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
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
	return login.saveCookiesWithUserAgent(cookies, deviceID, vkLoginUserAgent)
}

func (login *wbLoginManager) saveCookiesWithUserAgent(cookies []*network.Cookie, deviceID, userAgent string) error {
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
	stored = append(stored, vkStoredCookie{Name: "__wb_user_agent", Value: userAgent, Domain: "stream.wb.ru", Path: "/", Secure: true})
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
	login.screenshot = nil
	login.pairing = nil
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
	login.pairing = nil
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
		login.pairing = nil
		login.screenshot = nil
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
	mux.Handle("GET /api/wb-login/screenshot", protect(func(w http.ResponseWriter, _ *http.Request) {
		body, ok := login.screenshotPNG()
		if !ok {
			http.Error(w, "WB diagnostic screenshot is not ready", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}))
	mux.Handle("POST /api/wb-login/start", mutate(func(w http.ResponseWriter, _ *http.Request) {
		status, err := login.start()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusAccepted, status)
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
		writeJSON(w, http.StatusCreated, map[string]any{
			"url": landingURL, "expiresAt": status.ExpiresAt, "status": status,
		})
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
	mux.HandleFunc("POST /api/wb-login/device/credentials", func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(bearer) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "pairing token required"})
			return
		}
		var input wbDeviceCredentials
		if !decodeRequest(w, r, &input) {
			return
		}
		status, err := login.submitDeviceCredentials(r.Context(), bearer, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
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
	registerWBDeviceLandingRoutes(mux)
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
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		strings.ContainsAny(host, "\\/\r\n\x00") {
		return "", errors.New("invalid public panel host")
	}
	return parsed.String(), nil
}

func registerWBDeviceLandingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /wb-device", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>WB · Whitelist Bypass</title><link rel="stylesheet" href="/wb-device.css"></head><body><main><p class="eyebrow">DEVICE ASSISTED LOGIN</p><h1>Вход WB через телефон</h1><p id="status">Проверяю одноразовую привязку…</p><a id="open" hidden>Открыть в Whitelist Bypass</a><small>Пароль панели и cookies не находятся в QR-коде. Код действует 10 минут.</small></main><script src="/wb-device.js"></script></body></html>`)
	})
	mux.HandleFunc("GET /wb-device.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = io.WriteString(w, `'use strict';(()=>{const token=location.hash.slice(1);const status=document.getElementById('status');const open=document.getElementById('open');if(!/^[A-Za-z0-9_-]{32,128}$/.test(token)){status.textContent='QR-код повреждён или уже недействителен.';return;}const target='wlb://wb-login?server='+encodeURIComponent(location.origin)+'&token='+encodeURIComponent(token);open.href=target;open.hidden=false;status.textContent='Открываю защищённый вход в приложении…';setTimeout(()=>{location.href=target;},300);})();`)
	})
	mux.HandleFunc("GET /wb-device.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, `:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#09080b;color:#e9e2d8;font:16px system-ui,sans-serif}main{width:min(560px,calc(100% - 32px));padding:36px;border:1px solid #8f6b28;background:#141116}h1{font:500 34px Georgia,serif;margin:8px 0 18px}.eyebrow,small{color:#b3945a;font:11px ui-monospace,monospace;letter-spacing:.13em}a{display:block;margin:24px 0;padding:15px;text-align:center;background:#d7a547;color:#17100a;text-decoration:none;font-weight:700}small{display:block;line-height:1.6}`)
	})
}
