package tunnel

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	fairFlowQueueBytes   = 256 * 1024
	fairTotalQueueBytes  = 8 * 1024 * 1024
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
	send       func([]byte)
	generation atomic.Uint64

	scheduledFrames   uint64
	maxQueuedBytes    uint64
	queueWaitNanos    uint64
	maxQueueWaitNanos uint64
}

func newFairSender(send func([]byte)) *fairSender {
	s := &fairSender{flows: make(map[uint32]*fairFlow), send: send}
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
	flow := s.flows[connID]
	if flow == nil {
		flow = &fairFlow{}
		s.flows[connID] = flow
	}
	for !s.stopped && (flow.bytes+len(frame) > fairFlowQueueBytes || s.bytes+len(frame) > fairTotalQueueBytes) {
		s.cond.Wait()
	}
	if s.stopped {
		return false
	}
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

func (s *fairSender) Reset() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	s.generation.Add(1)
	s.flows = make(map[uint32]*fairFlow)
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
