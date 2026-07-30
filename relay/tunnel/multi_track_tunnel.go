package tunnel

import (
	"encoding/binary"
	"sync"
)

type MultiTrackTunnel struct {
	tunnels []*VP8DataTunnel

	mu       sync.Mutex
	onData   func([]byte)
	onClose  func()
	isClosed bool
	fps      int
	batch    int
	// Raw mux frames are pinned by connection id so one TCP flow remains
	// ordered on one carrier. Once KCP wraps this tunnel, the payload here is a
	// KCP packet rather than a mux frame; KCP provides ordering itself and its
	// packets must be striped to use every published video track.
	roundRobinStriping bool
	nextTrack          uint64
}

func NewMultiTrackTunnel(tunnels []*VP8DataTunnel) *MultiTrackTunnel {
	m := &MultiTrackTunnel{tunnels: tunnels}
	for i, tun := range tunnels {
		// Only the cam (index 0) carries the cascade-on-close semantics;
		// screenshare tracks close independently during partial shrink.
		m.wireSubTunnel(tun, i == 0)
	}
	return m
}

func (m *MultiTrackTunnel) wireSubTunnel(tun *VP8DataTunnel, isCamera bool) {
	tun.SetOnData(func(data []byte) {
		m.mu.Lock()
		handler := m.onData
		m.mu.Unlock()
		if handler != nil {
			handler(data)
		}
	})
	if !isCamera {
		// Screenshare close is a partial shrink; it must not cascade-Stop
		// the cam writer or notify the parent that the whole tunnel died.
		return
	}
	tun.SetOnClose(func() {
		m.mu.Lock()
		if m.isClosed {
			m.mu.Unlock()
			return
		}
		m.isClosed = true
		closeHandler := m.onClose
		subTunnels := m.tunnels
		m.mu.Unlock()

		for _, t := range subTunnels {
			t.Stop()
		}
		if closeHandler != nil {
			closeHandler()
		}
	})
}

func (m *MultiTrackTunnel) AddSubTunnel(tun *VP8DataTunnel) {
	m.mu.Lock()
	if m.isClosed {
		m.mu.Unlock()
		tun.Stop()
		return
	}
	m.tunnels = append(m.tunnels, tun)
	fps := m.fps
	batch := m.batch
	m.mu.Unlock()
	m.wireSubTunnel(tun, false)
	if fps > 0 && batch > 0 {
		tun.Start(fps, batch)
	}
}

func (m *MultiTrackTunnel) RemoveLastSubTunnel() *VP8DataTunnel {
	m.mu.Lock()
	if len(m.tunnels) <= 1 {
		m.mu.Unlock()
		return nil
	}
	last := m.tunnels[len(m.tunnels)-1]
	m.tunnels = m.tunnels[:len(m.tunnels)-1]
	m.mu.Unlock()
	last.Stop()
	return last
}

func (m *MultiTrackTunnel) SubTunnelCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tunnels)
}

func (m *MultiTrackTunnel) SendData(data []byte) {
	m.mu.Lock()
	if len(m.tunnels) == 0 {
		m.mu.Unlock()
		return
	}
	idx := m.selectTrackIndexLocked(data, len(m.tunnels))
	tun := m.tunnels[idx]
	m.mu.Unlock()
	tun.SendData(data)
}

// EnableRoundRobinStriping is called by a reliability wrapper whose packets
// can safely arrive out of order. It deliberately remains opt-in: callers
// sending raw mux frames retain per-connection affinity.
func (m *MultiTrackTunnel) EnableRoundRobinStriping() {
	m.mu.Lock()
	m.roundRobinStriping = true
	m.nextTrack = 0
	m.mu.Unlock()
}

