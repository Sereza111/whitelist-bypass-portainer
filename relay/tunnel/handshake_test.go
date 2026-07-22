package tunnel

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestHelloRoundTrip(t *testing.T) {
	want := Hello{
		WireVersion:       WireVersion,
		Capabilities:      CapabilityMetricsV1 | CapabilityVideoKCP1,
		MaxCarrierPayload: 1126,
		Reliability:       ReliabilityRawVP8,
		TrackCount:        2,
		Nonce:             [16]byte{1, 2, 3, 4},
		BuildVersion:      "0.4.0-alpha.5+abcdef0",
		BuildCommit:       "abcdef0123456789",
	}
	frame := EncodeHello(want)
	var got Hello
	var ok bool
	DecodeFrames(frame, func(connID uint32, msgType byte, payload []byte) {
		if connID != ControlConnID || msgType != MsgHello {
			t.Fatalf("unexpected frame conn=%d type=%d", connID, msgType)
		}
		got, ok = DecodeHello(payload)
	})
	if !ok {
		t.Fatal("hello did not decode")
	}
	if got != want {
		t.Fatalf("hello mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestHelloRejectsMalformedPayload(t *testing.T) {
	if _, ok := DecodeHello([]byte("short")); ok {
		t.Fatal("short hello accepted")
	}
	h := Hello{WireVersion: WireVersion, Nonce: [16]byte{1}}
	frame := EncodeHello(h)
	DecodeFrames(frame, func(_ uint32, _ byte, payload []byte) {
		payload[0] = 'X'
		if _, ok := DecodeHello(payload); ok {
			t.Fatal("hello with invalid magic accepted")
		}
	})
}

func TestHelloAckRoundTrip(t *testing.T) {
	want := HelloAck{
		SelectedWireVersion: WireVersion,
		Status:              HandshakeOK,
		Capabilities:        CapabilityMetricsV1,
		EchoNonce:           [16]byte{1, 2, 3},
		ResponderNonce:      [16]byte{4, 5, 6},
	}
	frame := EncodeHelloAck(want)
	DecodeFrames(frame, func(connID uint32, msgType byte, payload []byte) {
		if connID != ControlConnID || msgType != MsgHelloAck {
			t.Fatalf("unexpected frame conn=%d type=%d", connID, msgType)
		}
		got, ok := DecodeHelloAck(payload)
		if !ok {
			t.Fatal("hello ack did not decode")
		}
		if got != want {
			t.Fatalf("hello ack mismatch\n got: %#v\nwant: %#v", got, want)
		}
	})
}

func TestDNSDestination(t *testing.T) {
	for _, addr := range []string{"1.1.1.1:53", "[2606:4700:4700::1111]:53", "dns.example:53"} {
		if !isDNSDestination(addr) {
			t.Fatalf("DNS destination not recognized: %s", addr)
		}
	}
	for _, addr := range []string{"1.1.1.1:443", "broken", "1.1.1.1"} {
		if isDNSDestination(addr) {
			t.Fatalf("non-DNS destination accepted: %s", addr)
		}
	}
}

func TestRelayBridgeNegotiatesCapability(t *testing.T) {
	leftTunnel, rightTunnel := newMemoryTunnelPair()
	logFn := func(string, ...any) {}
	left := NewRelayBridge(leftTunnel, "joiner", 1126, logFn)
	right := NewRelayBridge(rightTunnel, "creator", 1126, logFn)
	defer left.Close()
	defer right.Close()

	left.ConfigureHandshake(CapabilityMetricsV1|CapabilityVideoKCP1|CapabilityPriorityControl|CapabilityReliableDNS, 1126, ReliabilityRawVP8, 1)
	right.ConfigureHandshake(CapabilityMetricsV1, 1126, ReliabilityRawVP8, 1)
	left.sendHello()
	right.sendHello()

	// NewRelayBridge starts with a baseline Hello before callers can apply
	// feature-specific capabilities. The configured generation retries after
	// one second, so allow the same bounded recovery path that production uses
	// instead of depending on goroutine delivery order in memoryTunnel.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		leftResult, leftOK := left.NegotiatedHandshake()
		rightResult, rightOK := right.NegotiatedHandshake()
		if leftOK && rightOK && leftResult.Capabilities == CapabilityMetricsV1 &&
			rightResult.Capabilities == CapabilityMetricsV1 {
			if leftResult.Supports(CapabilityPriorityControl) || rightResult.Supports(CapabilityPriorityControl) {
				t.Fatal("priority control activated without support from both peers")
			}
			if leftResult.Supports(CapabilityReliableDNS) || rightResult.Supports(CapabilityReliableDNS) {
				t.Fatal("reliable DNS activated without support from both peers")
			}
			leftMetrics := left.MetricsSnapshot()
			rightMetrics := right.MetricsSnapshot()
			if leftMetrics.SentFrames == 0 || leftMetrics.ReceivedFrames == 0 ||
				rightMetrics.SentFrames == 0 || rightMetrics.ReceivedFrames == 0 {
				t.Fatalf("handshake traffic missing from metrics: left=%#v right=%#v", leftMetrics, rightMetrics)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	leftResult, _ := left.NegotiatedHandshake()
	rightResult, _ := right.NegotiatedHandshake()
	t.Fatalf("capability was not negotiated: left=%#v right=%#v", leftResult, rightResult)
}

func TestReliableDNSRequiresNegotiatedPriorityLane(t *testing.T) {
	rb := &RelayBridge{}
	rb.recordHandshakeResult(HandshakeResult{
		Status: HandshakeOK, SelectedWireVersion: WireVersion,
		Capabilities: CapabilityReliableDNS,
	})
	if rb.supportsReliableDNS() {
		t.Fatal("reliable DNS activated without priority control")
	}
	rb.recordHandshakeResult(HandshakeResult{
		Status: HandshakeOK, SelectedWireVersion: WireVersion,
		Capabilities: CapabilityReliableDNS | CapabilityPriorityControl,
	})
	if !rb.supportsReliableDNS() {
		t.Fatal("reliable DNS did not activate after full negotiation")
	}
}

func TestReliableDNSLatencyMetrics(t *testing.T) {
	rb := &RelayBridge{}
	packet := []byte{0x12, 0x34, 0x01, 0x00}
	rb.trackDNSQuery(7, packet)
	time.Sleep(time.Millisecond)
	rb.recordDNSReply(7, packet)
	if got := rb.reliableDNSReplies.Load(); got != 1 {
		t.Fatalf("reliable DNS replies=%d, want 1", got)
	}
	if rb.dnsLatencyNanos.Load() == 0 || rb.maxDNSLatencyNanos.Load() == 0 {
		t.Fatal("DNS latency metrics were not recorded")
	}
	rb.recordDNSReply(7, packet)
	if got := rb.reliableDNSReplies.Load(); got != 1 {
		t.Fatalf("duplicate DNS reply changed metrics: %d", got)
	}
}

type memoryTunnel struct {
	mu      sync.Mutex
	peer    *memoryTunnel
	onData  func([]byte)
	onClose func()
}

func newMemoryTunnelPair() (*memoryTunnel, *memoryTunnel) {
	left := &memoryTunnel{}
	right := &memoryTunnel{}
	left.peer = right
	right.peer = left
	return left, right
}

func (t *memoryTunnel) SendData(data []byte) {
	t.peer.mu.Lock()
	cb := t.peer.onData
	t.peer.mu.Unlock()
	if cb != nil {
		copy := bytes.Clone(data)
		go cb(copy)
	}
}

func (t *memoryTunnel) SetOnData(fn func([]byte)) {
	t.mu.Lock()
	t.onData = fn
	t.mu.Unlock()
}

func (t *memoryTunnel) SetOnClose(fn func()) {
	t.mu.Lock()
	t.onClose = fn
	t.mu.Unlock()
}

func (t *memoryTunnel) Reconfigure(int, int) {}
