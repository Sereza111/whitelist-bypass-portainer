package joiner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestExtractSuccessTokenSupportsCurrentAndNestedPayloads(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"legacy response", `{"response":{"success_token":"legacy-token"}}`},
		{"nested result", `{"result":{"challenge":{"successToken":"nested-token"}}}`},
		{"array response", `{"data":[{"payload":{"success-token":"array-token"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := extractSuccessToken([]byte(test.body)); got == "" {
				t.Fatal("success token was not detected")
			}
		})
	}
	if got := extractSuccessToken([]byte(`{"response":{"status":"ok"}}`)); got != "" {
		t.Fatalf("response without a token produced %q", got)
	}
}

func TestExtractSuccessTokenFromRedirect(t *testing.T) {
	for _, rawURL := range []string{
		"https://id.vk.com/done?success_token=query-token",
		"https://id.vk.com/done#successToken=fragment-token",
		"https://id.vk.com/done#/challenge/complete?success_token=route-token",
	} {
		if got := extractSuccessTokenFromURL(rawURL); got == "" {
			t.Fatalf("success token was not detected in %q", rawURL)
		}
	}
}

func TestCaptchaProxyCapturesJSONFromGenericProxy(t *testing.T) {
	landing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>captcha</body></html>")
	}))
	defer landing.Close()

	result := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"challenge":{"successToken":"captured-token"}}}`)
	}))
	defer result.Close()

	port := StartCaptchaProxy(landing.URL, nil)
	if port == 0 {
		t.Fatal("captcha proxy did not start")
	}
	defer StopCaptchaProxy()

	proxyURL := fmt.Sprintf(
		"http://127.0.0.1:%d/generic_proxy?proxy_url=%s",
		port,
		url.QueryEscape(result.URL),
	)
	response, err := http.Get(proxyURL)
	if err != nil {
		t.Fatalf("generic proxy request failed: %v", err)
	}
	response.Body.Close()

	tokenCh := make(chan string, 1)
	go func() { tokenCh <- GetCaptchaResult() }()
	select {
	case got := <-tokenCh:
		if got != "captured-token" {
			t.Fatalf("captured token=%q", got)
		}
	case <-time.After(2 * time.Second):
		StopCaptchaProxy()
		t.Fatal("timed out waiting for captured token")
	}
}
