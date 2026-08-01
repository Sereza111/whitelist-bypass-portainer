package wbstream

import (
	"testing"

	"whitelist-bypass/relay/livekit"
	"whitelist-bypass/relay/tunnel"
)

func TestParticipantGenerationChangesAfterCleanDisconnect(t *testing.T) {
	sess := NewSession(SessionConfig{LogFn: func(string, ...any) {}})
	sess.lk = livekit.NewClient(livekit.Config{})
	restarts := 0
	sess.OnPeerRestart = func() { restarts++ }

	sess.onParticipantUpdate([]livekit.ParticipantInfo{{
		SID: "peer-a", Identity: "peer-a", State: livekit.ParticipantStateActive,
	}})
	sess.onParticipantUpdate([]livekit.ParticipantInfo{{
		SID: "peer-a", Identity: "peer-a", State: livekit.ParticipantStateDisconnected,
	}})
	if restarts != 0 {
		t.Fatalf("disconnect unexpectedly reset generation: %d", restarts)
	}
	sess.onParticipantUpdate([]livekit.ParticipantInfo{{
		SID: "peer-b", Identity: "peer-b", State: livekit.ParticipantStateActive,
	}})
	if restarts != 1 {
		t.Fatalf("clean replacement restart callbacks=%d, want 1", restarts)
	}
}

func TestSessionRebuildsShardedKCPOnCarrierPeerEpochChange(t *testing.T) {
	secret := []byte("wb-peer-generation-test")
	local, err := tunnel.NewTunnelObfuscator(secret)
	if err != nil {
		t.Fatal(err)
	}
	peerA, err := tunnel.NewTunnelObfuscator(secret)
	if err != nil {
		t.Fatal(err)
	}
	peerB, err := tunnel.NewTunnelObfuscator(secret)
	if err != nil {
		t.Fatal(err)
	}

	subA := tunnel.NewVP8DataTunnel(nil, local, func(string, ...any) {})
	subB := tunnel.NewVP8DataTunnel(nil, local, func(string, ...any) {})
	carrier := tunnel.NewMultiTrackTunnel([]*tunnel.VP8DataTunnel{subA, subB})
	sess := NewSession(SessionConfig{LogFn: func(string, ...any) {}})
	sess.vp8tun = carrier
	carrier.SetOnPeerRestart(sess.handleCarrierPeerRestart)
	oldReliable := sess.maybeWrapReliable(carrier)
	sess.activeTunnel = oldReliable
	sess.tunFired = true
	defer func() {
		sess.mu.Lock()
		current := sess.kcptun
		sess.mu.Unlock()
		if current != nil {
			stopDataTunnel(current)
		}
	}()

	var replacement tunnel.DataTunnel
	callbacks := 0
	sess.OnConnected = func(tun tunnel.DataTunnel) {
		callbacks++
		replacement = tun
	}

	subA.HandleFrame(peerA.EncodeKeepalive())
	subA.HandleFrame(peerB.EncodeKeepalive())
	subA.HandleFrame(peerB.EncodeKeepalive())

	if callbacks != 1 {
		t.Fatalf("transport replacement callbacks=%d, want 1", callbacks)
	}
	if replacement == nil || replacement == oldReliable {
		t.Fatal("KCP transport was not rebuilt for the replacement peer")
	}
	if _, ok := replacement.(*tunnel.ShardedKCPTunnel); !ok {
		t.Fatalf("replacement transport type=%T, want *tunnel.ShardedKCPTunnel", replacement)
	}
}
