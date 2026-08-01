package tunnel

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestSelectKCPShardUsesControlLaneAndEveryDataLane(t *testing.T) {
	if got := selectKCPShard(EncodeFrame(ControlConnID, MsgHello, nil), 8); got != 0 {
		t.Fatalf("control frame shard=%d, want 0", got)
	}
	if got := selectKCPShard([]byte("malformed"), 8); got != 0 {
		t.Fatalf("malformed frame shard=%d, want 0", got)
	}

	used := make(map[int]bool)
	for connID := uint32(1); connID <= 128; connID++ {
		frame := EncodeFrame(connID, MsgData, []byte("payload"))
		first := selectKCPShard(frame, 8)
		second := selectKCPShard(frame, 8)
		if first != second {
			t.Fatalf("conn %d moved from shard %d to %d", connID, first, second)
		}
		if first < 1 || first > 7 {
			t.Fatalf("conn %d selected invalid data shard %d", connID, first)
		}
		used[first] = true
	}
	if len(used) != 7 {
		t.Fatalf("used data shards=%v, want all seven", used)
	}
}

func TestShardedKCPFreezesFlowsAcrossNegotiation(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()

	oldFlow := EncodeFrame(11, MsgConnect, []byte("old"))
	lane, _, _, ok := sharded.selectFlowLane(oldFlow)
	if !ok || lane != 0 {
		t.Fatalf("pre-handshake flow lane=%d ok=%t, want legacy lane 0", lane, ok)
	}
	sharded.SetKCPShardCount(4)
	lane, _, _, _ = sharded.selectFlowLane(EncodeFrame(11, MsgData, []byte("later")))
	if lane != 0 {
		t.Fatalf("existing flow moved to lane %d after negotiation", lane)
	}
	lane, _, _, _ = sharded.selectFlowLane(EncodeFrame(12, MsgConnect, []byte("new")))
	if lane == 0 {
		t.Fatal("new post-handshake flow stayed on reserved control lane")
	}
	sharded.SetKCPShardCount(1)
	lane, _, _, _ = sharded.selectFlowLane(EncodeFrame(12, MsgData, []byte("after reset")))
	if lane != 0 {
		t.Fatalf("reset retained stale flow lane %d, want legacy lane 0", lane)
	}
}

func TestShardedKCPFlowStripingUsesEveryDataLane(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 8}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()
	sharded.SetKCPShardCount(8)
	sharded.SetFlowStripingEnabled(true)

	for i := 0; i < 28; i++ {
		sharded.SendData(EncodeFrame(77, MsgData, bytes.Repeat([]byte{byte(i)}, 128)))
	}
	waitFor(t, func() bool {
		return len(carrier.dataTracks()) >= 7
	}, "striped flow did not emit a KCP segment on every data lane")

	used := make(map[int]bool)
	for _, track := range carrier.dataTracks() {
		if track > 0 {
			used[track] = true
		}
	}
	if len(used) != 7 {
		t.Fatalf("single flow used tracks=%v, want data tracks 1..7", used)
	}
}

func TestShardedKCPPriorityFramesUseReservedControlLane(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 8}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()
	sharded.SetKCPShardCount(8)
	sharded.SetFlowStripingEnabled(true)

	sharded.SendData(EncodeFrame(71, MsgConnect, []byte("example.invalid:443")))
	sharded.SendData(EncodeFrame(72, MsgDNSQuery, []byte("dns-query")))
	waitFor(t, func() bool { return len(carrier.dataTracks()) >= 2 }, "priority frames were not emitted")
	for _, track := range carrier.dataTracks() {
		if track != 0 {
			t.Fatalf("priority frame used bulk track %d, want reserved track 0", track)
		}
	}

	carrier.mu.Lock()
	carrier.sentTracks = nil
	carrier.mu.Unlock()
	sharded.SendData(EncodeFrame(73, MsgData, []byte("bulk")))
	waitFor(t, func() bool {
		for _, track := range carrier.dataTracks() {
			if track > 0 {
				return true
			}
		}
		return false
	}, "bulk frame was not emitted on a data track")
}

