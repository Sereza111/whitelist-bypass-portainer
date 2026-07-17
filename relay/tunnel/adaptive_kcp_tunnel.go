package tunnel

import (
	"bytes"
	"encoding/binary"
	"sync"
	"sync/atomic"
)

const (
	adaptiveKCPMarkerSize    = 4
	adaptiveKCPHeaderSize    = 24
	adaptiveKCPFrameOverhead = 9
	adaptiveKCPSegmentMTU    = 1122
	AdaptiveKCPRelayReadBuf  = adaptiveKCPSegmentMTU - adaptiveKCPHeaderSize - adaptiveKCPFrameOverhead
)

var adaptiveKCPMagic = [adaptiveKCPMarkerSize]byte{'W', 'K', 'C', '1'}

type AdaptiveKCPTunnel struct {
	inner   DataTunnel
	kcp     *KCPTunnel
	adapter *adaptiveKCPInner
	logFn   func(string, ...any)
	mode    atomic.Uint32
	ready   chan struct{}
	once    sync.Once

	mu      sync.Mutex
	onData  func([]byte)
	onClose func()
}

type adaptiveKCPInner struct {
	parent *AdaptiveKCPTunnel

	mu      sync.Mutex
	onData  func([]byte)
	onClose func()
}

func NewAdaptiveKCPTunnel(inner DataTunnel, logFn func(string, ...any)) *AdaptiveKCPTunnel {
	t := &AdaptiveKCPTunnel{inner: inner, logFn: logFn, ready: make(chan struct{})}
	t.adapter = &adaptiveKCPInner{parent: t}
	t.kcp = newKCPTunnel(t.adapter, adaptiveKCPSegmentMTU, logFn)
	t.kcp.SetOnData(t.deliverData)
	t.kcp.SetOnClose(t.deliverClose)
	inner.SetOnData(t.handleInnerData)
	inner.SetOnClose(t.handleInnerClose)
	return t
}

func (t *AdaptiveKCPTunnel) EnableKCP() bool {
	if !t.mode.CompareAndSwap(0, 2) {
		return false
	}
	t.once.Do(func() { close(t.ready) })
	if t.logFn != nil {
		t.logFn("adaptive-kcp: reliable data path enabled; raw control path retained")
	}
	return true
}

func (t *AdaptiveKCPTunnel) KCPEnabled() bool {
	return t.mode.Load() == 2
}

func (t *AdaptiveKCPTunnel) SetKCPProfile(profile string) string {
	return t.kcp.SetProfile(profile)
}

func (t *AdaptiveKCPTunnel) EnableRawCompatibility() bool {
	if !t.mode.CompareAndSwap(0, 1) {
		return false
	}
	t.once.Do(func() { close(t.ready) })
	if t.logFn != nil {
		t.logFn("adaptive-kcp: legacy raw data path enabled")
	}
	return true
}

func (t *AdaptiveKCPTunnel) SendData(data []byte) {
	if isControlFrame(data) {
		t.inner.SendData(data)
		return
	}
	if t.mode.Load() == 0 {
		<-t.ready
	}
	if t.mode.Load() == 2 {
		t.kcp.SendData(data)
		return
	}
	t.inner.SendData(data)
}

func (t *AdaptiveKCPTunnel) SetOnData(fn func([]byte)) {
	t.mu.Lock()
	t.onData = fn
	t.mu.Unlock()
}

func (t *AdaptiveKCPTunnel) SetOnClose(fn func()) {
	t.mu.Lock()
	t.onClose = fn
	t.mu.Unlock()
}

func (t *AdaptiveKCPTunnel) Reconfigure(fps, batch int) {
	t.inner.Reconfigure(fps, batch)
}

func (t *AdaptiveKCPTunnel) Stop() {
	t.EnableRawCompatibility()
	t.kcp.Stop()
}

func (t *AdaptiveKCPTunnel) handleInnerData(data []byte) {
	if len(data) >= len(adaptiveKCPMagic) && bytes.Equal(data[:len(adaptiveKCPMagic)], adaptiveKCPMagic[:]) {
		t.adapter.deliverData(data[len(adaptiveKCPMagic):])
		return
	}
	t.deliverData(data)
}

func (t *AdaptiveKCPTunnel) handleInnerClose() {
	t.adapter.deliverClose()
}

func (t *AdaptiveKCPTunnel) deliverData(data []byte) {
	t.mu.Lock()
	cb := t.onData
	t.mu.Unlock()
	if cb != nil {
		cb(data)
	}
}

func (t *AdaptiveKCPTunnel) deliverClose() {
	t.mu.Lock()
	cb := t.onClose
	t.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (t *AdaptiveKCPTunnel) TunnelMetrics() TunnelMetrics {
	kcpMetrics := t.kcp.TunnelMetrics()
	if provider, ok := t.inner.(tunnelMetricsProvider); ok {
		innerMetrics := provider.TunnelMetrics()
		if t.mode.Load() != 2 {
			innerMetrics.Kind = "adaptive-kcp-raw"
			return innerMetrics
		}
		kcpMetrics.QueueDepth = innerMetrics.QueueDepth
		kcpMetrics.QueueCapacity = innerMetrics.QueueCapacity
		kcpMetrics.MaxQueueDepth = innerMetrics.MaxQueueDepth
		kcpMetrics.SendWaitNanos += innerMetrics.SendWaitNanos
		kcpMetrics.TrackCount = innerMetrics.TrackCount
	}
	if t.mode.Load() == 2 {
		kcpMetrics.Kind = "adaptive-kcp-active-" + t.kcp.Profile()
	} else {
		kcpMetrics.Kind = "adaptive-kcp-raw"
	}
	return kcpMetrics
}

func (a *adaptiveKCPInner) SendData(segment []byte) {
	framed := make([]byte, len(adaptiveKCPMagic)+len(segment))
	copy(framed, adaptiveKCPMagic[:])
	copy(framed[len(adaptiveKCPMagic):], segment)
	a.parent.inner.SendData(framed)
}

func (a *adaptiveKCPInner) SetOnData(fn func([]byte)) {
	a.mu.Lock()
	a.onData = fn
	a.mu.Unlock()
}

func (a *adaptiveKCPInner) SetOnClose(fn func()) {
	a.mu.Lock()
	a.onClose = fn
	a.mu.Unlock()
}

func (a *adaptiveKCPInner) Reconfigure(fps, batch int) {
	a.parent.inner.Reconfigure(fps, batch)
}

func (a *adaptiveKCPInner) deliverData(data []byte) {
	a.mu.Lock()
	cb := a.onData
	a.mu.Unlock()
	if cb != nil {
		cb(data)
	}
}

func (a *adaptiveKCPInner) deliverClose() {
	a.mu.Lock()
	cb := a.onClose
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func isControlFrame(data []byte) bool {
	if len(data) < 9 {
		return false
	}
	frameLen := int(binary.BigEndian.Uint32(data[0:4]))
	if frameLen < 5 || frameLen+4 != len(data) {
		return false
	}
	return binary.BigEndian.Uint32(data[4:8]) == ControlConnID
}
