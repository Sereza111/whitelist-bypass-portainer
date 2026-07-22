package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerWatchdogReportsFailureOnce(t *testing.T) {
	p := NewP2PHandler(nil)
	failures := make(chan string, 2)
	p.SetHealthCallbacks(nil, func(reason string) { failures <- reason })
	p.armPeerWatchdog("offer timeout", 10*time.Millisecond)

	select {
	case reason := <-failures:
		if reason != "offer timeout" {
			t.Fatalf("unexpected failure reason %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("peer watchdog did not fire")
	}

	p.reportPeerFailure("duplicate state failure")
	select {
	case reason := <-failures:
		t.Fatalf("duplicate failure reported: %q", reason)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestTunnelRelayClosesSessionOnce(t *testing.T) {
	relay := NewTunnelRelay()
	var closes atomic.Int32
	relay.SetSessionClose(func() { closes.Add(1) })
	relay.Close()
	relay.Close()
	if got := closes.Load(); got != 1 {
		t.Fatalf("session close calls = %d, want 1", got)
	}
}

func TestConnectedPeerCancelsWatchdog(t *testing.T) {
	p := NewP2PHandler(nil)
	connected := make(chan struct{}, 1)
	failures := make(chan string, 1)
	p.SetHealthCallbacks(func() { connected <- struct{}{} }, func(reason string) { failures <- reason })
	p.armPeerWatchdog("should be cancelled", 20*time.Millisecond)
	p.OnConnectionState("connected")

	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("connected callback was not called")
	}
	select {
	case reason := <-failures:
		t.Fatalf("watchdog fired after connection: %q", reason)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPeerRecoveryEscalatesAndResets(t *testing.T) {
	b := &Bridge{}
	for i := 1; i < maxPeerRecoveryFailures; i++ {
		b.notePeerFailure("offer timeout")
		if err := b.peerRecoveryError(); err != nil {
			t.Fatalf("recovery escalated on attempt %d: %v", i, err)
		}
	}
	b.notePeerFailure("offer timeout")
	if err := b.peerRecoveryError(); err == nil {
		t.Fatal("expected exhausted peer recovery error")
	}
	b.notePeerConnected()
	if err := b.peerRecoveryError(); err != nil {
		t.Fatalf("healthy connection did not reset recovery counter: %v", err)
	}
}
