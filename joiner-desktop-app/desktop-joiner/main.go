// Command desktop-joiner is the engine behind the desktop joiner GUI.
// On Windows it brings up a wintun adapter so every IP packet on the
// host is steered through the resulting SOCKS5 proxy. On Linux it
// only exposes the SOCKS5 proxy (TUN routing is left to the user).
//
// On Windows it must run with administrator rights (the embedded
// manifest asks for them); creating wintun adapters and editing the
// route table both require elevation.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"whitelist-bypass/relay/common"
	"whitelist-bypass/relay/desktoptun"
	"whitelist-bypass/relay/dion"
	"whitelist-bypass/relay/pion"
	joinerCommon "whitelist-bypass/relay/pion/headless-joiner-common"
	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"
)

type statusEmitter struct{}

var tunnelLostCh = make(chan struct{}, 1)
var selfHealReconnect bool

func (statusEmitter) EmitStatus(status string) {
	log.Printf("[status] %s", status)
	// CAPTCHA:url is fired by the VK auth path when an interactive
	// captcha is required. The Electron wrapper watches stdout for
	// this exact prefix and opens a BrowserWindow at the URL.
	if strings.HasPrefix(status, "CAPTCHA:") {
		fmt.Printf("STATUS:%s\n", status)
	}
	if status == common.StatusTunnelLost && !selfHealReconnect {
		select {
		case tunnelLostCh <- struct{}{}:
		default:
		}
	}
}
func (statusEmitter) EmitStatusError(msg string) {
	log.Printf("[status] ERROR: %s", msg)
	select {
	case tunnelLostCh <- struct{}{}:
	default:
	}
}

type fileCacheStore struct{ dir string }

func newFileCacheStore() *fileCacheStore {
	dir, _ := os.UserCacheDir()
	if dir == "" {
		dir = os.TempDir()
	}
	cacheDir := filepath.Join(dir, "whitelist-bypass")
	os.MkdirAll(cacheDir, 0755)
	return &fileCacheStore{dir: cacheDir}
}

func (c *fileCacheStore) Save(key, value string) {
	os.WriteFile(filepath.Join(c.dir, key), []byte(value), 0644)
}

func (c *fileCacheStore) Load(key string) string {
	data, err := os.ReadFile(filepath.Join(c.dir, key))
	if err != nil {
		return ""
	}
	return string(data)
}

const (
	tunAdapter = "WhitelistBypass"
	tunIP      = "10.99.0.2"
	tunMask    = "255.255.255.0"
	tunPeer    = "10.99.0.1"
	tunMTU     = 1500
)

