package tunnel

import (
	"sync/atomic"
	"time"
)

const defaultMetricsInterval = 10 * time.Second

type TunnelMetrics struct {
	Kind                 string `json:"kind"`
	SentBytes            uint64 `json:"sentBytes"`
	ReceivedBytes        uint64 `json:"receivedBytes"`
	SentFrames           uint64 `json:"sentFrames"`
	ReceivedFrames       uint64 `json:"receivedFrames"`
	QueueDepth           int    `json:"queueDepth"`
	QueueCapacity        int    `json:"queueCapacity"`
	MaxQueueDepth        uint64 `json:"maxQueueDepth"`
	SendWaitNanos        uint64 `json:"sendWaitNanos"`
	KCPInputSegments     uint64 `json:"kcpInputSegments,omitempty"`
	KCPOutputSegments    uint64 `json:"kcpOutputSegments,omitempty"`
	KCPDroppedSegments   uint64 `json:"kcpDroppedSegments,omitempty"`
	KCPWaitSnd           int    `json:"kcpWaitSnd,omitempty"`
	KCPBackpressureNanos uint64 `json:"kcpBackpressureNanos,omitempty"`
	KCPOutputQueueDepth  int    `json:"kcpOutputQueueDepth,omitempty"`
	KCPOutputQueueCap    int    `json:"kcpOutputQueueCapacity,omitempty"`
	KCPStallRecoveries   uint64 `json:"kcpStallRecoveries,omitempty"`
	KCPLastInputAgeNanos uint64 `json:"kcpLastInputAgeNanos,omitempty"`
	TrackCount           int    `json:"trackCount"`
}

type RelayMetrics struct {
	Timestamp           time.Time     `json:"timestamp"`
	Uptime              time.Duration `json:"uptime"`
	Mode                string        `json:"mode"`
	SentBytes           uint64        `json:"sentBytes"`
	ReceivedBytes       uint64        `json:"receivedBytes"`
	SentFrames          uint64        `json:"sentFrames"`
	ReceivedFrames      uint64        `json:"receivedFrames"`
	SentControlFrames   uint64        `json:"sentControlFrames"`
	RecvControlFrames   uint64        `json:"receivedControlFrames"`
	SendWaitNanos       uint64        `json:"sendWaitNanos"`
	MaxSendWaitNanos    uint64        `json:"maxSendWaitNanos"`
	ActiveTCP           int           `json:"activeTcp"`
	ActiveUDP           int           `json:"activeUdp"`
	NegotiatedWire      uint16        `json:"negotiatedWire"`
	NegotiatedCaps      uint64        `json:"negotiatedCapabilities"`
	LegacyCompatibility bool          `json:"legacyCompatibility"`
	DNSQueries          uint64        `json:"dnsQueries"`
	DNSRetryFrames      uint64        `json:"dnsRetryFrames"`
	Tunnel              TunnelMetrics `json:"tunnel"`
}

type tunnelMetricsProvider interface {
	TunnelMetrics() TunnelMetrics
}

func (rb *RelayBridge) MetricsSnapshot() RelayMetrics {
	tcpConns, udpConns, _ := rb.Stats()
	snapshot := RelayMetrics{
		Timestamp:         time.Now().UTC(),
		Uptime:            time.Since(rb.startedAt),
		Mode:              rb.mode,
		SentBytes:         rb.sentBytes.Load(),
		ReceivedBytes:     rb.receivedBytes.Load(),
		SentFrames:        rb.sentFrames.Load(),
		ReceivedFrames:    rb.receivedFrames.Load(),
		SentControlFrames: rb.sentControlFrames.Load(),
		RecvControlFrames: rb.recvControlFrames.Load(),
		SendWaitNanos:     rb.sendWaitNanos.Load(),
		MaxSendWaitNanos:  rb.maxSendWaitNanos.Load(),
		ActiveTCP:         tcpConns,
		ActiveUDP:         udpConns,
		DNSQueries:        rb.dnsQueries.Load(),
		DNSRetryFrames:    rb.dnsRetryFrames.Load(),
	}
	if result, ok := rb.NegotiatedHandshake(); ok {
		snapshot.NegotiatedWire = result.SelectedWireVersion
		snapshot.NegotiatedCaps = result.Capabilities
		snapshot.LegacyCompatibility = result.LegacyFallback
	}
	if provider, ok := rb.currentTunnel().(tunnelMetricsProvider); ok {
		snapshot.Tunnel = provider.TunnelMetrics()
	}
	return snapshot
}

func (rb *RelayBridge) metricsLoop() {
	ticker := time.NewTicker(defaultMetricsInterval)
	defer ticker.Stop()
	lastAt := time.Now()
	var lastSent, lastReceived uint64
	for {
		select {
		case <-rb.metricsStop:
			return
		case <-ticker.C:
			m := rb.MetricsSnapshot()
			now := time.Now()
			elapsed := now.Sub(lastAt).Seconds()
			txKbps := float64(m.SentBytes-lastSent) * 8 / elapsed / 1000
			rxKbps := float64(m.ReceivedBytes-lastReceived) * 8 / elapsed / 1000
			lastAt, lastSent, lastReceived = now, m.SentBytes, m.ReceivedBytes
			rb.logFn("METRICS mode=%s uptime=%s tx_bytes=%d rx_bytes=%d tx_kbps=%.1f rx_kbps=%.1f tx_frames=%d rx_frames=%d control_tx=%d control_rx=%d send_wait_ms=%.2f max_send_wait_ms=%.2f tcp=%d udp=%d dns_queries=%d dns_retries=%d wire=%d caps=0x%x legacy=%t tunnel=%s tunnel_tx=%d tunnel_rx=%d queue=%d/%d queue_max=%d kcp_wait_snd=%d kcp_out_queue=%d/%d kcp_dropped=%d kcp_backpressure_ms=%.2f kcp_stalls=%d kcp_input_idle_ms=%.0f",
				m.Mode, m.Uptime.Round(time.Second), m.SentBytes, m.ReceivedBytes,
				txKbps, rxKbps,
				m.SentFrames, m.ReceivedFrames, m.SentControlFrames, m.RecvControlFrames,
				float64(m.SendWaitNanos)/float64(time.Millisecond),
				float64(m.MaxSendWaitNanos)/float64(time.Millisecond),
				m.ActiveTCP, m.ActiveUDP, m.DNSQueries, m.DNSRetryFrames,
				m.NegotiatedWire, m.NegotiatedCaps,
				m.LegacyCompatibility, m.Tunnel.Kind, m.Tunnel.SentBytes,
				m.Tunnel.ReceivedBytes, m.Tunnel.QueueDepth, m.Tunnel.QueueCapacity,
				m.Tunnel.MaxQueueDepth, m.Tunnel.KCPWaitSnd,
				m.Tunnel.KCPOutputQueueDepth, m.Tunnel.KCPOutputQueueCap,
				m.Tunnel.KCPDroppedSegments,
				float64(m.Tunnel.KCPBackpressureNanos)/float64(time.Millisecond),
				m.Tunnel.KCPStallRecoveries,
				float64(m.Tunnel.KCPLastInputAgeNanos)/float64(time.Millisecond))
		}
	}
}

func updateAtomicMax(target *atomic.Uint64, value uint64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}
