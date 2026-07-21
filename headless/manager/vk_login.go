package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const (
	vkLoginLifetime  = 4 * time.Minute
	vkLoginURL       = "https://vk.com/"
	vkWebTokenURL    = "https://login.vk.com/?act=web_token"
	vkCallsAppID     = "6287487"
	vkLoginUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
)

type vkLoginStatus struct {
	State            string     `json:"state"`
	Message          string     `json:"message"`
	AccountID        string     `json:"accountId,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	ScreenshotReady  bool       `json:"screenshotReady"`
	BrowserAvailable bool       `json:"browserAvailable"`
	Managed          bool       `json:"managed"`
	Mounted          bool       `json:"mounted"`
	Warning          string     `json:"warning,omitempty"`
}

type vkStoredCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expirationDate,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
}

type vkLoginManager struct {
	mu sync.Mutex

	managedDir string
	mountedDir string
	browser    string
	state      string
	message    string
	accountID  string
	warning    string
	expiresAt  *time.Time
	screenshot []byte
	cancel     context.CancelFunc
	generation uint64
}

func newVKLoginManager(managedDir, mountedDir string) *vkLoginManager {
	login := &vkLoginManager{
		managedDir: managedDir,
		mountedDir: mountedDir,
		browser:    findVKLoginBrowser(),
		state:      "idle",
		message:    "Подключи отдельный серверный VK через QR",
	}
	if fileReady(login.managedCookiePath()) {
		login.state = "ready"
		login.message = "Серверный VK подключён через панель"
	}
	return login
}

func findVKLoginBrowser() string {
	if configured := strings.TrimSpace(os.Getenv("VK_LOGIN_BROWSER")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured
		}
	}
	for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome", "chrome"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Chromium\Application\chrome.exe`),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func (login *vkLoginManager) managedCookiePath() string {
	return filepath.Join(login.managedDir, "cookies-vk.json")
}

func (login *vkLoginManager) status() vkLoginStatus {
	login.mu.Lock()
	defer login.mu.Unlock()
	return login.statusLocked()
}

func (login *vkLoginManager) statusLocked() vkLoginStatus {
	managed := fileReady(login.managedCookiePath())
	mounted := fileReady(filepath.Join(login.mountedDir, "cookies-vk.json"))
	state, message := login.state, login.message
	if state == "idle" && !managed && mounted {
		state = "mounted"
		message = "Используется импортированный cookies-vk.json — его можно заменить QR-входом"
	}
	return vkLoginStatus{
		State: state, Message: message, AccountID: login.accountID,
		ExpiresAt: login.expiresAt, ScreenshotReady: len(login.screenshot) > 0,
		BrowserAvailable: login.browser != "", Managed: managed,
		Mounted: mounted, Warning: login.warning,
	}
}

func (login *vkLoginManager) start() (vkLoginStatus, error) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.browser == "" {
		return login.statusLocked(), errors.New("QR browser is unavailable in this image")
	}
	if login.state == "starting" || login.state == "waiting" || login.state == "authorizing" {
		return login.statusLocked(), nil
	}
	if login.cancel != nil {
		login.cancel()
	}
	login.generation++
	generation := login.generation
	ctx, cancel := context.WithTimeout(context.Background(), vkLoginLifetime)
	login.cancel = cancel
	expires := time.Now().UTC().Add(vkLoginLifetime)
	login.state = "starting"
	login.message = "Запускаю защищённое окно VK…"
	login.accountID = ""
	login.warning = ""
	login.expiresAt = &expires
	login.screenshot = nil
	go login.run(ctx, generation)
	return login.statusLocked(), nil
}

func (login *vkLoginManager) cancelLogin(message string) vkLoginStatus {
	login.mu.Lock()
	defer login.mu.Unlock()
	login.generation++
	if login.cancel != nil {
		login.cancel()
		login.cancel = nil
	}
	login.state = "idle"
	login.message = message
	login.expiresAt = nil
	login.screenshot = nil
	return login.statusLocked()
}

func (login *vkLoginManager) removeManagedCredentials() (vkLoginStatus, error) {
	login.cancelLogin("Серверный VK отключён")
	err := os.Remove(login.managedCookiePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return login.status(), err
	}
	login.mu.Lock()
	login.accountID = ""
	login.warning = ""
	status := login.statusLocked()
	login.mu.Unlock()
	return status, nil
}

func (login *vkLoginManager) screenshotPNG() ([]byte, bool) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if len(login.screenshot) == 0 {
		return nil, false
	}
	return append([]byte(nil), login.screenshot...), true
}

func (login *vkLoginManager) run(ctx context.Context, generation uint64) {
	sessionRoot := filepath.Join(login.managedDir, ".vk-login")
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

	if err := chromedp.Run(browserCtx,
		network.Enable(),
		chromedp.Navigate(vkLoginURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	); err != nil {
		log.Printf("[vk-login] browser start failed: %v", err)
		login.failFromContext(ctx, generation, "VK не открыл страницу входа")
		return
	}
	login.setWaiting(generation)

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		cookies, err := browserCookies(browserCtx)
		if err == nil && hasVKAuthCookie(cookies) {
			login.setAuthorizing(generation)
			cookieHeader := cookieHeader(cookies)
			if err := validateVKCookies(ctx, cookieHeader); err == nil {
				if err := login.saveCookies(cookies); err != nil {
					login.fail(generation, "Не удалось сохранить VK-сессию")
					return
				}
				login.succeed(generation, cookieValue(cookies, "remixuid"))
				return
			}
		} else if err := login.capture(browserCtx, generation); err != nil && ctx.Err() == nil {
			log.Printf("[vk-login] screenshot failed: %v", err)
			login.fail(generation, "Не удалось обновить QR-код")
			return
		}

		select {
		case <-ctx.Done():
			login.failFromContext(ctx, generation, "QR-код истёк — создай новый")
			return
		case <-ticker.C:
		}
	}
}

