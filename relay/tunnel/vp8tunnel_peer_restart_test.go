package tunnel

import "testing"

func TestVP8DataTunnelSignalsPeerEpochChangeOnce(t *testing.T) {
	secret := []byte("peer-generation-test")
	local, err := NewTunnelObfuscator(secret)
	if err != nil {
		t.Fatal(err)
	}
	peerA, err := NewTunnelObfuscator(secret)
	if err != nil {
		t.Fatal(err)
	}
	peerB, err := NewTunnelObfuscator(secret)
	if err != nil {
		t.Fatal(err)
	}

	tun := NewVP8DataTunnel(nil, local, func(string, ...any) {})
	restarts := 0
	tun.SetOnPeerRestart(func() { restarts++ })

	tun.HandleFrame(peerA.EncodeKeepalive())
	if restarts != 0 {
		t.Fatalf("initial peer reported as restart: %d", restarts)
	}
	tun.HandleFrame(peerB.EncodeKeepalive())
	tun.HandleFrame(peerB.EncodeKeepalive())
	if restarts != 1 {
		t.Fatalf("replacement peer restart callbacks=%d, want 1", restarts)
	}
}
