package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeRequest(t *testing.T) {
	m := newManager()
	got, err := m.normalizeRequest(sessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "vk" || got.Resources != "default" || got.VideoReliability != "auto" || got.DisplayName != "Headless" {
		t.Fatalf("unexpected defaults: %#v", got)
	}
	if _, err := m.normalizeRequest(sessionRequest{Mode: "unknown"}); err == nil {
		t.Fatal("unsupported mode accepted")
	}
}

func TestLogRingRedactsJoinLink(t *testing.T) {
	ring := newLogRing(10)
	_, _ = ring.Write([]byte("join_link: https://example.test/secret\nnormal event\n"))
	body := strings.Join(ring.snapshot(), "\n")
	if strings.Contains(body, "secret") {
		t.Fatalf("join link leaked into logs: %s", body)
	}
	if !strings.Contains(body, "normal event") {
		t.Fatalf("normal log missing: %s", body)
	}
}

func TestRequireAuth(t *testing.T) {
	handler := requireAuth("admin", "long-test-password", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetBasicAuth("admin", "long-test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authenticated status=%d", response.Code)
	}
}