func TestShardedKCPFlowStripingSkipsSaturatedLane(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()
	sharded.SetKCPShardCount(4)
	sharded.SetFlowStripingEnabled(true)
	fillKCPWindow(t, sharded.lanes[1])

	sharded.SendData(EncodeFrame(78, MsgData, []byte("uses-next-free-lane")))

	if sent := sharded.lanes[1].sentMessages.Load(); sent != 0 {
		t.Fatalf("saturated lane accepted %d messages", sent)
	}
	if sent := sharded.lanes[2].sentMessages.Load(); sent != 1 {
		t.Fatalf("next free lane accepted %d messages, want 1", sent)
	}
	metrics := sharded.TunnelMetrics()
	if metrics.KCPSaturatedLaneSkips == 0 {
		t.Fatal("saturated lane skip was not recorded")
	}
	if metrics.KCPAllLanesFullPolls != 0 {
		t.Fatalf("dispatcher reported all lanes full %d times with free lanes", metrics.KCPAllLanesFullPolls)
	}
}

func TestShardedKCPFlowStripingPrefersLeastOccupiedLane(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()
	sharded.SetKCPShardCount(4)
	sharded.SetFlowStripingEnabled(true)
	queueKCPMessages(t, sharded.lanes[1], 20)
	queueKCPMessages(t, sharded.lanes[2], 10)
	queueKCPMessages(t, sharded.lanes[3], 1)

	sharded.SendData(EncodeFrame(80, MsgData, []byte("uses-least-occupied-lane")))

	if sent := sharded.lanes[3].sentMessages.Load(); sent != 1 {
		t.Fatalf("least occupied lane accepted %d messages, want 1", sent)
	}
	if sent := sharded.lanes[1].sentMessages.Load() + sharded.lanes[2].sentMessages.Load(); sent != 0 {
		t.Fatalf("more occupied lanes accepted %d messages", sent)
	}
}

func TestShardedKCPFlowStripingBackpressuresOnlyWhenAllDataLanesFull(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()
	sharded.SetKCPShardCount(4)
	sharded.SetFlowStripingEnabled(true)
	for lane := 1; lane < 4; lane++ {
		fillKCPWindow(t, sharded.lanes[lane])
	}

	done := make(chan struct{})
	go func() {
		sharded.SendData(EncodeFrame(79, MsgData, []byte("waits-for-capacity")))
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("striped send completed while every data lane was full")
	case <-time.After(30 * time.Millisecond):
	}
	waitFor(t, func() bool {
		return sharded.TunnelMetrics().KCPAllLanesFullPolls > 0
	}, "all-lanes-full backpressure was not recorded")

	// Open one atomic admission slot without acknowledging any other lane. The
	// blocked dispatcher must resume through this lane rather than waiting for
	// the originally selected lane.
	lane := sharded.lanes[3]
	lane.mu.Lock()
	lane.maxWaitSnd++
	lane.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("striped send did not resume after one data lane became available")
	}
	if sent := lane.sentMessages.Load(); sent != 1 {
		t.Fatalf("newly available lane accepted %d messages, want 1", sent)
	}
	metrics := sharded.TunnelMetrics()
	if metrics.KCPDispatchWaitNanos == 0 || metrics.KCPMaxDispatchWait == 0 {
		t.Fatalf("dispatcher wait was not measured: %+v", metrics)
	}
}

