package tunnel

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"
)

const maxKCPShardCount = 8

const (
	// A striped mux message adds a 4-byte per-flow sequence and preserves the
	// original message type in its payload. Keeping RelayBridge reads below one
	// KCP MSS avoids turning every full TCP read into two separately paced VP8
	// samples (the second one used to contain only a short tail).
	kcpHeaderSize           = 24
	kcpMuxFrameOverhead     = 9
	kcpFlowStripeOverhead   = 5
	ShardedKCPRelayReadBuf  = kcpSegmentMTU - kcpHeaderSize - kcpMuxFrameOverhead - kcpFlowStripeOverhead
	maxKCPFlowReorderFrames = 16 * 1024
)

type kcpShardCarrier interface {
	DataTunnel
	EnableRoundRobinStriping()
	SendDataOnTrack(trackIndex int, data []byte)
	SubTunnelCount() int
}

// ShardedKCPTunnel keeps the legacy KCP conversation on lane zero until both
// peers negotiate CapabilityKCPShards. Alpha.42 peers keep one logical flow on
// one of seven data conversations. Peers which also negotiate
// CapabilityKCPFlowStriping sequence and spread each new flow across every data
// conversation, then restore mux order before RelayBridge. Lane zero remains
// available for global control.
type ShardedKCPTunnel struct {
	inner    kcpShardCarrier
	lanes    []*KCPTunnel
	adapters []*kcpShardInner
	logFn    func(string, ...any)

	activeShards      atomic.Int32
	flowStriping      atomic.Bool
	flowMu            sync.Mutex
	sendFlows         map[uint32]*kcpShardSendFlow
	recvMu            sync.Mutex
	recvFlows         map[uint32]*kcpShardReceiveFlow
	reorderFrames     atomic.Int64
	reorderBytes      atomic.Int64
	maxReorderFrames  atomic.Uint64
	maxReorderBytes   atomic.Uint64
	malformedStripes  atomic.Uint64
	dispatchWaitNanos atomic.Uint64
	maxDispatchWait   atomic.Uint64
	saturatedSkips    atomic.Uint64
	allLanesFullPolls atomic.Uint64
	mu                sync.Mutex
	onData            func([]byte)
	onClose           func()
	closeOnce         sync.Once
	stopOnce          sync.Once
	stopCh            chan struct{}
}

type kcpShardSendFlow struct {
	mu       sync.Mutex
	striped  bool
	lane     int
	nextSeq  uint32
	nextLane uint32
}

type kcpStripedFrame struct {
	data    []byte
	msgType byte
}

type kcpShardReceiveFlow struct {
	mu      sync.Mutex
	nextSeq uint32
	pending map[uint32]kcpStripedFrame
}

type kcpShardInner struct {
	parent *ShardedKCPTunnel
	index  int

	mu      sync.Mutex
	onData  func([]byte)
	onClose func()
}

func NewShardedKCPTunnel(inner *MultiTrackTunnel, logFn func(string, ...any)) *ShardedKCPTunnel {
	return newShardedKCPTunnel(inner, logFn)
}

func newShardedKCPTunnel(inner kcpShardCarrier, logFn func(string, ...any)) *ShardedKCPTunnel {
	t := &ShardedKCPTunnel{
		inner: inner, logFn: logFn,
		sendFlows: make(map[uint32]*kcpShardSendFlow),
		recvFlows: make(map[uint32]*kcpShardReceiveFlow),
		stopCh:    make(chan struct{}),
	}
	t.activeShards.Store(1)
	inner.EnableRoundRobinStriping()
	for index := 0; index < maxKCPShardCount; index++ {
		adapter := &kcpShardInner{parent: t, index: index}
		lane := newKCPTunnelWithConversation(adapter, kcpSegmentMTU, kcpConversationID+uint32(index), nil)
		lane.SetOnData(t.deliverData)
		lane.SetOnClose(t.deliverClose)
		t.adapters = append(t.adapters, adapter)
		t.lanes = append(t.lanes, lane)
	}
	inner.SetOnData(t.handleInnerData)
	inner.SetOnClose(t.handleInnerClose)
	if logFn != nil {
		logFn("kcp-shards: prepared=%d active=1 profile=%s legacy_lane=0", len(t.lanes), t.Profile())
	}
	return t
}