func main() {
	common.MaybePrintVersion()
	common.LogBuild(log.Printf)
	platform := flag.String("platform", "", "wbstream | telemost | vk | dion (required)")
	link := flag.String("link", "", "WB Stream room link, Telemost join URI, VK call link, or DION event link (required)")
	displayName := flag.String("name", "Joiner", "display name in the room")
	socksHost := flag.String("socks-host", common.SocksLocalhostIP, "SOCKS5 listen address (use 0.0.0.0 to expose on LAN; tun2socks always connects via loopback)")
	socksPort := flag.Int("socks-port", 1080, "local SOCKS5 port")
	socksUser := flag.String("socks-user", "", "optional SOCKS5 username")
	socksPass := flag.String("socks-pass", "", "optional SOCKS5 password")
	resources := flag.String("resources", "default", "moderate | default | unlimited")
	tunnelMode := flag.String("tunnel-mode", "video", "tunnel mode for WB Stream: video | dc")
	vp8FPS := flag.Int("vp8-fps", 24, "VP8 frame rate")
	vp8Batch := flag.Int("vp8-batch", 30, "VP8 batch multiplier")
	dns := flag.String("dns", "1.1.1.1,8.8.8.8", "comma-separated DNS servers for the tunnel adapter")
	noTun := flag.Bool("no-tun", false, "expose SOCKS5 only, do not bring up the wintun adapter")
	dualTrack := flag.Bool("dual-track", false, "VK/WB Stream: dual-track tunnel (second screenshare channel) for higher throughput")
	videoReliability := flag.String("video-reliability", "auto", "VK Video reliability: auto or raw")
	kcpProfile := flag.String("kcp-profile", tunnel.KCPProfileBalanced, "KCP profile: fast, balanced, or stable")
	cleanupRoutes := flag.Bool("cleanup-routes", false, "remove stale Windows split-default routes and exit")
	remoteSocks := flag.String("remote-socks", "", "phone/LAN SOCKS5 endpoint as IPv4:port (starts system-wide TUN without joining a call)")
	remoteSocksUser := flag.String("remote-socks-user", "", "username for phone/LAN SOCKS5")
	remoteSocksPass := flag.String("remote-socks-pass", "", "password for phone/LAN SOCKS5")
	flag.Parse()
	if *cleanupRoutes {
		if err := desktoptun.CleanupStaleRoutes(tunAdapter); err != nil {
			log.Fatalf("[desktoptun] cleanup stale routes: %v", err)
		}
		log.Printf("[desktoptun] stale routes cleaned")
		return
	}

	if *videoReliability != "auto" && *videoReliability != "raw" {
		log.Fatal("--video-reliability must be auto or raw")
	}
	if *kcpProfile == tunnel.KCPProfileFast && !*noTun {
		log.Printf("[transport] fast profile is unsafe with system-wide TUN; using balanced (use SOCKS-only for controlled Fast tests)")
		*kcpProfile = tunnel.KCPProfileBalanced
	}

	switch *resources {
	case "moderate":
		debug.SetMemoryLimit(64 << 20)
	case "default":
		debug.SetMemoryLimit(128 << 20)
	case "unlimited":
		debug.SetMemoryLimit(256 << 20)
	default:
		log.Fatalf("[config] unknown resources mode: %s", *resources)
	}

	if *remoteSocks != "" {
		if *noTun {
			log.Fatal("--remote-socks is a system-wide tunnel mode and cannot be combined with --no-tun")
		}
		host, port, err := parseRemoteSocksEndpoint(*remoteSocks)
		if err != nil {
			log.Fatalf("[phone-socks] %v", err)
		}
		if *remoteSocksUser == "" || *remoteSocksPass == "" {
			log.Fatal("[phone-socks] --remote-socks-user and --remote-socks-pass are required")
		}
		if err := runRemoteSocksMode(host, port, *remoteSocksUser, *remoteSocksPass, splitCSV(*dns)); err != nil {
			log.Printf("[phone-socks] startup aborted before routing traffic: %v", err)
			os.Exit(3)
		}
		return
	}

	if *platform == "" || *link == "" {
		log.Fatal("--platform and --link are required unless --remote-socks is used")
	}

	// One desktoptun.Tunnel covers both platforms. Created up-front so
	// signaling-host bypass routes can be installed before any platform
	// code touches the network.
	var tun *desktoptun.Tunnel
	if !*noTun {
		if err := desktoptun.CleanupStaleRoutes(tunAdapter); err != nil {
			log.Printf("[desktoptun] preflight stale-route cleanup: %v", err)
		}
		cfg := desktoptun.Config{
			AdapterName: tunAdapter,
			TunnelIP:    tunIP,
			TunnelMask:  tunMask,
			TunnelPeer:  tunPeer,
			MTU:         tunMTU,
			DNSServers:  splitCSV(*dns),
			SocksHost:   common.SocksLocalhostIP,
			SocksPort:   *socksPort,
			SocksUser:   *socksUser,
			SocksPass:   *socksPass,
			LogFn:       log.Printf,
		}
		var err error
		tun, err = desktoptun.New(cfg)
		if err != nil {
			log.Fatalf("[desktoptun] init: %v", err)
		}
		defer tun.Stop()
	}

	// Add bypass routes for the signaling hosts before any traffic
	// from the joiner reaches them. These are needed even before
	// engine.Start, because the joiner opens its WebSocket as soon
	// as we call Start() below.
	bypassHosts := signalingHosts(*platform, *link)
	preResolved := map[string][]net.IP{}
	for _, h := range bypassHosts {
		ips, err := net.LookupIP(h)
		if err != nil {
			log.Printf("[bypass] resolve %s: %v (will rely on candidate hook)", h, err)
			continue
		}
		preResolved[h] = ips
		log.Printf("[bypass] %s -> %v (pre-tun)", h, ips)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	watchStdinQuit(sig)

	tunReady := make(chan struct{})
	var tunOnce sync.Once
	var (
		pendingMu  sync.Mutex
		pending    []string
		tunStarted bool
	)
	bringUpTun := func() {
		tunOnce.Do(func() {
			if tun == nil {
				fmt.Printf("\n  PROXY ACTIVE on socks5://%s:%d\n  system routes are unchanged; configure individual apps to use this endpoint\n\n",
					*socksHost, *socksPort)
				close(tunReady)
				return
			}
			if err := tun.Start(); err != nil {
				log.Fatalf("[desktoptun] start: %v", err)
			}
			for host, ips := range preResolved {
				for _, ip := range ips {
					if err := tun.AddBypassIP(ip); err != nil {
						log.Printf("[bypass] %s ip %s: %v", host, ip, err)
					}
				}
			}
			pendingMu.Lock()
			drained := pending
			pending = nil
			tunStarted = true
			pendingMu.Unlock()
			for _, c := range drained {
				if err := tun.AddBypassFromCandidate(c); err != nil {
					log.Printf("[bypass] replay: %v", err)
				}
			}
			fmt.Printf("\n  TUNNEL ACTIVE on adapter %q (DNS=%s)\n  all traffic now egresses via %s\n\n",
				tunAdapter, *dns, *platform)
			close(tunReady)
		})
	}

	tryBypass := func(c string) {
		if err := tun.AddBypassFromCandidate(c); err != nil {
			pendingMu.Lock()
			if !tunStarted {
				pending = append(pending, c)
				pendingMu.Unlock()
				return
			}
			pendingMu.Unlock()
			log.Printf("[bypass] candidate: %v", err)
		}
	}

	addCandidate := func(target int, candidateOrSDP string) {
		if tun == nil {
			return
		}
		tryBypass(candidateOrSDP)
		if strings.Contains(candidateOrSDP, "a=candidate:") {
			for _, line := range strings.Split(candidateOrSDP, "\n") {
				line = strings.TrimRight(line, "\r")
				if strings.HasPrefix(line, "a=candidate:") {
					tryBypass(line)
				}
			}
		}
	}

	var (
		bridge                  *tunnel.RelayBridge
		bridgeMu                sync.Mutex
		requestCarrierReconnect func(string)
	)
	onConnected := func(t tunnel.DataTunnel) {
		readBuf := common.VP8BufSize
		trackCount := 1
		var adaptive *tunnel.AdaptiveKCPTunnel
		if _, ok := t.(*tunnel.DCTunnel); ok {
			readBuf = common.DCBufSize
		} else if strings.EqualFold(*platform, "vk") && *videoReliability == "auto" {
			if multi, ok := t.(*tunnel.MultiTrackTunnel); ok {
				trackCount = multi.SubTunnelCount()
			}
			adaptive = tunnel.NewAdaptiveKCPTunnel(t, log.Printf)
			adaptive.SetKCPProfile(*kcpProfile)
			adaptive.SetOnStall(func() {
				if requestCarrierReconnect != nil {
					requestCarrierReconnect("KCP acknowledgements stalled")
				}
			})
			t = adaptive
			readBuf = tunnel.AdaptiveKCPRelayReadBuf
		}
		bridgeMu.Lock()
		defer bridgeMu.Unlock()
		configureAdaptive := func() {
			if adaptive == nil {
				return
			}
			bridge.SetOnHandshake(func(result tunnel.HandshakeResult) {
				if result.Supports(tunnel.CapabilityVideoKCP1) {
					if result.Supports(tunnel.CapabilityPriorityControl) {
						adaptive.EnablePriorityControl()
					} else {
						effective := adaptive.SetKCPProfile(tunnel.PreferSaferKCPProfile(*kcpProfile, tunnel.KCPProfileBalanced))
						log.Printf("[transport] peer lacks profile/control capability; compatibility profile=%s", effective)
					}
					adaptive.EnableKCP()
					if result.Supports(tunnel.CapabilityPriorityControl) {
						bridge.SendKCPProfile(*kcpProfile)
					}
				} else {
					adaptive.EnableRawCompatibility()
				}
			})
			bridge.SetOnPeerKCPProfile(func(profile string) {
				effective := adaptive.SetKCPProfile(tunnel.PreferSaferKCPProfile(*kcpProfile, profile))
				log.Printf("[transport] negotiated bidirectional KCP profile=%s", effective)
			})
			bridge.ConfigureHandshake(
				tunnel.CapabilityMetricsV1|tunnel.CapabilityVideoKCP1|tunnel.CapabilityPriorityControl|tunnel.CapabilityReliableDNS,
				common.VP8BufSize,
				tunnel.ReliabilityRawVP8,
				trackCount,
			)
		}
		// Reconnect: swap the new tunnel behind the persistent SOCKS
		// listener instead of binding a second one
		if bridge != nil {
			bridge.SwapTunnel(t)
			configureAdaptive()
			log.Printf("[socks] tunnel swapped after reconnect")
			return
		}
		bridge = tunnel.NewRelayBridgeWithAuth(t, "joiner", readBuf, log.Printf, *socksUser, *socksPass)
		configureAdaptive()
		bridge.SetPersistentListener(true)
		bridge.MarkReady()
		addr := fmt.Sprintf("%s:%d", *socksHost, *socksPort)
		go func() {
			if err := bridge.ListenSOCKS(addr); err != nil {
				log.Printf("[socks] listen: %v", err)
			}
		}()
		log.Printf("[socks] listening on %s", addr)
		// SOCKS5 is up; bring up wintun so the OS starts steering
		// traffic into it. Doing this after the joiner has connected
		// also means we already have remote candidates and bypass
		// routes are in place.
		bringUpTun()
	}

	switch strings.ToLower(*platform) {
	case "wbstream", "wb":
		runWBStream(*link, *displayName, *tunnelMode, *vp8FPS, *vp8Batch, *dualTrack,
			onConnected, addCandidate)
	case "telemost", "tm":
		runTelemost(*link, *displayName, *vp8FPS, *vp8Batch,
			onConnected, addCandidate)
	case "vk":
		selfHealReconnect = true
		runVK(*link, *displayName, *tunnelMode, *vp8FPS, *vp8Batch, *dualTrack,
			onConnected, addCandidate, func(fn func(string)) { requestCarrierReconnect = fn })
	case "dion", "dn":
		runDion(*link, *displayName, onConnected, addCandidate)
	default:
		log.Fatalf("[config] unknown --platform %q", *platform)
	}

	var lost bool
	select {
	case <-sig:
		log.Printf("[main] shutting down")
	case <-tunnelLostCh:
		log.Printf("[main] tunnel lost, exiting with code 2 to trigger auto-reconnect")
		lost = true
	}
	if tun != nil {
		tun.Stop()
	}
	// Give in-flight goroutines a beat to drain before the process exits.
	time.Sleep(200 * time.Millisecond)
	if lost {
		os.Exit(2)
	}
}

func parseRemoteSocksEndpoint(endpoint string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(endpoint))
	if err != nil {
		return "", 0, fmt.Errorf("--remote-socks must be IPv4:port, for example 192.168.43.1:1080: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return "", 0, fmt.Errorf("--remote-socks host must be a non-loopback IPv4 literal")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("--remote-socks port must be between 1 and 65535")
	}
	return ip.To4().String(), port, nil
}

func runRemoteSocksMode(host string, port int, user, pass string, dns []string) error {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))
	if err := probeAuthenticatedSocks(endpoint, user, pass, 5*time.Second); err != nil {
		return fmt.Errorf("authentication preflight failed for %s: %w", endpoint, err)
	}

	if err := desktoptun.CleanupStaleRoutes(tunAdapter); err != nil {
		log.Printf("[desktoptun] preflight stale-route cleanup: %v", err)
	}
	tun, err := desktoptun.New(desktoptun.Config{
		AdapterName: tunAdapter,
		TunnelIP:    tunIP,
		TunnelMask:  tunMask,
		TunnelPeer:  tunPeer,
		MTU:         tunMTU,
		DNSServers:  dns,
		SocksHost:   host,
		SocksPort:   port,
		SocksUser:   user,
		SocksPass:   pass,
		LogFn:       log.Printf,
	})
	if err != nil {
		return fmt.Errorf("initialize TUN: %w", err)
	}
	if err := tun.Start(); err != nil {
		return fmt.Errorf("start TUN: %w", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	watchStdinQuit(sig)
	fmt.Printf("\n  TUNNEL ACTIVE via phone SOCKS %s\n  adapter=%q DNS=%s\n\n",
		endpoint, tunAdapter, strings.Join(dns, ","))
	phoneLost := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		failures := 0
		for range ticker.C {
			if err := probeAuthenticatedSocks(endpoint, user, pass, 3*time.Second); err != nil {
				failures++
				log.Printf("[phone-socks] health check failed %d/3: %v", failures, err)
				if failures >= 3 {
					select {
					case phoneLost <- struct{}{}:
					default:
					}
					return
				}
				continue
			}
			failures = 0
		}
	}()
	select {
	case <-sig:
		log.Printf("[phone-socks] shutting down")
	case <-phoneLost:
		log.Printf("[phone-socks] phone gateway lost; restoring normal PC routes")
	}
	tun.Stop()
	return nil
}

