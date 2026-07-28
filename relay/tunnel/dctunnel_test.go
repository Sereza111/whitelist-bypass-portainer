package tunnel

import "testing"

func TestDCTunnelInternalCloseSurvivesPublicCallbackReplacement(t *testing.T) {
	tun := &DCTunnel{}
	internalCalls := 0
	firstPublicCalls := 0
	secondPublicCalls := 0
	var order []string
	tun.SetOnInternalClose(func() {
		internalCalls++
		order = append(order, "internal")
	})
	tun.SetOnClose(func() { firstPublicCalls++ })
	tun.SetOnClose(func() {
		secondPublicCalls++
		order = append(order, "public")
	})

	tun.notifyClose()
	tun.notifyClose()

	if internalCalls != 1 {
		t.Fatalf("internal close calls=%d, want 1", internalCalls)
	}
	if firstPublicCalls != 0 || secondPublicCalls != 1 {
		t.Fatalf("public close calls first=%d second=%d, want 0/1", firstPublicCalls, secondPublicCalls)
	}
	if len(order) != 2 || order[0] != "public" || order[1] != "internal" {
		t.Fatalf("close order=%v, want [public internal]", order)
	}
}