func (t *ShardedKCPTunnel) SendData(data []byte) {
	if len(data) == 0 {
		return
	}
	// Lane zero is deliberately excluded from striped bulk traffic. Use that
	// reserved capacity for connection setup and reliable DNS so a saturated or
	// heavily reordered download cannot make the device look offline.
	if isPriorityMuxFrame(data) {
		t.lanes[0].SendData(data)
		return
	}
	flow, connID, msgType, isFlow := t.selectSendFlow(data)
	if !isFlow {
		t.lanes[0].SendData(data)
		return
	}

	flow.mu.Lock()
	lane := flow.lane
	sent := true
	if flow.striped && t.KCPShardCount() > 1 {
		sent = t.sendStripedFlowFrameLocked(flow, connID, msgType, data[9:])
	} else {
		flow.mu.Unlock()
		t.lanes[lane].SendData(data)
	}
	if sent && msgType == MsgClose {
		t.deleteSendFlow(connID, flow)
	}
}

// sendStripedFlowFrameLocked assigns the sequence only after a data lane has
// atomically accepted the frame. A saturated lane is skipped instead of
// blocking the single fair-sender goroutine while other independent KCP
// windows still have room. Backpressure is applied only when every data lane
// is full. The caller holds flow.mu so concurrent writes retain exact order.
func (t *ShardedKCPTunnel) sendStripedFlowFrameLocked(flow *kcpShardSendFlow, connID uint32, msgType byte, payload []byte) bool {
	defer flow.mu.Unlock()
	shardCount := t.KCPShardCount()
	dataLaneCount := shardCount - 1
	if dataLaneCount < 1 {
		return false
	}
	sequence := flow.nextSeq
	outbound := encodeKCPFlowStripe(connID, sequence, msgType, payload)
	startLane := int(flow.nextLane % uint32(dataLaneCount))
	started := time.Now()
	waited := false
	for {
		candidates := t.availableDataLanes(startLane, dataLaneCount)
		for _, candidate := range candidates {
			lane := 1 + candidate
			if t.lanes[lane].trySendData(outbound) {
				flow.nextSeq++
				flow.nextLane = uint32((candidate + 1) % dataLaneCount)
				if waited {
					elapsed := uint64(time.Since(started))
					t.dispatchWaitNanos.Add(elapsed)
					updateAtomicMax(&t.maxDispatchWait, elapsed)
				}
				return true
			}
			t.saturatedSkips.Add(1)
		}
		waited = true
		t.allLanesFullPolls.Add(1)
		select {
		case <-t.stopCh:
			elapsed := uint64(time.Since(started))
			t.dispatchWaitNanos.Add(elapsed)
			updateAtomicMax(&t.maxDispatchWait, elapsed)
			return false
		case <-time.After(kcpBackpressurePoll):
		}
	}
}

// availableDataLanes returns lanes with capacity from least to most occupied.
// The round-robin cursor breaks ties, retaining even distribution when ACK
// rates match while avoiding additional work on a slower, nearly full lane.
func (t *ShardedKCPTunnel) availableDataLanes(startLane, dataLaneCount int) []int {
	type candidate struct {
		index   int
		waitSnd int
		window  int
		offset  int
	}
	available := make([]candidate, 0, dataLaneCount)
	for offset := 0; offset < dataLaneCount; offset++ {
		index := (startLane + offset) % dataLaneCount
		waitSnd, window, stopped := t.lanes[1+index].sendWindowOccupancy()
		if stopped || window <= 0 || waitSnd >= window {
			t.saturatedSkips.Add(1)
			continue
		}
		available = append(available, candidate{index: index, waitSnd: waitSnd, window: window, offset: offset})
	}
	for i := 1; i < len(available); i++ {
		for j := i; j > 0; j-- {
			left, right := available[j-1], available[j]
			leftScaled := int64(left.waitSnd) * int64(right.window)
			rightScaled := int64(right.waitSnd) * int64(left.window)
			if leftScaled < rightScaled || (leftScaled == rightScaled && left.offset < right.offset) {
				break
			}
			available[j-1], available[j] = available[j], available[j-1]
		}
	}
	result := make([]int, len(available))
	for i, item := range available {
		result[i] = item.index
	}
	return result
}

