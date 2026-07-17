package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	Version     = "0.5.0-alpha.2"
	BuildCommit = "unknown"
	BuildTime   = "unknown"
)

//go:embed web/*
var webFiles embed.FS

type sessionRequest struct {
	Mode             string `json:"mode"`
	Resources        string `json:"resources"`
	DisplayName      string `json:"displayName"`
	ExistingLink     string `json:"existingLink"`
	VideoReliability string `json:"videoReliability"`
	KCPProfile       string `json:"kcpProfile"`
}

type sessionStatus struct {
	State        string            `json:"state"`
	Mode         string            `json:"mode"`
	Resources    string            `json:"resources"`
	DisplayName  string            `json:"displayName"`
	Reliability  string            `json:"videoReliability"`
	KCPProfile   string            `json:"kcpProfile"`
	StartedAt    time.Time         `json:"startedAt,omitempty"`
	SessionLink  string            `json:"sessionLink,omitempty"`
	ExitError    string            `json:"exitError,omitempty"`
	BuildVersion string            `json:"buildVersion"`
	BuildCommit  string            `json:"buildCommit"`
	Logs         []string          `json:"logs"`
	Metrics      map[string]string `json:"metrics,omitempty"`
}

type manager struct {
	mu sync.Mutex

	binsDir    string
	secretsDir string
	dataDir    string
	linkFile   string

	cmd     *exec.Cmd
	done    chan struct{}
	state   string
	request sessionRequest
	started time.Time
	link    string
	exitErr string
	logs    *logRing
}

type logRing struct {
	mu      sync.Mutex
	lines   []string
	pending string
	max     int
}

var joinLinkPattern = regexp.MustCompile(`(?i)(join[_ -]?link\s*[:=]\s*)(\S+)`)

func newLogRing(max int) *logRing {
	return &logRing{max: max}
}

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	body := r.pending + string(p)
	parts := strings.Split(body, "\n")
	r.pending = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		line = strings.TrimRight(line, "\r")
		line = joinLinkPattern.ReplaceAllString(line, "${1}[redacted; use Session link]")
		if strings.TrimSpace(line) == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > r.max {
		r.lines = append([]string(nil), r.lines[len(r.lines)-r.max:]...)
	}
	return len(p), nil
}

func (r *logRing) add(format string, args ...any) {
	_, _ = r.Write([]byte(fmt.Sprintf(format, args...) + "\n"))
}

func (r *logRing) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.lines...)
	if strings.TrimSpace(r.pending) != "" {
		out = append(out, r.pending)
	}
	return out
}

func newManager() *manager {
	dataDir := envOr("DATA_DIR", "/data")
	return newManagerAt(dataDir)
}

func newManagerAt(dataDir string) *manager {
	return &manager{
		binsDir:    envOr("BINS_DIR", "/opt/wlb/bin"),
		secretsDir: envOr("SECRETS_DIR", "/run/secrets/wlb"),
		dataDir:    dataDir,
		linkFile:   envOr("LINK_FILE", filepath.Join(dataDir, "manager-session-link.txt")),
		state:      "stopped",
		logs:       newLogRing(400),
	}
}

