package tunnel

import (
	"bytes"
	"encoding/binary"
	"sync"
	"sync/atomic"
)

const (
	adaptiveKCPMarkerSize        = 4
	adaptiveKCPControlMarkerSize = 4
	adaptiveKCPHeaderSize        = 24
	adaptiveKCPFrameOverhead     = 9
	adaptiveKCPSegmentMTU        = 1122
	AdaptiveKCPRelayReadBuf      = adaptiveKCPSegmentMTU - adaptiveKCPHeaderSize - adaptiveKCPFrameOverhead
)

var adaptiveKCPMagic = [adaptiveKCPMarkerSize]byte{'W', 'K', 'C', '1'}
var adaptiveKCPControlMagic = [adaptiveKCPControlMarkerSize]byte{'W', 'K', 'C', '2'}

type AdaptiveKCPTunnel struct {
	inner          DataTunnel
	kcp            *KCPTunnel
	adapter        *adaptiveKCPInner
	controlAdapter *adaptiveKCPControlInner
	controlKCP     *KCPTunnel
	priorityOutput *priorityCarrier
	logFn          func(string, ...any)
	mode           atomic.Uint32
	ready          chan struct{}
	once           sync.Once

	mu              sync.Mutex
	onData          func([]byte)
	onClose         func()
	priorityControl atomic.Bool
}

type adaptiveKCPInner struct {
	parent *AdaptiveKCPTunnel

	mu      sync.Mutex
	onData  func([]byte)
	onClose func()
}

type adaptiveKCPControlInner struct {
	parent *AdaptiveKCPTunnel

	mu      sync.Mutex
	onData  func([]byte)
	onClose func()
}

type priorityCarrier struct {
	inner    DataTunnel
	priority chan []byte
	normal   chan []byte
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewAdaptiveKCPTunnel(inner DataTunnel, logFn func(string, ...any)) *AdaptiveKCPTunnel {
	priorityOutput := newPriorityCarrier(inner)
	t := &AdaptiveKCPTunnel{inner: inner, priorityOutput: priorityOutput, logFn: logFn, ready: make(chan struct{})}
	t.adapter = &adaptiveKCPInner{parent: t}
	t.controlAdapter = &adaptiveKCPControlInner{parent: t}
	t.kcp = newKCPTunnel(t.adapter, adaptiveKCPSegmentMTU, logFn)
	t.controlKCP = newKCPTunnelWithConversation(t.controlAdapter, adaptiveKCPSegmentMTU, kcpConversationID+1, logFn)
	t.controlKCP.SetProfile(KCPProfileStable)
	t.kcp.SetOnData(t.deliverData)
	t.controlKCP.SetOnData(t.deliverData)
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
	normalized := t.kcp.SetProfile(profile)
	t.controlKCP.SetProfile(KCPProfileStable)
	return normalized
}

func (t *AdaptiveKCPTunnel) EnablePriorityControl() {
	t.priorityControl.Store(true)
	if t.logFn != nil {
		t.logFn("adaptive-kcp: separate reliable control lane enabled")
	}
}

func (t *AdaptiveKCPTunnel) SetOnStall(fn func()) {
	t.kcp.SetOnStall(fn)
	t.controlKCP.SetOnStall(fn)
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
	if t.priorityControl.Load() && isKCPProfileFrame(data) {
		t.controlKCP.SendData(data)
		return
	}
	if isControlFrame(data) {
		t.inner.SendData(data)
		return
	}
	if t.mode.Load() == 0 {
		<-t.ready
	}
	if t.mode.Load() == 2 {
		if t.priorityControl.Load() && isPriorityMuxFrame(data) {
			t.controlKCP.SendData(data)
			return
		}
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
	t.controlKCP.Stop()
	t.priorityOutput.Stop()
}

func (t *AdaptiveKCPTunnel) handleInnerData(data []byte) {
	if len(data) >= len(adaptiveKCPMagic) && bytes.Equal(data[:len(adaptiveKCPMagic)], adaptiveKCPMagic[:]) {
		t.adapter.deliverData(data[len(adaptiveKCPMagic):])
		return
	}
	if len(data) >= len(adaptiveKCPControlMagic) && bytes.Equal(data[:len(adaptiveKCPControlMagic)], adaptiveKCPControlMagic[:]) {
		t.controlAdapter.deliverData(data[len(adaptiveKCPControlMagic):])
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
	controlMetrics := t.controlKCP.TunnelMetrics()
	kcpMetrics.KCPControlWaitSnd = controlMetrics.KCPWaitSnd
	kcpMetrics.KCPControlSentFrames = controlMetrics.SentFrames
	kcpMetrics.KCPControlReceivedFrames = controlMetrics.ReceivedFrames
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
	a.parent.priorityOutput.SendNormal(framed)
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

func (a *adaptiveKCPControlInner) SendData(segment []byte) {
	framed := make([]byte, len(adaptiveKCPControlMagic)+len(segment))
	copy(framed, adaptiveKCPControlMagic[:])
	copy(framed[len(adaptiveKCPControlMagic):], segment)
	a.parent.priorityOutput.SendPriority(framed)
}

func newPriorityCarrier(inner DataTunnel) *priorityCarrier {
	p := &priorityCarrier{
		inner: inner, priority: make(chan []byte, 64), normal: make(chan []byte, kcpOutputQueueDepth),
		stopCh: make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *priorityCarrier) SendPriority(data []byte) { p.enqueue(p.priority, data) }
func (p *priorityCarrier) SendNormal(data []byte)   { p.enqueue(p.normal, data) }

func (p *priorityCarrier) enqueue(queue chan []byte, data []byte) {
	copyData := bytes.Clone(data)
	select {
	case queue <- copyData:
	case <-p.stopCh:
	}
}

func (p *priorityCarrier) run() {
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}
		select {
		case data := <-p.priority:
			p.inner.SendData(data)
			continue
		default:
		}
		select {
		case <-p.stopCh:
			return
		case data := <-p.priority:
			p.inner.SendData(data)
		case data := <-p.normal:
			p.inner.SendData(data)
		}
	}
}

func (p *priorityCarrier) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

func (a *adaptiveKCPControlInner) SetOnData(fn func([]byte)) {
	a.mu.Lock()
	a.onData = fn
	a.mu.Unlock()
}

func (a *adaptiveKCPControlInner) SetOnClose(fn func()) {
	a.mu.Lock()
	a.onClose = fn
	a.mu.Unlock()
}

func (a *adaptiveKCPControlInner) Reconfigure(fps, batch int) {
	a.parent.inner.Reconfigure(fps, batch)
}

func (a *adaptiveKCPControlInner) deliverData(data []byte) {
	a.mu.Lock()
	cb := a.onData
	a.mu.Unlock()
	if cb != nil {
		cb(data)
	}
}

func isPriorityMuxFrame(data []byte) bool {
	if len(data) < 9 {
		return false
	}
	frameLen := int(binary.BigEndian.Uint32(data[0:4]))
	if frameLen < 5 || frameLen+4 != len(data) {
		return false
	}
	switch data[8] {
	case MsgConnect, MsgConnectOK, MsgConnectErr:
		return true
	default:
		return false
	}
}

func isKCPProfileFrame(data []byte) bool {
	if len(data) < 9 {
		return false
	}
	frameLen := int(binary.BigEndian.Uint32(data[0:4]))
	return frameLen >= 5 && frameLen+4 == len(data) &&
		binary.BigEndian.Uint32(data[4:8]) == ControlConnID && data[8] == MsgKCPProfile
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