func (login *vkLoginManager) capture(ctx context.Context, generation uint64) error {
	var screenshot []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&screenshot)); err != nil {
		return err
	}
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.generation != generation {
		return context.Canceled
	}
	login.screenshot = screenshot
	return nil
}

func browserCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		cookies, err = network.GetCookies().WithURLs([]string{"https://vk.com/", "https://login.vk.com/"}).Do(actionCtx)
		return err
	}))
	return cookies, err
}

func hasVKAuthCookie(cookies []*network.Cookie) bool {
	for _, cookie := range cookies {
		if (cookie.Name == "remixsid" || cookie.Name == "remixsid6") && cookie.Value != "" {
			return true
		}
	}
	return false
}

func cookieHeader(cookies []*network.Cookie) string {
	parts := make([]string, 0, len(cookies))
	seen := make(map[string]struct{}, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name == "" || cookie.Value == "" {
			continue
		}
		key := cookie.Name + "\x00" + cookie.Domain + "\x00" + cookie.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func cookieValue(cookies []*network.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func validateVKCookies(parent context.Context, cookieHeader string) error {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	form := url.Values{"version": {"1"}, "app_id": {vkCallsAppID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vkWebTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", vkLoginUserAgent)
	req.Header.Set("Origin", "https://vk.com")
	req.Header.Set("Referer", "https://vk.com/")
	req.Header.Set("Cookie", cookieHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if result.Data.AccessToken == "" {
		return errors.New("VK did not accept the browser session")
	}
	return nil
}

func (login *vkLoginManager) saveCookies(cookies []*network.Cookie) error {
	if err := os.MkdirAll(login.managedDir, 0o700); err != nil {
		return err
	}
	stored := make([]vkStoredCookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name == "" || cookie.Value == "" {
			continue
		}
		stored = append(stored, vkStoredCookie{
			Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path,
			Expires: cookie.Expires, HTTPOnly: cookie.HTTPOnly, Secure: cookie.Secure,
		})
	}
	if len(stored) == 0 {
		return errors.New("VK returned no cookies")
	}
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

func (login *vkLoginManager) setWaiting(generation uint64) {
	login.update(generation, func() {
		login.state = "waiting"
		login.message = "Отсканируй QR вторым VK-аккаунтом и подтверди вход"
	})
}

func (login *vkLoginManager) setAuthorizing(generation uint64) {
	login.update(generation, func() {
		login.state = "authorizing"
		login.message = "Проверяю и сохраняю серверный VK…"
		login.screenshot = nil
	})
}

func (login *vkLoginManager) succeed(generation uint64, accountID string) {
	login.update(generation, func() {
		login.state = "ready"
		login.message = "Серверный VK сохранён. Новые звонки используют его; текущую сессию перезапусти"
		login.accountID = accountID
		login.expiresAt = nil
		login.screenshot = nil
		login.cancel = nil
		if accountID != "" && accountID == strings.TrimSpace(os.Getenv("VK_PEER_ID")) {
			login.warning = "Это тот же аккаунт, что VK_PEER_ID: уведомления самому себе могут не прийти"
		}
	})
}

func (login *vkLoginManager) failFromContext(ctx context.Context, generation uint64, fallback string) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	login.fail(generation, fallback)
}

func (login *vkLoginManager) fail(generation uint64, message string) {
	login.update(generation, func() {
		login.state = "failed"
		login.message = message
		login.expiresAt = nil
		login.screenshot = nil
		login.cancel = nil
	})
}

func (login *vkLoginManager) update(generation uint64, fn func()) {
	login.mu.Lock()
	defer login.mu.Unlock()
	if login.generation != generation {
		return
	}
	fn()
}

func registerVKLoginRoutes(mux *http.ServeMux, login *vkLoginManager, username, password string) {
	protect := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, handler)
	}
	mutate := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(username, password, sameOrigin(handler))
	}
	mux.Handle("GET /api/vk-login", protect(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, login.status())
	}))
	mux.Handle("POST /api/vk-login/start", mutate(func(w http.ResponseWriter, _ *http.Request) {
		status, err := login.start()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "status": status})
			return
		}
		writeJSON(w, http.StatusAccepted, status)
	}))
	mux.Handle("POST /api/vk-login/cancel", mutate(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, login.cancelLogin("QR-вход отменён"))
	}))
	mux.Handle("DELETE /api/vk-login/credentials", mutate(func(w http.ResponseWriter, _ *http.Request) {
		status, err := login.removeManagedCredentials()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)
	}))
	mux.Handle("GET /api/vk-login/screenshot", protect(func(w http.ResponseWriter, _ *http.Request) {
		body, ok := login.screenshotPNG()
		if !ok {
			http.Error(w, "QR is not ready", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}))
}

func fileReady(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}