func (m *manager) normalizeRequest(req sessionRequest) (sessionRequest, error) {
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	req.Resources = strings.ToLower(strings.TrimSpace(req.Resources))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.ExistingLink = strings.TrimSpace(req.ExistingLink)
	req.VideoReliability = strings.ToLower(strings.TrimSpace(req.VideoReliability))
	req.KCPProfile = strings.ToLower(strings.TrimSpace(req.KCPProfile))
	if req.Mode == "" {
		req.Mode = "vk"
	}
	if req.Resources == "" {
		req.Resources = "default"
	}
	if req.DisplayName == "" {
		req.DisplayName = "Headless"
	}
	if req.VideoReliability == "" {
		req.VideoReliability = "auto"
	}
	if req.KCPProfile == "" {
		req.KCPProfile = "balanced"
	}
	switch req.Mode {
	case "vk", "telemost", "wbstream", "dion":
	default:
		return req, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	switch req.Resources {
	case "moderate", "default", "unlimited":
	default:
		return req, fmt.Errorf("unsupported resources mode %q", req.Resources)
	}
	if req.VideoReliability != "auto" && req.VideoReliability != "raw" {
		return req, errors.New("videoReliability must be auto or raw")
	}
	if req.KCPProfile != "fast" && req.KCPProfile != "balanced" && req.KCPProfile != "stable" {
		return req, errors.New("kcpProfile must be fast, balanced, or stable")
	}
	return req, nil
}

func (m *manager) commandFor(req sessionRequest) (*exec.Cmd, error) {
	binaryNames := map[string]string{
		"vk":       "headless-vk-creator",
		"telemost": "headless-telemost-creator",
		"wbstream": "headless-wbstream-creator",
		"dion":     "headless-dion-creator",
	}
	cookieNames := map[string]string{
		"vk":       "cookies-vk.json",
		"telemost": "cookies-yandex.json",
		"wbstream": "cookies-wbstream.json",
		"dion":     "cookies-dion.json",
	}
	binaryPath := filepath.Join(m.binsDir, binaryNames[req.Mode])
	cookiePath := filepath.Join(m.secretsDir, cookieNames[req.Mode])
	if info, err := os.Stat(binaryPath); err != nil || info.IsDir() {
		return nil, fmt.Errorf("creator binary unavailable: %s", binaryPath)
	}
	if info, err := os.Stat(cookiePath); err != nil || info.Size() == 0 {
		return nil, fmt.Errorf("cookie file unavailable or empty: %s", cookiePath)
	}

	args := []string{"--cookies", cookiePath, "--resources", req.Resources, "--write-file", m.linkFile}
	switch req.Mode {
	case "vk":
		if req.ExistingLink != "" {
			args = append(args, "--vk-link", req.ExistingLink)
		} else if peerID := strings.TrimSpace(os.Getenv("VK_PEER_ID")); peerID != "" {
			args = append(args, "--peer-id", peerID)
		}
		args = append(args, "--video-reliability", req.VideoReliability)
		args = append(args, "--kcp-profile", req.KCPProfile)
	case "telemost":
		if req.ExistingLink != "" {
			args = append(args, "--tm-link", req.ExistingLink)
		}
	case "wbstream", "dion":
		if req.ExistingLink != "" {
			args = append(args, "--room", req.ExistingLink)
		}
		args = append(args, "--name", req.DisplayName)
	}
	if value := strings.TrimSpace(os.Getenv("UPSTREAM_SOCKS")); value != "" {
		args = append(args, "--upstream-socks", value)
	}
	if value := strings.TrimSpace(os.Getenv("UPSTREAM_USER")); value != "" {
		args = append(args, "--upstream-user", value)
	}
	if value := strings.TrimSpace(os.Getenv("UPSTREAM_PASS")); value != "" {
		args = append(args, "--upstream-pass", value)
	}
	cmd := exec.Command(binaryPath, args...)
	configureChildProcess(cmd)
	return cmd, nil
}

func (m *manager) start(req sessionRequest) error {
	normalized, err := m.normalizeRequest(req)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.cmd != nil {
		m.mu.Unlock()
		return errors.New("a Creator session is already running")
	}
	if err := os.MkdirAll(m.dataDir, 0o700); err != nil {
		m.mu.Unlock()
		return err
	}
	_ = os.Remove(m.linkFile)
	cmd, err := m.commandFor(normalized)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	cmd.Stdout = m.logs
	cmd.Stderr = m.logs
	m.state = "starting"
	m.request = normalized
	m.started = time.Now().UTC()
	m.link = ""
	m.exitErr = ""
	m.done = make(chan struct{})
	m.cmd = cmd
	m.logs.add("[manager] starting mode=%s resources=%s reliability=%s kcp_profile=%s", normalized.Mode, normalized.Resources, normalized.VideoReliability, normalized.KCPProfile)
	if err := cmd.Start(); err != nil {
		m.cmd = nil
		m.state = "failed"
		m.exitErr = err.Error()
		close(m.done)
		m.mu.Unlock()
		return err
	}
	m.state = "running"
	m.mu.Unlock()

	go m.wait(cmd)
	go m.watchLink(cmd)
	return nil
}

func (m *manager) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != cmd {
		return
	}
	if err != nil && m.state != "stopping" {
		m.state = "failed"
		m.exitErr = err.Error()
		m.logs.add("[manager] Creator exited with error: %v", err)
	} else {
		m.state = "stopped"
		m.logs.add("[manager] Creator stopped")
	}
	m.cmd = nil
	close(m.done)
}

func (m *manager) watchLink(cmd *exec.Cmd) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		if m.cmd != cmd {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		body, err := os.ReadFile(m.linkFile)
		if err != nil {
			continue
		}
		lines := strings.Fields(string(body))
		if len(lines) == 0 {
			continue
		}
		link := lines[len(lines)-1]
		m.mu.Lock()
		if m.cmd == cmd {
			m.link = link
			if m.state == "running" {
				m.state = "link-ready"
			}
		}
		m.mu.Unlock()
	}
}

