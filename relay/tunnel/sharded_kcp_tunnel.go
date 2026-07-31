package tunnel

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
)

const maxKCPShardCount = 8

type kcpShardCarrier interface {
	DataTunnel
	EnableRoundRobinStriping()
	SendDataOnTrack(trackIndex int, data []byte)
	SubTunnelCount() int
}

// ShardedKCPTunnel keeps the legacy KCP conversation on lane zero until both
// peers negotiate CapabilityKCPShards. Matching peers then place every logical
// flow into one of seven independent data conversations while lane zero remains
// available for global control. A blocked/lost flow can no longer fill the KCP
// window used by unrelated flows.
type ShardedKCPTunnel struct {
	inner    kcpShardCarrier
	lanes    []*KCPTunnel
	adapters []*kcpShardInner
	logFn    func(string, ...any)

	activeShards atomic.Int32
	flowLanes    sync.Map
	mu           sync.Mutex
	onData       func([]byte)
	onClose      func()
	closeOnce    sync.Once
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
	t := &ShardedKCPTunnel{inner: inner, logFn: logFn}
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
	lane, connID, msgType, isFlow := t.selectFlowLane(data)
	t.lanes[lane].SendData(data)
	if isFlow && msgType == MsgClose {
		t.flowLanes.Delete(connID)
	}
}

// selectFlowLane freezes an existing flow on the lane where it started. This
// matters during the capability handshake: a flow opened on legacy lane zero
// must not jump to a newly enabled shard and let later bytes overtake data that
// is still buffered in the base KCP conversation.
func (t *ShardedKCPTunnel) selectFlowLane(frame []byte) (lane int, connID uint32, msgType byte, isFlow bool) {
	connID, msgType, ok := muxFrameIdentity(frame)
	if !ok || connID == ControlConnID {
		return 0, connID, msgType, false
	}
	if existing, ok := t.flowLanes.Load(connID); ok {
		return existing.(int), connID, msgType, true
	}
	candidate := selectKCPShard(frame, t.KCPShardCount())
	actual, _ := t.flowLanes.LoadOrStore(connID, candidate)
	return actual.(int), connID, msgType, true
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
	if count == 1 {
		// Every handshake/reset returns to the legacy conversation after
		// RelayBridge has closed its flows. Forget stale ids as well: a new
		// Joiner process may restart connID allocation from one.
		t.flowLanes.Range(func(key, _ any) bool {
			t.flowLanes.Delete(key)
			return true
		})
	}
	previous := int(t.activeShards.Swap(int32(count)))
	if previous != count && t.logFn != nil {
		t.logFn("kcp-shards: active %d -> %d (lane 0 control, %d flow lanes)", previous, count, count-1)
	}
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
	if index < 0 || index >= t.KCPShardCount() {
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
	t.mu.Lock()
	cb := t.onData
	t.mu.Unlock()
	if cb != nil {
		cb(data)
	}
	// A remote CLOSE terminates the local half of the same logical flow. Drop
	// its affinity only after RelayBridge has processed the frame so concurrent
	// final writes cannot move ahead of it on another conversation.
	if connID, msgType, ok := muxFrameIdentity(data); ok && connID != ControlConnID && msgType == MsgClose {
		t.flowLanes.Delete(connID)
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
	metrics := TunnelMetrics{
		Kind:          "kcp-vp8-sharded-" + t.Profile(),
		KCPShardCount: active,
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