// selectFlowLane freezes an existing flow on the lane where it started. This
// matters during the capability handshake: a flow opened on legacy lane zero
// must not jump to a newly enabled shard and let later bytes overtake data that
// is still buffered in the base KCP conversation.
func (t *ShardedKCPTunnel) selectFlowLane(frame []byte) (lane int, connID uint32, msgType byte, isFlow bool) {
	flow, connID, msgType, isFlow := t.selectSendFlow(frame)
	if !isFlow {
		return 0, connID, msgType, false
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.striped && t.KCPShardCount() > 1 {
		return 1 + int(flow.nextLane%uint32(t.KCPShardCount()-1)), connID, msgType, true
	}
	return flow.lane, connID, msgType, true
}

func (t *ShardedKCPTunnel) selectSendFlow(frame []byte) (*kcpShardSendFlow, uint32, byte, bool) {
	connID, msgType, ok := muxFrameIdentity(frame)
	if !ok || connID == ControlConnID {
		return nil, connID, msgType, false
	}
	t.flowMu.Lock()
	defer t.flowMu.Unlock()
	if existing := t.sendFlows[connID]; existing != nil {
		return existing, connID, msgType, true
	}
	striped := t.flowStriping.Load() && t.KCPShardCount() > 1
	lane := selectKCPShard(frame, t.KCPShardCount())
	flow := &kcpShardSendFlow{striped: striped, lane: lane}
	t.sendFlows[connID] = flow
	return flow, connID, msgType, true
}

func (t *ShardedKCPTunnel) deleteSendFlow(connID uint32, expected *kcpShardSendFlow) {
	t.flowMu.Lock()
	if expected == nil || t.sendFlows[connID] == expected {
		delete(t.sendFlows, connID)
	}
	t.flowMu.Unlock()
}

func (t *ShardedKCPTunnel) deleteFixedSendFlow(connID uint32) {
	t.flowMu.Lock()
	if flow := t.sendFlows[connID]; flow != nil && !flow.striped {
		delete(t.sendFlows, connID)
	}
	t.flowMu.Unlock()
}

func (t *ShardedKCPTunnel) SetOnData(fn func([]byte)) {
	t.mu.Lock()
	t.onData = fn
	t.mu.Unlock()
}

func (t *ShardedKCPTunnel) SetOnClose(fn func()) {
	t.mu.Lock()
	t.onClose = fn
	t.mu.Unlock()
}

func (t *ShardedKCPTunnel) Reconfigure(fps, batch int) {
	t.inner.Reconfigure(fps, batch)
}

func (t *ShardedKCPTunnel) Stop() {
	t.stopOnce.Do(func() { close(t.stopCh) })
	for _, lane := range t.lanes {
		lane.Stop()
	}
}

func (t *ShardedKCPTunnel) SetOnStall(fn func()) {
	for _, lane := range t.lanes {
		lane.SetOnStall(fn)
	}
}

func (t *ShardedKCPTunnel) SetProfile(profile string) string {
	var normalized string
	for _, lane := range t.lanes {
		normalized = lane.SetProfile(profile)
	}
	if t.logFn != nil {
		t.logFn("kcp-shards: profile=%s lanes=%d", normalized, len(t.lanes))
	}
	return normalized
}

func (t *ShardedKCPTunnel) Profile() string {
	if len(t.lanes) == 0 {
		return KCPProfileBalanced
	}
	return t.lanes[0].Profile()
}

func (t *ShardedKCPTunnel) CarrierTrackCount() int {
	count := t.inner.SubTunnelCount()
	if count < 1 {
		return 1
	}
	if count > len(t.lanes) {
		return len(t.lanes)
	}
	return count
}

func (t *ShardedKCPTunnel) KCPShardCount() int {
	count := int(t.activeShards.Load())
	if count < 1 {
		return 1
	}
	if count > len(t.lanes) {
		return len(t.lanes)
	}
	return count
}

// SetKCPShardCount is intentionally safe to call repeatedly as Hello/Ack frames
// cross in either order. Setting one restores the exact legacy conversation.
func (t *ShardedKCPTunnel) SetKCPShardCount(count int) {
	if count < 1 {
		count = 1
	}
	if tracks := t.CarrierTrackCount(); count > tracks {
		count = tracks
	}
	if count > len(t.lanes) {
		count = len(t.lanes)
	}
	previous := int(t.activeShards.Swap(int32(count)))
	if count == 1 {
		// Every handshake/reset returns to the legacy conversation after
		// RelayBridge has closed its flows. Forget stale ids as well: a new
		// Joiner process may restart connID allocation from one.
		t.flowStriping.Store(false)
		t.resetFlows()
	}
	if previous != count && t.logFn != nil {
		t.logFn("kcp-shards: active %d -> %d (lane 0 control, %d flow lanes)", previous, count, count-1)
	}
}

// SetFlowStripingEnabled affects only flows which start after capability
// negotiation. A pre-handshake flow retains its alpha.42 lane affinity so no
// bytes can overtake data already buffered in the base conversation.
func (t *ShardedKCPTunnel) SetFlowStripingEnabled(enabled bool) {
	enabled = enabled && t.KCPShardCount() > 1
	previous := t.flowStriping.Swap(enabled)
	if previous != enabled && t.logFn != nil {
		t.logFn("kcp-flow-stripe: enabled=%t data_lanes=%d read_buf=%d", enabled, t.KCPShardCount()-1, ShardedKCPRelayReadBuf)
	}
}

func (t *ShardedKCPTunnel) FlowStripingEnabled() bool {
	return t.flowStriping.Load()
}

func (t *ShardedKCPTunnel) resetFlows() {
	t.flowMu.Lock()
	t.sendFlows = make(map[uint32]*kcpShardSendFlow)
	t.flowMu.Unlock()
	t.recvMu.Lock()
	t.recvFlows = make(map[uint32]*kcpShardReceiveFlow)
	t.recvMu.Unlock()
	t.reorderFrames.Store(0)
	t.reorderBytes.Store(0)
}

func (t *ShardedKCPTunnel) handleInnerData(segment []byte) {
	if len(segment) < 4 {
		return
	}
	conversation := binary.LittleEndian.Uint32(segment[:4])
	if conversation < kcpConversationID {
		return
	}
	index := int(conversation - kcpConversationID)
	// Accept prepared conversations before local send activation. Hello/Ack and
	// video tracks are asynchronous, so a matching peer's first data segment may
	// overtake the lane-0 acknowledgement which enables our outbound striping.
	if index < 0 || index >= t.CarrierTrackCount() {
		return
	}
	t.adapters[index].deliverData(segment)
}

func (t *ShardedKCPTunnel) handleInnerClose() {
	for _, adapter := range t.adapters {
		adapter.deliverClose()
	}
}

func (t *ShardedKCPTunnel) deliverData(data []byte) {
	connID, seq, restored, msgType, striped := decodeKCPFlowStripe(data)
	if striped {
		t.deliverStriped(connID, seq, restored, msgType)
		return
	}
	if _, envelopeType, ok := muxFrameIdentity(data); ok && envelopeType == MsgKCPFlowStripe {
		if t.malformedStripes.Add(1) == 1 && t.logFn != nil {
			t.logFn("kcp-flow-stripe: dropped malformed envelope")
		}
		return
	}
	t.deliverFrame(data)
}

func (t *ShardedKCPTunnel) deliverStriped(connID, seq uint32, frame []byte, msgType byte) {
	t.recvMu.Lock()
	flow := t.recvFlows[connID]
	if flow == nil {
		flow = &kcpShardReceiveFlow{pending: make(map[uint32]kcpStripedFrame)}
		t.recvFlows[connID] = flow
	}
	t.recvMu.Unlock()

	flow.mu.Lock()
	var ready []kcpStripedFrame
	switch {
	case seq == flow.nextSeq:
		ready = append(ready, kcpStripedFrame{data: frame, msgType: msgType})
		flow.nextSeq++
		for {
			pending, ok := flow.pending[flow.nextSeq]
			if !ok {
				break
			}
			delete(flow.pending, flow.nextSeq)
			t.reorderFrames.Add(-1)
			t.reorderBytes.Add(-int64(len(pending.data)))
			ready = append(ready, pending)
			flow.nextSeq++
		}
	case sequenceAfter(seq, flow.nextSeq):
		if _, duplicate := flow.pending[seq]; !duplicate {
			if len(flow.pending) >= maxKCPFlowReorderFrames {
				if t.logFn != nil {
					t.logFn("kcp-flow-stripe: reorder overflow conn=%d expected=%d received=%d; frame rejected", connID, flow.nextSeq, seq)
				}
				flow.mu.Unlock()
				return
			}
			flow.pending[seq] = kcpStripedFrame{data: frame, msgType: msgType}
			currentFrames := t.reorderFrames.Add(1)
			currentBytes := t.reorderBytes.Add(int64(len(frame)))
			updateAtomicMax(&t.maxReorderFrames, uint64(currentFrames))
			updateAtomicMax(&t.maxReorderBytes, uint64(currentBytes))
		}
	}

	closed := false
	for _, item := range ready {
		t.deliverFrame(item.data)
		if item.msgType == MsgClose {
			closed = true
			break
		}
	}
	if closed {
		for _, pending := range flow.pending {
			t.reorderFrames.Add(-1)
			t.reorderBytes.Add(-int64(len(pending.data)))
		}
		flow.pending = nil
		t.recvMu.Lock()
		if t.recvFlows[connID] == flow {
			delete(t.recvFlows, connID)
		}
		t.recvMu.Unlock()
	}
	flow.mu.Unlock()
}

func (t *ShardedKCPTunnel) deliverFrame(data []byte) {
	t.mu.Lock()
	cb := t.onData
	t.mu.Unlock()
	if cb != nil {
		cb(data)
	}
	// A remote CLOSE terminates the local half of the same logical flow. Drop
	// its affinity only after RelayBridge has processed the frame so concurrent
	// final writes cannot move ahead of it on another conversation. A striped
	// outbound direction keeps its sequence until its own CLOSE is sent; resetting
	// it here would restart at sequence zero and make that CLOSE look stale.
	if connID, msgType, ok := muxFrameIdentity(data); ok && connID != ControlConnID && msgType == MsgClose {
		t.deleteFixedSendFlow(connID)
	}
}

func encodeKCPFlowStripe(connID, seq uint32, msgType byte, payload []byte) []byte {
	stripedPayload := make([]byte, kcpFlowStripeOverhead+len(payload))
	binary.BigEndian.PutUint32(stripedPayload[:4], seq)
	stripedPayload[4] = msgType
	copy(stripedPayload[5:], payload)
	return EncodeFrame(connID, MsgKCPFlowStripe, stripedPayload)
}

func decodeKCPFlowStripe(frame []byte) (connID, seq uint32, restored []byte, msgType byte, ok bool) {
	connID, envelopeType, valid := muxFrameIdentity(frame)
	if !valid || connID == ControlConnID || envelopeType != MsgKCPFlowStripe || len(frame) < 14 {
		return 0, 0, nil, 0, false
	}
	seq = binary.BigEndian.Uint32(frame[9:13])
	msgType = frame[13]
	if !isKCPFlowMessage(msgType) {
		return 0, 0, nil, 0, false
	}
	return connID, seq, EncodeFrame(connID, msgType, frame[14:]), msgType, true
}

func isKCPFlowMessage(msgType byte) bool {
	switch msgType {
	case MsgConnect, MsgConnectOK, MsgConnectErr, MsgData, MsgClose, MsgUDP, MsgUDPReply:
		return true
	default:
		return false
	}
}

func (t *ShardedKCPTunnel) deliverClose() {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		cb := t.onClose
		t.mu.Unlock()
		if cb != nil {
			cb()
		}
	})
}

