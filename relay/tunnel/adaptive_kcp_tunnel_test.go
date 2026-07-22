package tunnel

import (
	"bytes"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdaptiveKCPTunnelMixedTransition(t *testing.T) {
	leftRaw, rightRaw := newMemoryTunnelPair()
	logFn := func(string, ...any) {}
	left := NewAdaptiveKCPTunnel(leftRaw, logFn)
	right := NewAdaptiveKCPTunnel(rightRaw, logFn)
	defer left.Stop()
	defer right.Stop()

	leftData := make(chan []byte, 4)
	rightData := make(chan []byte, 4)
	left.SetOnData(func(data []byte) { leftData <- bytes.Clone(data) })
	right.SetOnData(func(data []byte) { rightData <- bytes.Clone(data) })

	control := EncodeFrame(ControlConnID, MsgHello, []byte("control"))
	left.SendData(control)
	expectTunnelPayload(t, rightData, control)

	left.EnableKCP()
	reliable := EncodeFrame(7, MsgData, []byte("reliable payload"))
	left.SendData(reliable)
	expectTunnelPayload(t, rightData, reliable)

	right.EnableRawCompatibility()
	raw := EncodeFrame(8, MsgData, []byte("legacy payload"))
	right.SendData(raw)
	expectTunnelPayload(t, leftData, raw)
}

func TestAdaptiveKCPReadBufferFitsOneSegment(t *testing.T) {
	if AdaptiveKCPRelayReadBuf+adaptiveKCPFrameOverhead+adaptiveKCPHeaderSize != adaptiveKCPSegmentMTU {
		t.Fatalf("relay frame no longer aligns with one KCP segment")
	}
	if adaptiveKCPSegmentMTU+adaptiveKCPMarkerSize > 1126 {
		t.Fatalf("marked KCP segment exceeds VP8 carrier payload")
	}
}

func TestAdaptiveKCPProfiles(t *testing.T) {
	leftRaw, _ := newMemoryTunnelPair()
	adaptive := NewAdaptiveKCPTunnel(leftRaw, func(string, ...any) {})
	defer adaptive.Stop()

	for input, want := range map[string]string{
		"fast":     KCPProfileFast,
		"stable":   KCPProfileStable,
		"BALANCED": KCPProfileBalanced,
		"unknown":  KCPProfileBalanced,
	} {
		if got := adaptive.SetKCPProfile(input); got != want {
			t.Fatalf("profile %q normalized to %q, want %q", input, got, want)
		}
	}
}

func TestPreferSaferKCPProfile(t *testing.T) {
	for _, test := range []struct{ local, peer, want string }{
		{KCPProfileFast, KCPProfileBalanced, KCPProfileBalanced},
		{KCPProfileBalanced, KCPProfileFast, KCPProfileBalanced},
		{KCPProfileStable, KCPProfileFast, KCPProfileStable},
		{KCPProfileFast, KCPProfileStable, KCPProfileStable},
	} {
		if got := PreferSaferKCPProfile(test.local, test.peer); got != test.want {
			t.Fatalf("PreferSaferKCPProfile(%q, %q)=%q want %q", test.local, test.peer, got, test.want)
		}
	}
}

func TestKCPProfileEncoding(t *testing.T) {
	for _, want := range []string{KCPProfileStable, KCPProfileBalanced, KCPProfileFast} {
		frame := EncodeKCPProfile(want)
		decoded := false
		DecodeFrames(frame, func(connID uint32, msgType byte, payload []byte) {
			if connID != ControlConnID || msgType != MsgKCPProfile {
				t.Fatalf("unexpected profile frame conn=%d type=%d", connID, msgType)
			}
			got, ok := DecodeKCPProfile(payload)
			if !ok || got != want {
				t.Fatalf("profile decode=(%q, %t), want (%q, true)", got, ok, want)
			}
			decoded = true
		})
		if !decoded {
			t.Fatalf("profile %q frame was not decoded", want)
		}
	}
	for _, payload := range [][]byte{nil, {}, {0}, {4}, {1, 2}} {
		if got, ok := DecodeKCPProfile(payload); ok {
			t.Fatalf("malformed profile %v decoded as %q", payload, got)
		}
	}
}

func TestKCPPacketAckProgressDoesNotAdvanceOnRepeatedUNA(t *testing.T) {
	packet := make([]byte, 24)
	packet[4] = 81 // PUSH; UNA still acknowledges earlier outbound segments.
	binary.LittleEndian.PutUint32(packet[16:20], 42)
	sequence, ok := kcpPacketAckProgress(packet)
	if !ok || sequence != 42 {
		t.Fatalf("ack progress=(%d, %t), want (42, true)", sequence, ok)
	}
	var target atomic.Uint32
	if !advanceAtomicSequence(&target, sequence) {
		t.Fatal("first UNA did not advance acknowledgement progress")
	}
	if advanceAtomicSequence(&target, sequence) {
		t.Fatal("repeated UNA incorrectly counted as acknowledgement progress")
	}
	if !advanceAtomicSequence(&target, sequence+1) {
		t.Fatal("newer UNA did not advance acknowledgement progress")
	}
	if !sequenceAfter(0, ^uint32(0)) {
		t.Fatal("sequence comparison does not handle uint32 wrap")
	}
}

func TestPriorityCarrierSendsPriorityBeforeQueuedNormalData(t *testing.T) {
	inner := &blockingSendTunnel{entered: make(chan struct{}), release: make(chan struct{}), sent: make(chan byte, 3)}
	carrier := newPriorityCarrier(inner)
	defer carrier.Stop()

	carrier.SendNormal([]byte{1})
	select {
	case <-inner.entered:
	case <-time.After(time.Second):
		t.Fatal("first normal send did not reach carrier")
	}
	carrier.SendNormal([]byte{2})
	carrier.SendPriority([]byte{9})
	close(inner.release)

	got := make([]byte, 0, 3)
	for len(got) < 3 {
		select {
		case value := <-inner.sent:
			got = append(got, value)
		case <-time.After(time.Second):
			t.Fatalf("carrier output stopped at %v", got)
		}
	}
	if !bytes.Equal(got, []byte{1, 9, 2}) {
		t.Fatalf("carrier order=%v, want [1 9 2]", got)
	}
}

func TestCloseRemainsOrderedWithBulkData(t *testing.T) {
	if isPriorityMuxFrame(EncodeFrame(7, MsgClose, nil)) {
		t.Fatal("CLOSE must not overtake preceding stream data")
	}
}

func TestKCPProfileUsesReliableControlLane(t *testing.T) {
	leftRaw, rightRaw := newMemoryTunnelPair()
	left := NewAdaptiveKCPTunnel(leftRaw, func(string, ...any) {})
	right := NewAdaptiveKCPTunnel(rightRaw, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()
	left.EnablePriorityControl()
	right.EnablePriorityControl()
	left.EnableKCP()
	right.EnableKCP()

	received := make(chan string, 1)
	right.SetOnData(func(data []byte) {
		DecodeFrames(data, func(_ uint32, msgType byte, payload []byte) {
			if msgType == MsgKCPProfile {
				if profile, ok := DecodeKCPProfile(payload); ok {
					received <- profile
				}
			}
		})
	})
	left.SendData(EncodeKCPProfile(KCPProfileStable))
	select {
	case profile := <-received:
		if profile != KCPProfileStable {
			t.Fatalf("profile=%q, want stable", profile)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("profile was not delivered over control KCP")
	}
	if metrics := left.TunnelMetrics(); metrics.KCPControlSentFrames == 0 {
		t.Fatalf("profile bypassed control KCP: %#v", metrics)
	}
}

func TestAdaptiveKCPPriorityControlBypassesBulkBacklog(t *testing.T) {
	leftRaw, rightRaw := newMemoryTunnelPair()
	left := NewAdaptiveKCPTunnel(leftRaw, func(string, ...any) {})
	right := NewAdaptiveKCPTunnel(rightRaw, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()
	left.EnablePriorityControl()
	right.EnablePriorityControl()
	left.EnableKCP()
	right.EnableKCP()

	received := make(chan byte, 8)
	right.SetOnData(func(data []byte) {
		DecodeFrames(data, func(_ uint32, msgType byte, _ []byte) { received <- msgType })
	})
	left.SendData(EncodeFrame(7, MsgData, []byte("bulk")))
	left.SendData(EncodeFrame(8, MsgConnect, []byte("example.test:443")))
	left.SendData(EncodeFrame(9, MsgDNSQuery, []byte("dns-query")))
	right.SendData(EncodeFrame(9, MsgDNSReply, []byte("dns-reply")))

	seen := map[byte]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 3 {
		select {
		case msgType := <-received:
			seen[msgType] = true
		case <-deadline:
			t.Fatalf("priority lane did not deliver both frames: %#v", seen)
		}
	}
	metrics := left.TunnelMetrics()
	if metrics.KCPControlSentFrames == 0 {
		t.Fatalf("priority CONNECT/DNS did not use control KCP: %#v", metrics)
	}
	if right.TunnelMetrics().KCPControlSentFrames == 0 {
		t.Fatal("priority DNS reply did not use control KCP")
	}
}

func TestAdaptiveKCPRecoversFromThreePercentLoss(t *testing.T) {
	leftRaw, rightRaw := newLossyTunnelPair(33)
	logFn := func(string, ...any) {}
	left := NewAdaptiveKCPTunnel(leftRaw, logFn)
	right := NewAdaptiveKCPTunnel(rightRaw, logFn)
	defer left.Stop()
	defer right.Stop()
	left.EnableKCP()
	right.EnableKCP()

	const messageCount = 1000
	received := make(chan uint32, messageCount)
	right.SetOnData(func(data []byte) {
		DecodeFrames(data, func(_ uint32, msgType byte, payload []byte) {
			if msgType == MsgData && len(payload) >= 4 {
				received <- binary.BigEndian.Uint32(payload[:4])
			}
		})
	})

	for i := uint32(0); i < messageCount; i++ {
		payload := make([]byte, 100)
		binary.BigEndian.PutUint32(payload[:4], i)
		left.SendData(EncodeFrame(9, MsgData, payload))
	}

	deadline := time.After(15 * time.Second)
	for expected := uint32(0); expected < messageCount; expected++ {
		select {
		case got := <-received:
			if got != expected {
				t.Fatalf("messages reordered: got=%d want=%d", got, expected)
			}
		case <-deadline:
			t.Fatalf("timed out after %d/%d messages; dropped left=%d right=%d",
				expected, messageCount, leftRaw.dropped.Load(), rightRaw.dropped.Load())
		}
	}
	if rightRaw.dropped.Load() == 0 {
		t.Fatalf("loss injector did not drop data traffic: left=%d right=%d",
			leftRaw.dropped.Load(), rightRaw.dropped.Load())
	}
}

func TestAdaptiveKCPRequestsRecoveryWhenCarrierSilentlyStalls(t *testing.T) {
	previousTimeout := kcpStallTimeout
	kcpStallTimeout = 50 * time.Millisecond
	defer func() { kcpStallTimeout = previousTimeout }()

	leftRaw, rightRaw := newLossyTunnelPair(1)
	left := NewAdaptiveKCPTunnel(leftRaw, func(string, ...any) {})
	right := NewAdaptiveKCPTunnel(rightRaw, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()
	left.EnableKCP()
	right.EnableKCP()

	recovery := make(chan struct{}, 1)
	left.SetOnStall(func() { recovery <- struct{}{} })
	go func() {
		payload := EncodeFrame(9, MsgData, make([]byte, AdaptiveKCPRelayReadBuf))
		for i := 0; i < kcpBalancedWindow+64; i++ {
			left.SendData(payload)
		}
	}()

	select {
	case <-recovery:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled carrier did not request recovery")
	}
	if got := left.TunnelMetrics().KCPStallRecoveries; got != 1 {
		t.Fatalf("stall recoveries=%d, want 1", got)
	}
}

func TestAdaptiveKCPRequestsRecoveryWithoutAckProgress(t *testing.T) {
	previousTimeout := kcpAckProgressTimeout
	kcpAckProgressTimeout = 60 * time.Millisecond
	defer func() { kcpAckProgressTimeout = previousTimeout }()

	leftRaw, rightRaw := newOneWayTunnelPair()
	left := NewAdaptiveKCPTunnel(leftRaw, func(string, ...any) {})
	right := NewAdaptiveKCPTunnel(rightRaw, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()
	left.EnableKCP()
	right.EnableKCP()

	recovery := make(chan struct{}, 1)
	left.SetOnStall(func() { recovery <- struct{}{} })
	go func() {
		payload := EncodeFrame(9, MsgData, make([]byte, AdaptiveKCPRelayReadBuf))
		for i := 0; i < kcpBalancedWindow; i++ {
			left.SendData(payload)
		}
	}()

	select {
	case <-recovery:
	case <-time.After(3 * time.Second):
		t.Fatal("one-way ACK stall did not request recovery")
	}
	if got := left.TunnelMetrics().KCPAckStallRecoveries; got != 1 {
		t.Fatalf("ack stall recoveries=%d, want 1", got)
	}
}

func expectTunnelPayload(t *testing.T, ch <-chan []byte, want []byte) {
	t.Helper()
	select {
	case got := <-ch:
		if !bytes.Equal(got, want) {
			t.Fatalf("payload mismatch\n got: %x\nwant: %x", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel payload")
	}
}

type lossyMemoryTunnel struct {
	mu        sync.Mutex
	peer      *lossyMemoryTunnel
	onData    func([]byte)
	onClose   func()
	inbound   chan []byte
	dropEvery uint64
	seen      atomic.Uint64
	dropped   atomic.Uint64
}

type blockingSendTunnel struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	sent    chan byte
}

func (t *blockingSendTunnel) SendData(data []byte) {
	t.once.Do(func() {
		close(t.entered)
		<-t.release
	})
	t.sent <- data[0]
}

func (*blockingSendTunnel) SetOnData(func([]byte)) {}
func (*blockingSendTunnel) SetOnClose(func())      {}
func (*blockingSendTunnel) Reconfigure(int, int)   {}

func newOneWayTunnelPair() (*lossyMemoryTunnel, *lossyMemoryTunnel) {
	left, right := newLossyTunnelPair(0)
	// Drop every framed KCP packet travelling from right to left. The forward
	// carrier remains alive, reproducing the asymmetric Android field failure.
	left.dropEvery = 1
	return left, right
}

func newLossyTunnelPair(dropEvery uint64) (*lossyMemoryTunnel, *lossyMemoryTunnel) {
	left := &lossyMemoryTunnel{inbound: make(chan []byte, 4096), dropEvery: dropEvery}
	right := &lossyMemoryTunnel{inbound: make(chan []byte, 4096), dropEvery: dropEvery}
	left.peer = right
	right.peer = left
	go left.run()
	go right.run()
	return left, right
}

func (t *lossyMemoryTunnel) run() {
	for data := range t.inbound {
		if len(data) >= len(adaptiveKCPMagic) && bytes.Equal(data[:len(adaptiveKCPMagic)], adaptiveKCPMagic[:]) {
			seen := t.seen.Add(1)
			if t.dropEvery > 0 && seen%t.dropEvery == 0 {
				t.dropped.Add(1)
				continue
			}
		}
		t.mu.Lock()
		cb := t.onData
		t.mu.Unlock()
		if cb != nil {
			cb(data)
		}
	}
}

func (t *lossyMemoryTunnel) SendData(data []byte) {
	t.peer.inbound <- bytes.Clone(data)
}

func (t *lossyMemoryTunnel) SetOnData(fn func([]byte)) {
	t.mu.Lock()
	t.onData = fn
	t.mu.Unlock()
}

func (t *lossyMemoryTunnel) SetOnClose(fn func()) {
	t.mu.Lock()
	t.onClose = fn
	t.mu.Unlock()
}

func (t *lossyMemoryTunnel) Reconfigure(int, int) {}
