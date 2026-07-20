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
	parts := strings.Split(message, ".")
	if len(parts) != 3 || parts[0] != "WLB1" {
		t.Fatalf("bad envelope: %q", message)
	}
	mac := hmac.New(sha256.New, []byte(notice.Key))
	_, _ = mac.Write([]byte(parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		t.Fatal("signature mismatch")
	}
}
