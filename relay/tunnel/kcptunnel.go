package tunnel

import (
	"encoding/binary"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
)

const (
	kcpConversationID = 0x77627374
	kcpUpdateInterval = 10 * time.Millisecond
	// Video carriers regularly see 100ms+ base RTT and multi-second RTT while
	// an SFU reshapes a burst. The old 128/256 windows capped throughput to
	// roughly window/RTT (the 256 profile measured 0.5-0.7 Mbps at 3-4s RTT).
	// Keep the conservative profile available, but give normal profiles enough
	// bandwidth-delay product for a transcontinental call.
	// Balanced used to allow 1024 unacknowledged ~1 KiB segments. At the
	// measured 1 Mbps carrier that alone represented roughly eight seconds of
	// hidden queue and produced 7s loaded ping without packet loss or an ACK
	// stall. Keep enough BDP for a lossy SFU, but bound normal queue growth.
	kcpBalancedWindow   = 512
	kcpFastWindow       = 2048
	kcpStableWindow     = 256
	kcpSegmentMTU       = 1000
	kcpReceiveBufSize   = 128 * 1024
	kcpStatsEvery       = 500
	kcpOutputQueueDepth = 1024
	kcpBackpressurePoll = 5 * time.Millisecond
	kcpHighWaterPercent = 75
)

var (
	kcpStallTimeout       = 12 * time.Second
	kcpAckProgressTimeout = 15 * time.Second
)

const (
	KCPProfileFast     = "fast"
	KCPProfileBalanced = "balanced"
	KCPProfileStable   = "stable"
)

type KCPTunnel struct {
	inner DataTunnel
	logFn func(string, ...any)

	mu      sync.Mutex
	kcp     *kcp.KCP
	recvBuf []byte
	onData  func([]byte)
	onClose func()

	stopCh     chan struct{}
	stopOnce   sync.Once
	outputCh   chan []byte
	profile    string
	maxWaitSnd int
	stallMu    sync.Mutex
	onStall    func()

	sentMessages       atomic.Uint64
	deliveredMessages  atomic.Uint64
	sentBytes          atomic.Uint64
	deliveredBytes     atomic.Uint64
	outputSegments     atomic.Uint64
	droppedSegments    atomic.Uint64
	inputSegments      atomic.Uint64
	backpressureNanos  atomic.Uint64
	lastInputUnixNano  atomic.Int64
	lastAckUnixNano    atomic.Int64
	lastAckSequence    atomic.Uint32
	highWaterUnixNano  atomic.Int64
	stallRecoveries    atomic.Uint64
	ackStallRecoveries atomic.Uint64
	stallNotified      atomic.Bool
}

func NewKCPTunnel(inner DataTunnel, logFn func(string, ...any)) *KCPTunnel {
	t := newKCPTunnel(inner, kcpSegmentMTU, logFn)
	t.SetProfile(KCPProfileFast)
	return t
}

func newKCPTunnel(inner DataTunnel, segmentMTU int, logFn func(string, ...any)) *KCPTunnel {
	return newKCPTunnelWithConversation(inner, segmentMTU, kcpConversationID, logFn)
}

func newKCPTunnelWithConversation(inner DataTunnel, segmentMTU int, conversationID uint32, logFn func(string, ...any)) *KCPTunnel {
	t := &KCPTunnel{
		inner:    inner,
		logFn:    logFn,
		recvBuf:  make([]byte, kcpReceiveBufSize),
		stopCh:   make(chan struct{}),
		outputCh: make(chan []byte, kcpOutputQueueDepth),
	}
	t.kcp = kcp.NewKCP(conversationID, func(buf []byte, size int) {
		if size <= 0 {
			return
		}
		segment := make([]byte, size)
		copy(segment, buf[:size])
		select {
		case t.outputCh <- segment:
			t.outputSegments.Add(1)
		default:
			// Treat a saturated carrier as packet loss. Blocking here would
			// hold the KCP mutex, delay ACK processing and amplify collapse.
			t.droppedSegments.Add(1)
		}
	})
	t.kcp.SetMtu(segmentMTU)
	now := time.Now().UnixNano()
	t.lastInputUnixNano.Store(now)
	t.lastAckUnixNano.Store(now)
	t.SetProfile(KCPProfileBalanced)
	inner.SetOnData(t.handleInnerData)
	inner.SetOnClose(t.handleInnerClose)
	go t.outputLoop()
	go t.updateLoop()
	return t
}

func (t *KCPTunnel) SendData(data []byte) {
	if len(data) == 0 {
		return
	}
	started := time.Now()
	for {
		t.mu.Lock()
		if t.kcp.WaitSnd() < t.maxWaitSnd {
			t.sentMessages.Add(1)
			t.sentBytes.Add(uint64(len(data)))
			t.kcp.Send(data)
			t.kcp.Update()
			t.mu.Unlock()
			t.backpressureNanos.Add(uint64(time.Since(started)))
			return
		}
		t.mu.Unlock()
		select {
		case <-t.stopCh:
			return
		case <-time.After(kcpBackpressurePoll):
		}
	}
}

