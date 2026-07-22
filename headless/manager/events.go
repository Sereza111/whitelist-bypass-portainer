package main

import (
	"sync"
	"time"
)

type panelEvent struct {
	ID        uint64    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Reference string    `json:"reference,omitempty"`
}

type eventLog struct {
	mu     sync.Mutex
	events []panelEvent
	max    int
	nextID uint64
}

func newEventLog(max int) *eventLog {
	if max < 1 {
		max = 200
	}
	return &eventLog{max: max}
}

func (log *eventLog) add(level, kind, message, reference string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.nextID++
	log.events = append(log.events, panelEvent{
		ID: log.nextID, Timestamp: time.Now().UTC(), Level: level,
		Kind: kind, Message: message, Reference: reference,
	})
	if len(log.events) > log.max {
		log.events = append([]panelEvent(nil), log.events[len(log.events)-log.max:]...)
	}
}

func (log *eventLog) list(limit int) []panelEvent {
	log.mu.Lock()
	defer log.mu.Unlock()
	if limit < 1 || limit > len(log.events) {
		limit = len(log.events)
	}
	start := len(log.events) - limit
	result := append([]panelEvent(nil), log.events[start:]...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
