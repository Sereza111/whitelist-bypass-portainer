package tunnel

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
)

const (
	kcpConversationID = 0x77627374
	kcpUpdateInterval = 10 * time.Millisecond
	kcpBalancedWindow = 256
	kcpFastWindow     = 512
	kcpStableWindow   = 128
	kcpSegmentMTU     = 1000
	kcpReceiveBufSize = 128 * 1024
	kcpStatsEvery     = 500
	kcpOutputQueueDepth = 256
	kcpBackpressurePoll = 5 * time.Millisecond
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

	stopCh   chan struct{}
	stopOnce sync.Once
	outputCh chan []byte
	profile  string
	maxWaitSnd int

	sentMessages      atomic.Uint64
	deliveredMessages atomic.Uint64
	sentBytes         atomic.Uint64
	deliveredBytes    atomic.Uint64
	outputSegments    atomic.Uint64
	droppedSegments   atomic.Uint64
	inputSegments     atomic.Uint64
	backpressureNanos atomic.Uint64
}

func NewKCPTunnel(inner DataTunnel, logFn func(string, ...any)) *KCPTunnel {
	t := newKCPTunnel(inner, kcpSegmentMTU, logFn)
	t.SetProfile(KCPProfileFast)
	return t
}

func newKCPTunnel(inner DataTunnel, segmentMTU int, logFn func(string, ...any)) *KCPTunnel {
	t := &KCPTunnel{
		inner:   inner,
		logFn:   logFn,
		recvBuf: make([]byte, kcpReceiveBufSize),
		stopCh:  make(chan struct{}),
		outputCh: make(chan []byte, kcpOutputQueueDepth),
	}
	t.kcp = kcp.NewKCP(kcpConversationID, func(buf []byte, size int) {
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
	t.mu.Lock()
	t.kcp.Input(segment, kcp.IKCP_PACKET_REGULAR, true)
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

	if cb == nil {
		return
	}
	for _, message := range messages {
		t.deliveredMessages.Add(1)
		t.deliveredBytes.Add(uint64(len(message)))
		cb(message)
	}
}

func (t *KCPTunnel) TunnelMetrics() TunnelMetrics {
	t.mu.Lock()
	waitSnd := t.kcp.WaitSnd()
	profile := t.profile
	t.mu.Unlock()
	return TunnelMetrics{
		Kind:              "kcp-vp8-" + profile,
		SentBytes:         t.sentBytes.Load(),
		ReceivedBytes:     t.deliveredBytes.Load(),
		SentFrames:        t.sentMessages.Load(),
		ReceivedFrames:    t.deliveredMessages.Load(),
		KCPInputSegments:  t.inputSegments.Load(),
		KCPOutputSegments: t.outputSegments.Load(),
		KCPDroppedSegments: t.droppedSegments.Load(),
		KCPWaitSnd:        waitSnd,
		KCPBackpressureNanos: t.backpressureNanos.Load(),
		KCPOutputQueueDepth:  len(t.outputCh),
		KCPOutputQueueCap:    cap(t.outputCh),
		TrackCount:        1,
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
			t.mu.Unlock()
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
