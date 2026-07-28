package wbstream

import (
	"sync"
	"time"

	"whitelist-bypass/relay/tunnel"
)

const defaultSmartDCGrace = 1500 * time.Millisecond

type smartTransportKind string

const (
	smartTransportDC    smartTransportKind = "data-channel"
	smartTransportVideo smartTransportKind = "kcp-video"
)

type smartSelection struct {
	tunnel tunnel.DataTunnel
	kind   smartTransportKind
	reason string
}

// smartTransportSelector is deliberately independent of WebRTC callbacks so
// readiness, timeout and failover races can be tested deterministically. It
// selects DC when it becomes usable during the grace period, otherwise Video.
// Once Video wins it stays selected; only a selected DC may fail over to Video.
type smartTransportSelector struct {
	mu       sync.Mutex
	grace    time.Duration
	onSelect func(smartSelection)
	timer    *time.Timer

	dc            tunnel.DataTunnel
	video         tunnel.DataTunnel
	dcUnavailable bool
	selected      smartTransportKind
	stopped       bool
}

func newSmartTransportSelector(grace time.Duration, onSelect func(smartSelection)) *smartTransportSelector {
	if grace <= 0 {
		grace = defaultSmartDCGrace
	}
	return &smartTransportSelector{grace: grace, onSelect: onSelect}
}

func (s *smartTransportSelector) videoReady(video tunnel.DataTunnel) {
	var selection *smartSelection
	s.mu.Lock()
	if !s.stopped && s.video == nil {
		s.video = video
		if s.selected == "" {
			if s.dcUnavailable {
				selection = s.selectLocked(video, smartTransportVideo, "dc-unavailable")
			} else if s.dc == nil && s.timer == nil {
				s.timer = time.AfterFunc(s.grace, s.expireVideoGrace)
			}
		}
	}
	s.mu.Unlock()
	s.emit(selection)
}

func (s *smartTransportSelector) dcReady(dc tunnel.DataTunnel) {
	var selection *smartSelection
	s.mu.Lock()
	if !s.stopped && !s.dcUnavailable && s.selected == "" {
		s.dc = dc
		selection = s.selectLocked(dc, smartTransportDC, "dc-ready")
	}
	s.mu.Unlock()
	s.emit(selection)
}

func (s *smartTransportSelector) dcClosed(dc tunnel.DataTunnel) {
	s.dcFailed(dc, "dc-closed")
}

func (s *smartTransportSelector) dcFailed(dc tunnel.DataTunnel, reason string) {
	var selection *smartSelection
	s.mu.Lock()
	if !s.stopped && s.dc == dc {
		s.dcUnavailable = true
		if s.selected == smartTransportDC {
			s.selected = ""
			if s.video != nil {
				selection = s.selectLocked(s.video, smartTransportVideo, reason)
			}
		} else if s.selected == "" && s.video != nil {
			selection = s.selectLocked(s.video, smartTransportVideo, reason)
		}
	}
	s.mu.Unlock()
	s.emit(selection)
}

func (s *smartTransportSelector) expireVideoGrace() {
	var selection *smartSelection
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = nil
	if !s.stopped && s.selected == "" && s.video != nil {
		selection = s.selectLocked(s.video, smartTransportVideo, "dc-timeout")
	}
	s.mu.Unlock()
	s.emit(selection)
}

func (s *smartTransportSelector) stop() {
	s.mu.Lock()
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
}

func (s *smartTransportSelector) selectLocked(candidate tunnel.DataTunnel, kind smartTransportKind, reason string) *smartSelection {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.selected = kind
	return &smartSelection{tunnel: candidate, kind: kind, reason: reason}
}

func (s *smartTransportSelector) emit(selection *smartSelection) {
	if selection != nil && s.onSelect != nil {
		s.onSelect(*selection)
	}
}