func (m *manager) stop() error {
	m.mu.Lock()
	cmd := m.cmd
	done := m.done
	if cmd == nil || cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}
	m.state = "stopping"
	pid := cmd.Process.Pid
	m.mu.Unlock()

	m.logs.add("[manager] stopping Creator pid=%d", pid)
	_ = signalChildProcess(cmd, false)
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		m.logs.add("[manager] Creator did not stop in time; killing process group")
		_ = signalChildProcess(cmd, true)
		<-done
		return nil
	}
}

func (m *manager) status() sessionStatus {
	m.mu.Lock()
	status := sessionStatus{
		State:        m.state,
		Mode:         m.request.Mode,
		Resources:    m.request.Resources,
		DisplayName:  m.request.DisplayName,
		Reliability:  m.request.VideoReliability,
		KCPProfile:   m.request.KCPProfile,
		StartedAt:    m.started,
		SessionLink:  m.link,
		ExitError:    m.exitErr,
		BuildVersion: Version,
		BuildCommit:  BuildCommit,
	}
	m.mu.Unlock()
	status.Logs = m.logs.snapshot()
	status.Metrics = latestMetrics(status.Logs)
	if status.State == "running" || status.State == "link-ready" {
		status.State = deriveRuntimeState(status.State, status.Logs)
	}
	return status
}

func latestMetrics(lines []string) map[string]string {
	for i := len(lines) - 1; i >= 0; i-- {
		marker := strings.Index(lines[i], "METRICS ")
		if marker < 0 {
			continue
		}
		values := make(map[string]string)
		for _, field := range strings.Fields(lines[i][marker+len("METRICS "):]) {
			key, value, ok := strings.Cut(field, "=")
			if ok && key != "" {
				values[key] = value
			}
		}
		return values
	}
	return nil
}

func deriveRuntimeState(fallback string, lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		switch {
		case strings.Contains(line, "stalled") || strings.Contains(line, "Rejoining"):
			return "degraded"
		case strings.Contains(line, "=== TUNNEL CONNECTED ===") || strings.Contains(line, "handshake status=ok"):
			return "connected"
		case strings.Contains(line, "CALL CREATED") || strings.Contains(line, "Wrote call link"):
			return "waiting-for-client"
		}
	}
	return fallback
}

func main() {
	listen := flag.String("listen", ":8080", "panel listen address")
	flag.Parse()

	username := envOr("PANEL_USERNAME", "admin")
	password := os.Getenv("PANEL_PASSWORD")
	if len(password) < 12 {
		log.Fatal("PANEL_PASSWORD is required and must contain at least 12 characters")
	}
	log.Printf("[build] version=%s commit=%s built=%s", Version, BuildCommit, BuildTime)

	dataDir := envOr("DATA_DIR", "/data")
	cp, err := newControlPlane(dataDir, envInt("MAX_SESSIONS", 4))
	if err != nil {
		log.Fatalf("control plane: %v", err)
	}
	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	registerControlAPIRoutes(mux, cp, username, password, envOr("SECRETS_DIR", "/run/secrets/wlb"))
	mux.Handle("/", requireAuth(username, password, http.FileServer(http.FS(webRoot))))

	handler := securityHeaders(mux)
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("[manager] panel listening on %s", *listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("panel: %v", err)
		}
	}()

	if strings.EqualFold(os.Getenv("AUTO_START"), "true") {
		var profile clientProfile
		for _, candidate := range cp.listProfiles() {
			if candidate.Name == "Autostart" {
				profile = candidate
				break
			}
		}
		var createErr error
		if profile.ID == "" {
			enabled := true
			profile, createErr = cp.createProfile(profileInput{
				Name: "Autostart", Enabled: &enabled, MaxSessions: 1,
				Config: sessionRequest{
					Mode: envOr("CREATOR_MODE", "vk"), Resources: envOr("RESOURCES", "default"),
					DisplayName:      envOr("DISPLAY_NAME", "Headless"),
					VideoReliability: envOr("VIDEO_RELIABILITY", "auto"), KCPProfile: envOr("KCP_PROFILE", "balanced"),
				},
			})
		}
		if createErr == nil {
			config := profile.Config
			config.ExistingLink = os.Getenv("EXISTING_LINK")
			_, createErr = cp.startSession(sessionInput{ClientID: profile.ID, Config: &config})
		}
		if createErr != nil {
			log.Printf("[manager] auto-start failed: %v", createErr)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	cp.stopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func requireAuth(username, password string, next http.Handler) http.Handler {
	wantUser := sha256.Sum256([]byte(username))
	wantPass := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		gotUser := sha256.Sum256([]byte(user))
		gotPass := sha256.Sum256([]byte(pass))
		if !ok || subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) != 1 ||
			subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="WLB Manager", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				http.Error(w, "origin rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
