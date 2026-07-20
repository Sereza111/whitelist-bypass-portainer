package desktoptun

import (
	"strings"
	"testing"
)

func TestSocksProxyURLRedactsAndEscapesCredentials(t *testing.T) {
	raw, safe := socksProxyURL("192.168.1.25", 1080, "phone user", "p@ss:word")
	if !strings.Contains(raw, "phone%20user:p%40ss%3Aword@192.168.1.25:1080") {
		t.Fatalf("credentials were not URL encoded: %q", raw)
	}
	if strings.Contains(safe, "p@ss:word") || !strings.Contains(safe, "[REDACTED]") {
		t.Fatalf("safe proxy URL leaked password: %q", safe)
	}
}

func TestSocksBypassIPv4(t *testing.T) {
	if got := socksBypassIPv4("192.168.43.1"); got == nil || got.String() != "192.168.43.1" {
		t.Fatalf("unexpected IPv4 result: %v", got)
	}
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "phone.local", "::1"} {
		if got := socksBypassIPv4(host); got != nil {
			t.Fatalf("expected no bypass for %q, got %v", host, got)
		}
	}
}
