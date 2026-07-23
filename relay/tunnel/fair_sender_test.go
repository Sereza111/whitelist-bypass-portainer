package tunnel

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFairSenderServesShortFlowBeforeBulkDrain(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	sent := make(chan byte, 16)
	first := true
	sender := newFairSender(func(frame []byte) {
		if first {
			first = false
			close(entered)
			<-release
		}
		sent <- frame[0]
	})
	defer sender.Stop()

	sender.Enqueue(1, bytes.Repeat([]byte{1}, 1000))
	<-entered
	for index := 0; index < 7; index++ {
		sender.Enqueue(1, bytes.Repeat([]byte{1}, 1000))
	}
	sender.Enqueue(2, bytes.Repeat([]byte{2}, 1000))
	close(release)

	shortIndex := -1
	for index := 0; index < 9; index++ {
		select {
		case flow := <-sent:
			if flow == 2 {
				shortIndex = index
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for fair sender")
		}
	}
	if shortIndex < 0 || shortIndex > 5 {
		t.Fatalf("short flow sent at index %d; bulk flow monopolized scheduler", shortIndex)
	}
}

func TestFairSenderAppliesPerFlowBackpressure(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	sender := newFairSender(func([]byte) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	})
	defer sender.Stop()

	frame := make([]byte, fairFlowQueueBytes/2)
	sender.Enqueue(7, frame)
	<-entered
	sender.Enqueue(7, frame)
	sender.Enqueue(7, frame)
	result := make(chan bool, 1)
	go func() { result <- sender.Enqueue(7, frame) }()

	select {
	case <-result:
		t.Fatal("enqueue bypassed the per-flow byte limit")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("enqueue stopped instead of resuming after drain")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backpressured enqueue did not resume")
	}
}

func TestFairScheduledMessageClasses(t *testing.T) {
	for _, msgType := range []byte{MsgData, MsgClose, MsgUDP, MsgUDPReply} {
		if !isFairScheduledMessage(msgType) {
			t.Fatalf("message type %d did not enter fair scheduler", msgType)
		}
	}
	for _, msgType := range []byte{MsgConnect, MsgConnectOK, MsgDNSQuery, MsgDNSReply, MsgHello} {
		if isFairScheduledMessage(msgType) {
			t.Fatalf("priority/control message type %d entered bulk scheduler", msgType)
		}
	}
}

func TestFairSenderCancelFlowDropsBacklogAndRejectsRacingEnqueue(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	sent := make(chan byte, 4)
	sender := newFairSender(func(frame []byte) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		sent <- frame[0]
	})
	defer sender.Stop()

	if !sender.Enqueue(7, []byte{1}) {
		t.Fatal("initial enqueue failed")
	}
	<-entered
	if !sender.Enqueue(7, []byte{2}) || !sender.Enqueue(8, []byte{8}) {
		t.Fatal("backlog enqueue failed")
	}

	sender.CancelFlow(7)
	if sender.Enqueue(7, []byte{3}) {
		t.Fatal("canceled flow accepted a racing enqueue")
	}
	close(release)

	got := []byte{<-sent, <-sent}
	if !bytes.Equal(got, []byte{1, 8}) {
		t.Fatalf("sent frames=%v, want in-flight frame plus live flow", got)
	}
	snapshot := sender.Snapshot()
	if snapshot.QueuedFrames != 0 || snapshot.QueuedBytes != 0 {
		t.Fatalf("canceled backlog remains queued: %+v", snapshot)
	}
}

func TestFairSenderCancelFlowWakesBlockedProducer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	sender := newFairSender(func([]byte) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	})
	defer sender.Stop()

	frame := make([]byte, fairFlowQueueBytes/2)
	sender.Enqueue(7, frame)
	<-entered
	sender.Enqueue(7, frame)
	sender.Enqueue(7, frame)
	result := make(chan bool, 1)
	go func() { result <- sender.Enqueue(7, frame) }()

	select {
	case <-result:
		t.Fatal("producer did not block at the per-flow limit")
	case <-time.After(50 * time.Millisecond):
	}
	sender.CancelFlow(7)
	select {
	case ok := <-result:
		if ok {
			t.Fatal("blocked producer resumed after its flow was canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not wake blocked producer")
	}
	close(release)
}

func TestCreatorNacksUnknownFlowOnce(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	rb := &RelayBridge{
		logFn: func(format string, args ...any) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	}
	rb.fairSender = newFairSender(func([]byte) {})
	defer rb.fairSender.Stop()

	for range 20 {
		rb.handleCreatorMessage(93, MsgData, make([]byte, 1089))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "NACK once") {
		t.Fatalf("unknown flow logs=%v, want one NACK", logs)
	}
}