func (t *KCPTunnel) SetProfile(profile string) string {
	profile = strings.ToLower(strings.TrimSpace(profile))
	t.mu.Lock()
	switch profile {
	case KCPProfileFast:
		t.kcp.NoDelay(1, 10, 2, 1)
		t.kcp.WndSize(kcpFastWindow, kcpFastWindow)
		t.maxWaitSnd = kcpFastWindow
	case KCPProfileStable:
		t.kcp.NoDelay(0, 30, 2, 0)
		t.kcp.WndSize(kcpStableWindow, kcpStableWindow)
		t.maxWaitSnd = kcpStableWindow
	default:
		profile = KCPProfileBalanced
		t.kcp.NoDelay(1, 20, 2, 0)
		t.kcp.WndSize(kcpBalancedWindow, kcpBalancedWindow)
		t.maxWaitSnd = kcpBalancedWindow
	}
	t.profile = profile
	maxWaitSnd := t.maxWaitSnd
	t.mu.Unlock()
	if t.logFn != nil {
		t.logFn("kcptunnel: profile=%s max_wait_snd=%d", profile, maxWaitSnd)
	}
	return profile
}

func (t *KCPTunnel) Profile() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.profile
}

func (t *KCPTunnel) SetOnData(fn func([]byte)) {
	t.mu.Lock()
	t.onData = fn
	t.mu.Unlock()
}

func (t *KCPTunnel) SetOnClose(fn func()) {
	t.mu.Lock()
	t.onClose = fn
	t.mu.Unlock()
}

// SetOnStall installs a recovery hook used by VK Creator/Joiner to close the
// signaling transport and force a clean WebRTC rejoin. KCP cannot recover when
// the SFU silently stops forwarding a still-"connected" video track.
func (t *KCPTunnel) SetOnStall(fn func()) {
	t.stallMu.Lock()
	t.onStall = fn
	t.stallMu.Unlock()
}

func (t *KCPTunnel) Reconfigure(fps, batch int) {
	t.inner.Reconfigure(fps, batch)
}

func (t *KCPTunnel) Stop() {
	t.stopOnce.Do(func() { close(t.stopCh) })
}

func (t *KCPTunnel) handleInnerData(segment []byte) {
	if len(segment) == 0 {
		return
	}
	t.inputSegments.Add(1)
	t.lastInputUnixNano.Store(time.Now().UnixNano())
	ackSequence, hasAckProgress := kcpPacketAckProgress(segment)
	t.mu.Lock()
	waitSndBefore := t.kcp.WaitSnd()
	t.kcp.Input(segment, kcp.IKCP_PACKET_REGULAR, true)
	waitSndAfter := t.kcp.WaitSnd()
	cb := t.onData
	var messages [][]byte
	if cb != nil {
		for {
			size := t.kcp.PeekSize()
			if size <= 0 {
				break
			}
			if size > len(t.recvBuf) {
				t.recvBuf = make([]byte, size)
			}
			n := t.kcp.Recv(t.recvBuf)
			if n <= 0 {
				break
			}
			message := make([]byte, n)
			copy(message, t.recvBuf[:n])
			messages = append(messages, message)
		}
	}
	t.mu.Unlock()
	sequenceAdvanced := hasAckProgress && advanceAtomicSequence(&t.lastAckSequence, ackSequence)
	if sequenceAdvanced || waitSndAfter < waitSndBefore {
		t.lastAckUnixNano.Store(time.Now().UnixNano())
		t.stallNotified.Store(false)
	}
	if cb == nil {
		return
	}
	for _, message := range messages {
		t.deliveredMessages.Add(1)
		t.deliveredBytes.Add(uint64(len(message)))
		cb(message)
	}
}

// kcpPacketAckProgress recognizes KCP ACK/UNA information without relying
// on unexported kcp-go state. Any valid segment carries UNA; an ACK segment also
// explicitly confirms one sequence number. Only a strictly newer sequence is
// progress; repeated inbound bulk packets with the same UNA must not hide a
// one-way acknowledgement stall.
func kcpPacketAckProgress(packet []byte) (uint32, bool) {
	const (
		kcpHeaderSize = 24
		kcpCommandACK = 82
	)
	var newest uint32
	found := false
	for len(packet) >= kcpHeaderSize {
		cmd := packet[4]
		sn := binary.LittleEndian.Uint32(packet[12:16])
		una := binary.LittleEndian.Uint32(packet[16:20])
		payloadSize := int(binary.LittleEndian.Uint32(packet[20:24]))
		if payloadSize < 0 || payloadSize > len(packet)-kcpHeaderSize {
			return 0, false
		}
		candidate := una
		candidateFound := una != 0
		if cmd == kcpCommandACK && (!candidateFound || sequenceAfter(sn+1, candidate)) {
			candidate = sn + 1
			candidateFound = true
		}
		if candidateFound && (!found || sequenceAfter(candidate, newest)) {
			newest = candidate
			found = true
		}
		packet = packet[kcpHeaderSize+payloadSize:]
	}
	return newest, found
}