func (t *ShardedKCPTunnel) sendLaneSegment(index int, segment []byte) {
	if t.KCPShardCount() == 1 && index == 0 {
		// Legacy peers understand only the base conversation. Preserve alpha.41's
		// round-robin physical striping until sharding has been negotiated.
		t.inner.SendData(segment)
		return
	}
	t.inner.SendDataOnTrack(index, segment)
}

func selectKCPShard(frame []byte, shardCount int) int {
	connID, _, ok := muxFrameIdentity(frame)
	if shardCount <= 1 || !ok {
		return 0
	}
	if connID == ControlConnID {
		return 0
	}
	// Multiplicative hashing avoids assigning bursts of sequential connection
	// ids to adjacent physical tracks while preserving affinity for the flow.
	hash := connID * 0x9e3779b1
	return 1 + int(hash%uint32(shardCount-1))
}

func muxFrameIdentity(frame []byte) (connID uint32, msgType byte, ok bool) {
	if len(frame) < 9 {
		return 0, 0, false
	}
	frameLength := int(binary.BigEndian.Uint32(frame[:4]))
	if frameLength < 5 || frameLength+4 != len(frame) {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(frame[4:8]), frame[8], true
}

func (t *ShardedKCPTunnel) TunnelMetrics() TunnelMetrics {
	active := t.KCPShardCount()
	kind := "kcp-vp8-sharded-" + t.Profile()
	if t.FlowStripingEnabled() {
		kind = "kcp-vp8-flow-striped-" + t.Profile()
	}
	t.recvMu.Lock()
	receiveFlows := len(t.recvFlows)
	t.recvMu.Unlock()
	metrics := TunnelMetrics{
		Kind:                  kind,
		KCPShardCount:         active,
		KCPFlowStriping:       t.FlowStripingEnabled(),
		KCPReorderFlows:       receiveFlows,
		KCPReorderFrames:      int(t.reorderFrames.Load()),
		KCPReorderBytes:       int(t.reorderBytes.Load()),
		KCPMaxReorderFrames:   t.maxReorderFrames.Load(),
		KCPMaxReorderBytes:    t.maxReorderBytes.Load(),
		KCPDispatchWaitNanos:  t.dispatchWaitNanos.Load(),
		KCPMaxDispatchWait:    t.maxDispatchWait.Load(),
		KCPSaturatedLaneSkips: t.saturatedSkips.Load(),
		KCPAllLanesFullPolls:  t.allLanesFullPolls.Load(),
	}
	var minInputAge, minAckAge uint64
	for index, lane := range t.lanes[:active] {
		item := lane.TunnelMetrics()
		metrics.SentBytes += item.SentBytes
		metrics.ReceivedBytes += item.ReceivedBytes
		metrics.SentFrames += item.SentFrames
		metrics.ReceivedFrames += item.ReceivedFrames
		metrics.KCPInputSegments += item.KCPInputSegments
		metrics.KCPOutputSegments += item.KCPOutputSegments
		metrics.KCPDroppedSegments += item.KCPDroppedSegments
		metrics.KCPWaitSnd += item.KCPWaitSnd
		metrics.KCPWindow += item.KCPWindow
		metrics.KCPBackpressureNanos += item.KCPBackpressureNanos
		metrics.KCPOutputQueueDepth += item.KCPOutputQueueDepth
		metrics.KCPOutputQueueCap += item.KCPOutputQueueCap
		metrics.KCPStallRecoveries += item.KCPStallRecoveries
		metrics.KCPAckStallRecoveries += item.KCPAckStallRecoveries
		metrics.KCPAutoWindowChanges += item.KCPAutoWindowChanges
		metrics.KCPShardWaitSnd = append(metrics.KCPShardWaitSnd, item.KCPWaitSnd)
		metrics.KCPShardSentBytes = append(metrics.KCPShardSentBytes, item.SentBytes)
		metrics.KCPShardReceivedBytes = append(metrics.KCPShardReceivedBytes, item.ReceivedBytes)
		if index == 0 || item.KCPLastInputAgeNanos < minInputAge {
			minInputAge = item.KCPLastInputAgeNanos
		}
		if index == 0 || item.KCPLastAckAgeNanos < minAckAge {
			minAckAge = item.KCPLastAckAgeNanos
		}
	}
	metrics.KCPBackpressureNanos += metrics.KCPDispatchWaitNanos
	metrics.KCPLastInputAgeNanos = minInputAge
	metrics.KCPLastAckAgeNanos = minAckAge
	if provider, ok := t.inner.(tunnelMetricsProvider); ok {
		inner := provider.TunnelMetrics()
		metrics.QueueDepth = inner.QueueDepth
		metrics.QueueCapacity = inner.QueueCapacity
		metrics.MaxQueueDepth = inner.MaxQueueDepth
		metrics.SendWaitNanos = inner.SendWaitNanos
		metrics.TrackCount = inner.TrackCount
		metrics.TrackSentBytes = inner.TrackSentBytes
		metrics.TrackReceivedBytes = inner.TrackReceivedBytes
		metrics.TrackSentFrames = inner.TrackSentFrames
		metrics.TrackReceivedFrames = inner.TrackReceivedFrames
		metrics.TrackQueueDepths = inner.TrackQueueDepths
		metrics.TrackWriteNanos = inner.TrackWriteNanos
		metrics.TrackMaxWriteNanos = inner.TrackMaxWriteNanos
		metrics.TrackWriteErrors = inner.TrackWriteErrors
	}
	return metrics
}

func (a *kcpShardInner) SendData(segment []byte) {
	a.parent.sendLaneSegment(a.index, segment)
}

func (a *kcpShardInner) SetOnData(fn func([]byte)) {
	a.mu.Lock()
	a.onData = fn
	a.mu.Unlock()
}

func (a *kcpShardInner) SetOnClose(fn func()) {
	a.mu.Lock()
	a.onClose = fn
	a.mu.Unlock()
}

func (a *kcpShardInner) Reconfigure(fps, batch int) {
	a.parent.inner.Reconfigure(fps, batch)
}

func (a *kcpShardInner) deliverData(data []byte) {
	a.mu.Lock()
	cb := a.onData
	a.mu.Unlock()
	if cb != nil {
		cb(data)
	}
}

func (a *kcpShardInner) deliverClose() {
	a.mu.Lock()
	cb := a.onClose
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
}
