package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestBuildRecoveryMessageIsSigned(t *testing.T) {
	notice := recoveryNotice{Profile: "client-1", Name: "Phone", Key: "test-recovery-key", Generation: 7}
	message, err := buildRecoveryMessage("https://vk.com/call/join/example", notice, time.Unix(1234, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "WhitelistBypass · Phone\nhttps://vk.com/call/join/example") {
		t.Fatalf("human-readable link missing: %q", message)
	}
	token := message[strings.LastIndex(message, "WLB2."):]
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "WLB2" {
		t.Fatalf("bad envelope: %q", message)
	}
	mac := hmac.New(sha256.New, []byte(notice.Key))
	_, _ = mac.Write([]byte(strings.Join([]string{parts[1], parts[2], parts[3], "https://vk.com/call/join/example"}, "\n")))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[4])) {
		t.Fatal("signature mismatch")
	}
}
