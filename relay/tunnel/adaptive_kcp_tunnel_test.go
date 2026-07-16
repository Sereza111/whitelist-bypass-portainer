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
