package main

import (
	"net"
	"testing"
	"time"

	"whitelist-bypass/relay/common"
)

func TestParseRemoteSocksEndpoint(t *testing.T) {
	host, port, err := parseRemoteSocksEndpoint("192.168.43.1:1080")
	if err != nil || host != "192.168.43.1" || port != 1080 {
		t.Fatalf("unexpected parse result host=%q port=%d err=%v", host, port, err)
	}
	for _, endpoint := range []string{
		"phone.local:1080",
		"127.0.0.1:1080",
		"192.168.43.1",
		"192.168.43.1:0",
		"192.168.43.1:65536",
	} {
		if _, _, err := parseRemoteSocksEndpoint(endpoint); err == nil {
			t.Fatalf("expected %q to be rejected", endpoint)
		}
	}
}

func TestProbeAuthenticatedSocks(t *testing.T) {
	endpoint := serveOneAuthHandshake(t, "phone-user", "phone-pass")
	if err := probeAuthenticatedSocks(endpoint, "phone-user", "phone-pass", time.Second); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}

	endpoint = serveOneAuthHandshake(t, "phone-user", "phone-pass")
	if err := probeAuthenticatedSocks(endpoint, "phone-user", "wrong", time.Second); err == nil {
		t.Fatal("invalid credentials accepted")
	}
}

func serveOneAuthHandshake(t *testing.T, user, pass string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		defer listener.Close()
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		common.NegotiateAuth(conn, user, pass)
	}()
	return listener.Addr().String()
}