func sequenceAfter(next, current uint32) bool {
	return int32(next-current) > 0
}

func advanceAtomicSequence(target *atomic.Uint32, next uint32) bool {
	for current := target.Load(); sequenceAfter(next, current); current = target.Load() {
		if target.CompareAndSwap(current, next) {
			return true
		}
	}
	return false
}

func (t *KCPTunnel) TunnelMetrics() TunnelMetrics {
	t.mu.Lock()
	waitSnd := t.kcp.WaitSnd()
	profile := t.profile
	t.mu.Unlock()
	return TunnelMetrics{
		Kind:                  "kcp-vp8-" + profile,
		SentBytes:             t.sentBytes.Load(),
		ReceivedBytes:         t.deliveredBytes.Load(),
		SentFrames:            t.sentMessages.Load(),
		ReceivedFrames:        t.deliveredMessages.Load(),
		KCPInputSegments:      t.inputSegments.Load(),
		KCPOutputSegments:     t.outputSegments.Load(),
		KCPDroppedSegments:    t.droppedSegments.Load(),
		KCPWaitSnd:            waitSnd,
		KCPBackpressureNanos:  t.backpressureNanos.Load(),
		KCPOutputQueueDepth:   len(t.outputCh),
		KCPOutputQueueCap:     cap(t.outputCh),
		KCPStallRecoveries:    t.stallRecoveries.Load(),
		KCPAckStallRecoveries: t.ackStallRecoveries.Load(),
		KCPLastInputAgeNanos:  uint64(time.Since(time.Unix(0, t.lastInputUnixNano.Load()))),
		KCPLastAckAgeNanos:    uint64(time.Since(time.Unix(0, t.lastAckUnixNano.Load()))),
		TrackCount:            1,
	}
}

func (t *KCPTunnel) outputLoop() {
	for {
		select {
		case <-t.stopCh:
			return
		case segment := <-t.outputCh:
			t.inner.SendData(segment)
		}
	}
}

func (t *KCPTunnel) handleInnerClose() {
	t.stopOnce.Do(func() { close(t.stopCh) })
	t.mu.Lock()
	cb := t.onClose
	t.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (t *KCPTunnel) updateLoop() {
	ticker := time.NewTicker(kcpUpdateInterval)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.mu.Lock()
			t.kcp.Update()
			waitSnd := t.kcp.WaitSnd()
			maxWaitSnd := t.maxWaitSnd
			t.mu.Unlock()
			lastInput := time.Unix(0, t.lastInputUnixNano.Load())
			lastAck := time.Unix(0, t.lastAckUnixNano.Load())
			highWater := maxWaitSnd * kcpHighWaterPercent / 100
			now := time.Now()
			if waitSnd < highWater {
				t.highWaterUnixNano.Store(0)
			} else if t.highWaterUnixNano.Load() == 0 {
				t.highWaterUnixNano.CompareAndSwap(0, now.UnixNano())
			}
			highWaterStarted := t.highWaterUnixNano.Load()
			highWaterSince := time.Unix(0, highWaterStarted)
			ackProgressSince := lastAck
			if highWaterSince.After(ackProgressSince) {
				ackProgressSince = highWaterSince
			}
			silentStall := waitSnd >= maxWaitSnd && time.Since(lastInput) >= kcpStallTimeout
			ackStall := waitSnd >= highWater && highWaterStarted != 0 && now.Sub(ackProgressSince) >= kcpAckProgressTimeout
			if (silentStall || ackStall) && t.stallNotified.CompareAndSwap(false, true) {
				t.stallRecoveries.Add(1)
				if ackStall && !silentStall {
					t.ackStallRecoveries.Add(1)
				}
				if t.logFn != nil {
					t.logFn("kcptunnel: stalled wait_snd=%d/%d no_input_for=%s no_ack_progress_for=%s; requesting carrier reconnect", waitSnd, maxWaitSnd, time.Since(lastInput).Round(time.Second), time.Since(lastAck).Round(time.Second))
				}
				t.stallMu.Lock()
				onStall := t.onStall
				t.stallMu.Unlock()
				if onStall != nil {
					go onStall()
				}
			}
			ticks++
			if ticks%kcpStatsEvery == 0 && t.logFn != nil {
				t.logFn("kcptunnel: sent=%d delivered=%d out_segs=%d dropped_segs=%d in_segs=%d wait_snd=%d",
					t.sentMessages.Load(), t.deliveredMessages.Load(),
					t.outputSegments.Load(), t.droppedSegments.Load(),
					t.inputSegments.Load(), waitSnd)
			}
		}
	}
}