func TestShardedKCPFlowStripingReordersAcrossLanesAndDrainsClose(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()

	received := make(chan []byte, 4)
	sharded.SetOnData(func(data []byte) { received <- bytes.Clone(data) })
	frames := [][]byte{
		EncodeFrame(91, MsgData, []byte("zero")),
		EncodeFrame(91, MsgData, []byte("one")),
		EncodeFrame(91, MsgData, []byte("two")),
		EncodeFrame(91, MsgClose, nil),
	}

	// Simulate independent KCP lanes completing in a different order.
	sharded.deliverData(encodeKCPFlowStripe(91, 2, MsgData, []byte("two")))
	sharded.deliverData(encodeKCPFlowStripe(91, 1, MsgData, []byte("one")))
	select {
	case got := <-received:
		t.Fatalf("delivered before missing sequence zero: %x", got)
	default:
	}
	sharded.deliverData(encodeKCPFlowStripe(91, 0, MsgData, []byte("zero")))
	sharded.deliverData(encodeKCPFlowStripe(91, 3, MsgClose, nil))
	for _, want := range frames {
		assertFrameReceived(t, received, want)
	}

	metrics := sharded.TunnelMetrics()
	if metrics.KCPReorderFrames != 0 || metrics.KCPReorderBytes != 0 || metrics.KCPReorderFlows != 0 {
		t.Fatalf("reorder state leaked after close: %+v", metrics)
	}
}

func TestShardedKCPFlowStripingKeepsPreNegotiationFlowOnLegacyLane(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()

	oldFlow := EncodeFrame(101, MsgConnect, []byte("old"))
	sharded.selectFlowLane(oldFlow)
	sharded.SetKCPShardCount(4)
	sharded.SetFlowStripingEnabled(true)
	lane, _, _, _ := sharded.selectFlowLane(EncodeFrame(101, MsgData, []byte("later")))
	if lane != 0 {
		t.Fatalf("pre-negotiation flow moved to lane %d", lane)
	}
	newLane, _, _, _ := sharded.selectFlowLane(EncodeFrame(102, MsgConnect, []byte("new")))
	if newLane == 0 {
		t.Fatal("post-negotiation striped flow stayed on control lane")
	}
}

func TestShardedKCPRemoteCloseDoesNotResetOutboundStripeSequence(t *testing.T) {
	carrier := &memoryShardCarrier{tracks: 4}
	sharded := newShardedKCPTunnel(carrier, func(string, ...any) {})
	defer sharded.Stop()
	sharded.SetKCPShardCount(4)
	sharded.SetFlowStripingEnabled(true)
	sharded.SetOnData(func([]byte) {})

	flow, _, _, ok := sharded.selectSendFlow(EncodeFrame(111, MsgData, []byte("outbound")))
	if !ok || !flow.striped {
		t.Fatal("expected striped outbound state")
	}
	flow.nextSeq = 7
	sharded.deliverFrame(EncodeFrame(111, MsgClose, nil))
	flowAfter, _, _, _ := sharded.selectSendFlow(EncodeFrame(111, MsgClose, nil))
	if flowAfter != flow || flowAfter.nextSeq != 7 {
		t.Fatalf("remote close reset outbound sequence: flow_same=%t seq=%d", flowAfter == flow, flowAfter.nextSeq)
	}
}

func TestKCPFlowStripeEnvelopeRoundTrip(t *testing.T) {
	if ShardedKCPRelayReadBuf+kcpMuxFrameOverhead+kcpFlowStripeOverhead+kcpHeaderSize != kcpSegmentMTU {
		t.Fatalf("striped read buffer does not fit one KCP segment: read=%d mtu=%d", ShardedKCPRelayReadBuf, kcpSegmentMTU)
	}
	want := EncodeFrame(123, MsgData, []byte("payload"))
	envelope := encodeKCPFlowStripe(123, 456, MsgData, []byte("payload"))
	connID, seq, got, msgType, ok := decodeKCPFlowStripe(envelope)
	if !ok || connID != 123 || seq != 456 || msgType != MsgData || !bytes.Equal(got, want) {
		t.Fatalf("decoded conn=%d seq=%d type=%d ok=%t frame=%x", connID, seq, msgType, ok, got)
	}
}

