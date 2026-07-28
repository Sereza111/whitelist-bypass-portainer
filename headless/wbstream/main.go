package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"sync"

	"whitelist-bypass/relay/common"
	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"
)

func main() {
	common.MaybePrintVersion()
	common.LogBuild(log.Printf)
	roomFlag := flag.String("room", "", "WB Stream invitation created by the paired Android device (required)")
	displayName := flag.String("name", "Headless", "display name in the room")
	resources := flag.String("resources", "default", "resource mode: default, moderate, unlimited, custom")
	customReadBuf := flag.Int("read-buf", 0, "DC read buffer size in bytes, used with -resources custom")
	customMemLimit := flag.Int64("mem-limit", 0, "memory limit in bytes, used with -resources custom")
	writeFile := flag.String("write-file", "", "path to file where active room id is appended")
	upstreamSocks := flag.String("upstream-socks", "", "route tunneled egress through this SOCKS5 proxy (host:port), e.g. a local VPN client")
	upstreamUser := flag.String("upstream-user", "", "upstream SOCKS5 username")
	upstreamPass := flag.String("upstream-pass", "", "upstream SOCKS5 password")
	flag.Parse()

	var readBuf int
	var memLimit int64
	switch *resources {
	case "moderate":
		readBuf = 16384
		memLimit = 64 << 20
	case "default":
		readBuf = common.DCBufSize
		memLimit = 128 << 20
	case "unlimited":
		readBuf = common.RTPBufSize
		memLimit = 256 << 20
	case "custom":
		readBuf = *customReadBuf
		if readBuf == 0 {
			readBuf = common.RTPBufSize
		}
		memLimit = *customMemLimit
		if memLimit == 0 {
			memLimit = 256 << 20
		}
	default:
		log.Fatalf("[config] unknown resources mode: %s (use moderate, default, unlimited, custom)", *resources)
	}
	if memLimit > 0 {
		debug.SetMemoryLimit(memLimit)
	}
	log.Printf("[config] resources=%s read-buf=%d mem-limit=%d", *resources, readBuf, memLimit)

	requestedRoom := wbstream.ParseRoomID(*roomFlag)
	if requestedRoom == "" {
		log.Fatal("[config] --room invitation is required; Manager obtains it from the paired Android creator")
	}
	roomID, roomToken, accessToken, serverURL, err := wbstream.AuthAndGetToken(nil, requestedRoom, *displayName)
	if err != nil {
		log.Fatalf("[auth] %v", err)
	}
	log.Printf("[auth] room=%s server=%s", roomID, serverURL)

	if *writeFile != "" {
		f, err := os.OpenFile(*writeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("Failed to open write-file: %v", err)
		}
		fmt.Fprintln(f, "wbstream://"+roomID)
		f.Close()
		log.Printf("[config] Wrote join link to %s", *writeFile)
	}

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(roomID))
	if err != nil {
		log.Fatalf("[obf] init failed: %v", err)
	}
	log.Printf("[obf] ready localEpoch=0x%08x", obf.LocalEpoch())

	var activeBridge *tunnel.RelayBridge
	var bridgeMu sync.Mutex
	makeSession := func(token, access, server string) *wbstream.Session {
		sess := wbstream.NewSession(wbstream.SessionConfig{
			RoomToken:   token,
			ServerURL:   server,
			DisplayName: *displayName,
			Obfuscator:  obf,
			LogFn:       log.Printf,
			RoomID:      roomID,
			AccessToken: access,
			ReadBuf:     readBuf,
		})
		sess.OnConnected = func(tun tunnel.DataTunnel) {
			bridgeMu.Lock()
			defer bridgeMu.Unlock()
			if activeBridge != nil {
				activeBridge.Close()
			}
			bridgeReadBuf := common.VP8BufSize
			mode := "video"
			switch tun.(type) {
			case *tunnel.DCTunnel:
				bridgeReadBuf = readBuf
				mode = "dc"
			case *tunnel.KCPTunnel:
				mode = "video+kcp"
			}
			activeBridge = tunnel.NewRelayBridge(tun, "creator", bridgeReadBuf, log.Printf)
			activeBridge.SetUpstreamSocks(*upstreamSocks, *upstreamUser, *upstreamPass)
			activeBridge.SetOnPeerConfig(func(fps, batch, trackCount int) {
				sess.AdaptTrackCount(trackCount)
			})
			fmt.Printf("\n  TUNNEL CONNECTED mode=%s\n", mode)
		}
		sess.OnPeerRestart = func() {
			bridgeMu.Lock()
			defer bridgeMu.Unlock()
			if activeBridge != nil {
				log.Printf("[creator] new peer detected, resetting relay bridge")
				activeBridge.Reset()
			}
		}
		return sess
	}

	fmt.Println("")
	fmt.Println("  CALL CREATED")
	fmt.Println("  join_link: wbstream://" + roomID)
	fmt.Println("")

	sess := makeSession(roomToken, accessToken, serverURL)
	if err := sess.Start(); err != nil {
		sess.Close()
		log.Fatalf("[session] start failed: %v", err)
	}
	<-sess.Done()
	log.Printf("[session] call ended; Manager must request a fresh Android invitation")
	sess.Close()
	bridgeMu.Lock()
	if activeBridge != nil {
		activeBridge.Close()
	}
	bridgeMu.Unlock()
	os.Exit(1)
}
