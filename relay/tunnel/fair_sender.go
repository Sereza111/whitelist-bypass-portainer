package tunnel

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Keep only a short staging queue above KCP. Field alpha.11 traces at a
	// 1 Mbps carrier accumulated 4.2 MiB here and made frames wait up to 38s,
	// even though the carrier and ACK stream were still healthy. TCP already
	// provides producer backpressure; buffering seconds of encrypted payload
	// here only turns congestion into unusable loaded latency.
	fairFlowQueueBytes   = 64 * 1024
	fairTotalQueueBytes  = 512 * 1024
	fairSchedulerQuantum = 4 * AdaptiveKCPRelayReadBuf
)

type queuedRelayFrame struct {
	data       []byte
	queuedAt   time.Time
	generation uint64
}

type fairFlow struct {
	frames  []queuedRelayFrame
	bytes   int
	deficit int
}

type fairSenderSnapshot struct {
	ActiveFlows       int
	QueuedFrames      int
	QueuedBytes       int
	ScheduledFrames   uint64
	MaxQueuedBytes    uint64
	QueueWaitNanos    uint64
	MaxQueueWaitNanos uint64
}

type fairSender struct {
	mu         sync.Mutex
	sendMu     sync.RWMutex
	cond       *sync.Cond
	flows      map[uint32]*fairFlow
	active     []uint32
	cursor     int
	bytes      int
	frames     int
	stopped    bool
	canceled   map[uint32]struct{}
	send       func([]byte)
	generation atomic.Uint64

	scheduledFrames   uint64
	maxQueuedBytes    uint64
	queueWaitNanos    uint64
	maxQueueWaitNanos uint64
}

func newFairSender(send func([]byte)) *fairSender {
	s := &fairSender{
		flows:    make(map[uint32]*fairFlow),
		canceled: make(map[uint32]struct{}),
		send:     send,
	}
	s.cond = sync.NewCond(&s.mu)
	go s.run()
	return s
}

func (s *fairSender) Enqueue(connID uint32, frame []byte) bool {
	if len(frame) == 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.stopped {
			return false
		}
		if _, canceled := s.canceled[connID]; canceled {
			return false
		}
		flow := s.flows[connID]
		if flow == nil {
			flow = &fairFlow{}
			s.flows[connID] = flow
		}
		if flow.bytes+len(frame) <= fairFlowQueueBytes && s.bytes+len(frame) <= fairTotalQueueBytes {
			if len(flow.frames) == 0 {
				flow.deficit = fairSchedulerQuantum
				s.active = append(s.active, connID)
			}
			flow.frames = append(flow.frames, queuedRelayFrame{data: frame, queuedAt: time.Now(), generation: s.generation.Load()})
			flow.bytes += len(frame)
			s.bytes += len(frame)
			s.frames++
			if uint64(s.bytes) > s.maxQueuedBytes {
				s.maxQueuedBytes = uint64(s.bytes)
			}
			s.cond.Signal()
			return true
		}
		s.cond.Wait()
	}
}

// CancelFlow drops frames which have not entered KCP after the peer has
// explicitly closed the logical connection. Connection IDs are monotonic for
// a RelayBridge lifetime, so rejecting later enqueues for the same ID is safe
// and prevents a racing socket reader from rebuilding a stale backlog.
func (s *fairSender) CancelFlow(connID uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceled[connID] = struct{}{}
	if flow := s.flows[connID]; flow != nil {
		s.bytes -= flow.bytes
		s.frames -= len(flow.frames)
		s.removeActiveLocked(connID)
	}
	s.cond.Broadcast()
}

func (s *fairSender) Reset() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	s.generation.Add(1)
	s.flows = make(map[uint32]*fairFlow)
	s.canceled = make(map[uint32]struct{})
	s.active = nil
	s.cursor = 0
	s.bytes = 0
	s.frames = 0
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *fairSender) Stop() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	s.generation.Add(1)
	s.stopped = true
	s.flows = make(map[uint32]*fairFlow)
	s.canceled = make(map[uint32]struct{})
	s.active = nil
	s.bytes = 0
	s.frames = 0
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *fairSender) Snapshot() fairSenderSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fairSenderSnapshot{
		ActiveFlows: len(s.active), QueuedFrames: s.frames, QueuedBytes: s.bytes,
		ScheduledFrames: s.scheduledFrames,
		MaxQueuedBytes:  s.maxQueuedBytes, QueueWaitNanos: s.queueWaitNanos,
		MaxQueueWaitNanos: s.maxQueueWaitNanos,
	}
}

func (s *fairSender) run() {
	for {
		s.mu.Lock()
		for !s.stopped && len(s.active) == 0 {
			s.cond.Wait()
		}
		if s.stopped {
			s.mu.Unlock()
			return
		}
		if s.cursor >= len(s.active) {
			s.cursor = 0
		}
		connID := s.active[s.cursor]
		flow := s.flows[connID]
		if flow == nil || len(flow.frames) == 0 {
			s.removeActiveLocked(connID)
			s.mu.Unlock()
			continue
		}
		if flow.deficit < len(flow.frames[0].data) {
			flow.deficit += fairSchedulerQuantum
			s.cursor = (s.cursor + 1) % len(s.active)
			s.mu.Unlock()
			continue
		}
		item := flow.frames[0]
		flow.frames[0] = queuedRelayFrame{}
		flow.frames = flow.frames[1:]
		flow.bytes -= len(item.data)
		flow.deficit -= len(item.data)
		s.bytes -= len(item.data)
		s.frames--
		s.scheduledFrames++
		waited := uint64(time.Since(item.queuedAt))
		s.queueWaitNanos += waited
		if waited > s.maxQueueWaitNanos {
			s.maxQueueWaitNanos = waited
		}
		if len(flow.frames) == 0 {
			s.removeActiveLocked(connID)
		} else if flow.deficit < len(flow.frames[0].data) {
			s.cursor = (s.cursor + 1) % len(s.active)
		}
		s.cond.Broadcast()
		s.mu.Unlock()
		s.sendMu.RLock()
		if item.generation == s.generation.Load() {
			s.send(item.data)
		}
		s.sendMu.RUnlock()
	}
}

func (s *fairSender) removeActiveLocked(connID uint32) {
	delete(s.flows, connID)
	for index, activeID := range s.active {
		if activeID != connID {
			continue
		}
		s.active = append(s.active[:index], s.active[index+1:]...)
		if len(s.active) == 0 {
			s.cursor = 0
		} else if s.cursor >= len(s.active) {
			s.cursor = 0
		}
		return
	}
}