func probeAuthenticatedSocks(endpoint, user, pass string, timeout time.Duration) error {
	if len(user) == 0 || len(user) > 255 || len(pass) == 0 || len(pass) > 255 {
		return fmt.Errorf("SOCKS credentials must contain 1..255 bytes")
	}
	conn, err := net.DialTimeout("tcp", endpoint, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return fmt.Errorf("send method negotiation: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("read method negotiation: %w", err)
	}
	if reply[0] != 5 || reply[1] != 2 {
		return fmt.Errorf("server did not require username/password authentication")
	}
	auth := make([]byte, 0, 3+len(user)+len(pass))
	auth = append(auth, 1, byte(len(user)))
	auth = append(auth, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := conn.Write(auth); err != nil {
		return fmt.Errorf("send authentication: %w", err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("read authentication: %w", err)
	}
	if reply[0] != 1 || reply[1] != 0 {
		return fmt.Errorf("username or password rejected")
	}
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func signalingHosts(platform, link string) []string {
	switch strings.ToLower(platform) {
	case "wbstream", "wb":
		return []string{"stream.wb.ru", "rtc-el-01.wb.ru"}
	case "telemost", "tm":
		hosts := []string{"telemost.yandex.ru", "telemost-api.yandex.ru"}
		if u, err := url.Parse(strings.TrimSpace(link)); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
		return hosts
	case "vk":
		hosts := []string{"vk.com", "login.vk.com", "api.vk.com", "ok.ru", "cloud-api.yandex.ru"}
		if u, err := url.Parse(strings.TrimSpace(link)); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
		return hosts
	case "dion", "dn":
		return []string{"dion.vc", "api.dion.vc", "api-clients.dion.vc"}
	}
	return nil
}

func runWBStream(link, name, mode string, fps, batch int, dualTrack bool,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	id := wbstream.ParseRoomID(link)
	roomID, roomToken, _, serverURL, err := wbstream.AuthAndGetToken(nil, id, name)
	if err != nil {
		log.Fatalf("[wb] auth: %v", err)
	}
	log.Printf("[wb] room=%s server=%s mode=%s", roomID, serverURL, mode)

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(roomID))
	if err != nil {
		log.Fatalf("[wb] obfuscator: %v", err)
	}

	sess := wbstream.NewSession(wbstream.SessionConfig{
		RoomToken:   roomToken,
		ServerURL:   serverURL,
		DisplayName: name,
		TunnelMode:  mode,
		Obfuscator:  obf,
		LogFn:       log.Printf,
		VP8FPS:      fps,
		VP8Batch:    batch,
		ScreenShare: dualTrack,
	})
	sess.OnConnected = onConnected
	sess.OnRemoteCandidate = onCandidate

	if err := sess.Start(); err != nil {
		log.Fatalf("[wb] session: %v", err)
	}
}

func runTelemost(link, name string, fps, batch int,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	inner := joinerCommon.NewTelemostHeadlessJoiner(
		log.Printf,
		resolveHostname,
		statusEmitter{},
		nil,
		pion.AddTunnelTracks,
		pion.ReadTrack,
	)
	inner.OnConnected = onConnected
	inner.OnRemoteCandidate = onCandidate

	params, _ := json.Marshal(struct {
		JoinLink    string `json:"joinLink"`
		DisplayName string `json:"displayName"`
		VP8FPS      int    `json:"vp8Fps"`
		VP8Batch    int    `json:"vp8Batch"`
	}{
		JoinLink:    strings.TrimSpace(link),
		DisplayName: name,
		VP8FPS:      fps,
		VP8Batch:    batch,
	})
	go inner.RunWithParams(string(params))
}

func runVK(link, name, mode string, fps, batch int, dualTrack bool,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
	onReconnectReady func(func(string)),
) {
	emitter := statusEmitter{}
	statusFn := func(s string) { emitter.EmitStatus(s) }

	authJSON, err := joinerCommon.RunVKAuth(strings.TrimSpace(link), name,
		log.Printf, statusFn, newFileCacheStore(), resolveHostname)
	if err != nil {
		log.Fatalf("[vk] auth: %v", err)
	}

	var authParams map[string]interface{}
	if json.Unmarshal([]byte(authJSON), &authParams) != nil {
		log.Fatalf("[vk] auth response not JSON: %s", authJSON)
	}
	authParams["tunnelMode"] = mode
	authParams["vp8Fps"] = fps
	authParams["vp8Batch"] = batch
	authParams["dualTrack"] = dualTrack
	patched, err := json.Marshal(authParams)
	if err != nil {
		log.Fatalf("[vk] auth marshal: %v", err)
	}

	inner := joinerCommon.NewVKHeadlessJoiner(
		log.Printf,
		resolveHostname,
		emitter,
		nil,
		pion.AddTunnelTracks,
		pion.ReadTrack,
	)
	inner.OnConnected = onConnected
	inner.OnRemoteCandidate = onCandidate
	if onReconnectReady != nil {
		onReconnectReady(inner.RequestReconnect)
	}
	go inner.RunWithParams(string(patched))
}

func runDion(link, name string,
	onConnected func(tunnel.DataTunnel),
	onCandidate func(int, string),
) {
	room := dion.ParseRoom(link)
	if room == "" {
		log.Fatalf("[dion] --link must be a room id or https://dion.vc/event/<id>")
	}
	auth, event, err := dion.JoinAsGuest(nil, room, name)
	if err != nil {
		log.Fatalf("[dion] JoinAsGuest: %v", err)
	}
	log.Printf("[dion] room=%s event_id=%s", event.Slug, event.ID)

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(event.Slug))
	if err != nil {
		log.Fatalf("[dion] obfuscator: %v", err)
	}

	call := dion.NewCall(dion.CallConfig{
		Auth:        auth,
		Event:       event,
		Obfuscator:  obf,
		DisplayName: name,
		LogFn:       log.Printf,
		Role:        dion.RoleJoiner,
	})
	call.OnConnected = onConnected
	call.OnRemoteSDP = func(sdp string) { onCandidate(0, sdp) }

	if err := call.Start(); err != nil {
		log.Fatalf("[dion] call.Start: %v", err)
	}
	go func() {
		<-call.Done()
		select {
		case tunnelLostCh <- struct{}{}:
		default:
		}
	}()
}

func resolveHostname(hostname string) (string, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		return hostname, nil
	}
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	for _, ip := range ips {
		return ip.String(), nil
	}
	return "", fmt.Errorf("no IPs for %s", hostname)
}
