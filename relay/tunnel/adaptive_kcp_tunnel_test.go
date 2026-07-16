package tunnel

import (
	"bytes"
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
