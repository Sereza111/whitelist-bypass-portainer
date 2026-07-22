package tunnel

import (
	"bytes"
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