func TestShardedKCPDeliversBidirectionally(t *testing.T) {
	leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
	left := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
	right := newShardedKCPTunnel(rightCarrier, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()
	left.SetKCPShardCount(4)
	right.SetKCPShardCount(4)

	leftReceived := make(chan []byte, 1)
	rightReceived := make(chan []byte, 1)
	left.SetOnData(func(data []byte) { leftReceived <- bytes.Clone(data) })
	right.SetOnData(func(data []byte) { rightReceived <- bytes.Clone(data) })

	toRight := EncodeFrame(41, MsgData, []byte("left-to-right"))
	toLeft := EncodeFrame(42, MsgData, []byte("right-to-left"))
	left.SendData(toRight)
	right.SendData(toLeft)
	assertFrameReceived(t, rightReceived, toRight)
	assertFrameReceived(t, leftReceived, toLeft)

	if tracks := leftCarrier.dataTracks(); len(tracks) == 0 || tracks[0] == 0 {
		t.Fatalf("left data did not use a sharded physical track: %v", tracks)
	}
}

func TestShardedKCPFlowStripingDeliversLargeFlowBidirectionally(t *testing.T) {
	leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
	left := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
	right := newShardedKCPTunnel(rightCarrier, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()
	left.SetKCPShardCount(4)
	right.SetKCPShardCount(4)
	left.SetFlowStripingEnabled(true)
	right.SetFlowStripingEnabled(true)

	leftReceived := make(chan []byte, 64)
	rightReceived := make(chan []byte, 64)
	left.SetOnData(func(data []byte) { leftReceived <- bytes.Clone(data) })
	right.SetOnData(func(data []byte) { rightReceived <- bytes.Clone(data) })
	for i := 0; i < 24; i++ {
		toRight := EncodeFrame(141, MsgData, bytes.Repeat([]byte{byte(i)}, ShardedKCPRelayReadBuf))
		toLeft := EncodeFrame(142, MsgData, bytes.Repeat([]byte{byte(255 - i)}, ShardedKCPRelayReadBuf))
		left.SendData(toRight)
		right.SendData(toLeft)
		assertFrameReceived(t, rightReceived, toRight)
		assertFrameReceived(t, leftReceived, toLeft)
	}
}

func TestShardedKCPLegacyLaneInteroperatesWithKCPTunnel(t *testing.T) {
	leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
	left := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
	right := NewKCPTunnel(rightCarrier, func(string, ...any) {})
	defer left.Stop()
	defer right.Stop()

	received := make(chan []byte, 1)
	right.SetOnData(func(data []byte) { received <- bytes.Clone(data) })
	want := EncodeFrame(7, MsgData, []byte("legacy-compatible"))
	left.SendData(want)
	assertFrameReceived(t, received, want)
}

func TestRelayBridgeNegotiatesKCPShardsAndFallsBackForLegacyPeer(t *testing.T) {
	t.Run("matching", func(t *testing.T) {
		leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
		leftTunnel := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
		rightTunnel := newShardedKCPTunnel(rightCarrier, func(string, ...any) {})
		defer leftTunnel.Stop()
		defer rightTunnel.Stop()
		left := NewRelayBridge(leftTunnel, "joiner", AdaptiveKCPRelayReadBuf, func(string, ...any) {})
		right := NewRelayBridge(rightTunnel, "creator", AdaptiveKCPRelayReadBuf, func(string, ...any) {})
		defer left.Close()
		defer right.Close()
		left.sendHello()
		right.sendHello()

		waitFor(t, func() bool {
			leftResult, leftOK := left.NegotiatedHandshake()
			rightResult, rightOK := right.NegotiatedHandshake()
			return leftOK && rightOK && leftResult.Supports(CapabilityKCPShards) &&
				rightResult.Supports(CapabilityKCPShards) &&
				leftResult.Supports(CapabilityPriorityControl) &&
				rightResult.Supports(CapabilityPriorityControl) &&
				leftResult.Supports(CapabilityReliableDNS) &&
				rightResult.Supports(CapabilityReliableDNS) &&
				leftResult.Supports(CapabilityKCPFlowStriping) &&
				rightResult.Supports(CapabilityKCPFlowStriping) &&
				leftTunnel.KCPShardCount() == 4 && rightTunnel.KCPShardCount() == 4 &&
				leftTunnel.FlowStripingEnabled() && rightTunnel.FlowStripingEnabled()
		}, "matching peers did not activate four KCP shards")
	})

	t.Run("alpha42_shards_without_flow_striping", func(t *testing.T) {
		leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
		leftTunnel := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
		rightTunnel := newShardedKCPTunnel(rightCarrier, func(string, ...any) {})
		defer leftTunnel.Stop()
		defer rightTunnel.Stop()
		left := NewRelayBridge(leftTunnel, "joiner", ShardedKCPRelayReadBuf, func(string, ...any) {})
		right := NewRelayBridge(rightTunnel, "creator", ShardedKCPRelayReadBuf, func(string, ...any) {})
		defer left.Close()
		defer right.Close()
		right.handshakeMu.Lock()
		right.localHello.Capabilities &^= CapabilityKCPFlowStriping
		right.localHello.Nonce = newHandshakeNonce()
		right.peerHello = nil
		right.handshakeResult = nil
		right.handshakeMu.Unlock()
		left.Reset()
		right.Reset()

		waitFor(t, func() bool {
			result, ok := left.NegotiatedHandshake()
			return ok && result.Supports(CapabilityKCPShards) &&
				!result.Supports(CapabilityKCPFlowStriping) && leftTunnel.KCPShardCount() == 4
		}, "alpha.42 capability subset did not retain flow-affinity shards")
		if leftTunnel.FlowStripingEnabled() || rightTunnel.FlowStripingEnabled() {
			t.Fatal("flow striping activated without mutual capability")
		}
	})

	t.Run("alpha45_shards_without_priority_dns", func(t *testing.T) {
		leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
		leftTunnel := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
		rightTunnel := newShardedKCPTunnel(rightCarrier, func(string, ...any) {})
		defer leftTunnel.Stop()
		defer rightTunnel.Stop()
		left := NewRelayBridge(leftTunnel, "joiner", ShardedKCPRelayReadBuf, func(string, ...any) {})
		right := NewRelayBridge(rightTunnel, "creator", ShardedKCPRelayReadBuf, func(string, ...any) {})
		defer left.Close()
		defer right.Close()
		right.handshakeMu.Lock()
		right.localHello.Capabilities &^= CapabilityPriorityControl | CapabilityReliableDNS
		right.localHello.Nonce = newHandshakeNonce()
		right.peerHello = nil
		right.handshakeResult = nil
		right.handshakeMu.Unlock()
		left.Reset()
		right.Reset()

		waitFor(t, func() bool {
			result, ok := left.NegotiatedHandshake()
			return ok && result.Supports(CapabilityKCPShards) &&
				result.Supports(CapabilityKCPFlowStriping) &&
				!result.Supports(CapabilityPriorityControl) &&
				!result.Supports(CapabilityReliableDNS) &&
				leftTunnel.KCPShardCount() == 4 && leftTunnel.FlowStripingEnabled()
		}, "alpha.45 capability subset did not retain shards and flow striping")
	})

	t.Run("legacy", func(t *testing.T) {
		leftCarrier, rightCarrier := newMemoryShardCarrierPair(4)
		leftTunnel := newShardedKCPTunnel(leftCarrier, func(string, ...any) {})
		rightTunnel := NewKCPTunnel(rightCarrier, func(string, ...any) {})
		defer leftTunnel.Stop()
		defer rightTunnel.Stop()
		left := NewRelayBridge(leftTunnel, "joiner", AdaptiveKCPRelayReadBuf, func(string, ...any) {})
		right := NewRelayBridge(rightTunnel, "creator", AdaptiveKCPRelayReadBuf, func(string, ...any) {})
		defer left.Close()
		defer right.Close()
		left.sendHello()
		right.sendHello()

		waitFor(t, func() bool {
			leftResult, leftOK := left.NegotiatedHandshake()
			rightResult, rightOK := right.NegotiatedHandshake()
			return leftOK && rightOK && !leftResult.Supports(CapabilityKCPShards) &&
				!rightResult.Supports(CapabilityKCPShards)
		}, "legacy capability intersection did not complete")
		if got := leftTunnel.KCPShardCount(); got != 1 {
			t.Fatalf("legacy peer activated %d shards, want 1", got)
		}
	})
}

func assertFrameReceived(t *testing.T, received <-chan []byte, want []byte) {
	t.Helper()
	select {
	case got := <-received:
		if !bytes.Equal(got, want) {
			t.Fatalf("received %x, want %x", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for KCP delivery")
	}
}

func waitFor(t *testing.T, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(failure)
}

func fillKCPWindow(t *testing.T, lane *KCPTunnel) {
	t.Helper()
	lane.mu.Lock()
	defer lane.mu.Unlock()
	for lane.kcp.WaitSnd() < lane.maxWaitSnd {
		if result := lane.kcp.Send([]byte{1}); result != 0 {
			t.Fatalf("fill KCP window: send result=%d", result)
		}
	}
}

func queueKCPMessages(t *testing.T, lane *KCPTunnel, count int) {
	t.Helper()
	lane.mu.Lock()
	defer lane.mu.Unlock()
	for i := 0; i < count; i++ {
		if result := lane.kcp.Send([]byte{1}); result != 0 {
			t.Fatalf("queue KCP message %d: send result=%d", i, result)
		}
	}
}

type memoryShardCarrier struct {
	mu         sync.Mutex
	peer       *memoryShardCarrier
	onData     func([]byte)
	onClose    func()
	tracks     int
	sentTracks []int
}

func newMemoryShardCarrierPair(tracks int) (*memoryShardCarrier, *memoryShardCarrier) {
	left := &memoryShardCarrier{tracks: tracks}
	right := &memoryShardCarrier{tracks: tracks}
	left.peer = right
	right.peer = left
	return left, right
}

func (c *memoryShardCarrier) EnableRoundRobinStriping() {}

func (c *memoryShardCarrier) SendData(data []byte) {
	c.deliver(-1, data)
}

func (c *memoryShardCarrier) SendDataOnTrack(trackIndex int, data []byte) {
	c.deliver(trackIndex, data)
}

func (c *memoryShardCarrier) deliver(trackIndex int, data []byte) {
	c.mu.Lock()
	c.sentTracks = append(c.sentTracks, trackIndex)
	peer := c.peer
	c.mu.Unlock()
	if peer == nil {
		return
	}
	peer.mu.Lock()
	cb := peer.onData
	peer.mu.Unlock()
	if cb != nil {
		cb(bytes.Clone(data))
	}
}

func (c *memoryShardCarrier) SetOnData(fn func([]byte)) {
	c.mu.Lock()
	c.onData = fn
	c.mu.Unlock()
}

func (c *memoryShardCarrier) SetOnClose(fn func()) {
	c.mu.Lock()
	c.onClose = fn
	c.mu.Unlock()
}

func (*memoryShardCarrier) Reconfigure(int, int) {}

func (c *memoryShardCarrier) SubTunnelCount() int { return c.tracks }

func (c *memoryShardCarrier) dataTracks() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]int, 0, len(c.sentTracks))
	for _, track := range c.sentTracks {
		if track >= 0 {
			result = append(result, track)
		}
	}
	return result
}