func (m *MultiTrackTunnel) selectTrackIndexLocked(data []byte, trackCount int) int {
	if trackCount <= 1 {
		return 0
	}
	if m.roundRobinStriping {
		idx := int(m.nextTrack % uint64(trackCount))
		m.nextTrack++
		return idx
	}
	var connID uint32
	if len(data) >= 8 {
		connID = binary.BigEndian.Uint32(data[4:8])
	}
	return int(connID % uint32(trackCount))
}

func (m *MultiTrackTunnel) SetOnData(fn func([]byte)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onData = fn
}

func (m *MultiTrackTunnel) SetOnClose(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onClose = fn
}

func (m *MultiTrackTunnel) Reconfigure(fps, batch int) {
	m.mu.Lock()
	m.fps = fps
	m.batch = batch
	tunnels := m.tunnels
	m.mu.Unlock()
	for _, tun := range tunnels {
		tun.Reconfigure(fps, batch)
	}
}

func (m *MultiTrackTunnel) Start(fps, batch int) {
	m.mu.Lock()
	m.fps = fps
	m.batch = batch
	tunnels := m.tunnels
	m.mu.Unlock()
	for _, tun := range tunnels {
		tun.Start(fps, batch)
	}
}

func (m *MultiTrackTunnel) Stop() {
	m.mu.Lock()
	if m.isClosed {
		m.mu.Unlock()
		return
	}
	m.isClosed = true
	tunnels := m.tunnels
	m.mu.Unlock()
	for _, tun := range tunnels {
		tun.Stop()
	}
}

func (m *MultiTrackTunnel) HandleFrame(frame []byte) {
	m.HandleFrameForTrack(0, frame)
}

func (m *MultiTrackTunnel) HandleFrameForTrack(trackIndex int, frame []byte) {
	m.mu.Lock()
	var selected *VP8DataTunnel
	if len(m.tunnels) > 0 {
		if trackIndex < 0 {
			trackIndex = 0
		}
		// A reconnect can subscribe replacement tracks before old TrackRemote
		// readers finish. Fold the monotonically assigned slot back onto the
		// current carrier set instead of collapsing every replacement onto 0.
		trackIndex %= len(m.tunnels)
		selected = m.tunnels[trackIndex]
	}
	m.mu.Unlock()
	if selected != nil {
		selected.HandleFrame(frame)
	}
}

func (m *MultiTrackTunnel) TunnelMetrics() TunnelMetrics {
	m.mu.Lock()
	tunnels := append([]*VP8DataTunnel(nil), m.tunnels...)
	m.mu.Unlock()
	metrics := TunnelMetrics{Kind: "multi-vp8", TrackCount: len(tunnels)}
	for _, tun := range tunnels {
		item := tun.TunnelMetrics()
		metrics.TrackSentBytes = append(metrics.TrackSentBytes, item.SentBytes)
		metrics.TrackReceivedBytes = append(metrics.TrackReceivedBytes, item.ReceivedBytes)
		metrics.TrackSentFrames = append(metrics.TrackSentFrames, item.SentFrames)
		metrics.TrackReceivedFrames = append(metrics.TrackReceivedFrames, item.ReceivedFrames)
		metrics.TrackQueueDepths = append(metrics.TrackQueueDepths, item.QueueDepth)
		metrics.TrackWriteNanos = append(metrics.TrackWriteNanos, item.TrackWriteNanos...)
		metrics.TrackMaxWriteNanos = append(metrics.TrackMaxWriteNanos, item.TrackMaxWriteNanos...)
		metrics.TrackWriteErrors = append(metrics.TrackWriteErrors, item.TrackWriteErrors...)
		metrics.SentBytes += item.SentBytes
		metrics.ReceivedBytes += item.ReceivedBytes
		metrics.SentFrames += item.SentFrames
		metrics.ReceivedFrames += item.ReceivedFrames
		metrics.QueueDepth += item.QueueDepth
		metrics.QueueCapacity += item.QueueCapacity
		metrics.MaxQueueDepth += item.MaxQueueDepth
		metrics.SendWaitNanos += item.SendWaitNanos
	}
	return metrics
}
