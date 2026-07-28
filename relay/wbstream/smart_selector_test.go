package wbstream

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
)

type selectorTestTunnel struct{}

func (*selectorTestTunnel) SendData([]byte)            {}
func (*selectorTestTunnel) SetOnData(func([]byte))     {}
func (*selectorTestTunnel) SetOnClose(func())          {}
func (*selectorTestTunnel) Reconfigure(fps, batch int) {}

func TestSmartSelectorPrefersDCBeforeGraceExpires(t *testing.T) {
	video := &selectorTestTunnel{}
	dc := &selectorTestTunnel{}
	var selections []smartSelection
	selector := newSmartTransportSelector(time.Hour, func(selection smartSelection) {
		selections = append(selections, selection)
	})
	defer selector.stop()

	selector.videoReady(video)
	selector.dcReady(dc)

	if len(selections) != 1 {
		t.Fatalf("selections=%d, want 1", len(selections))
	}
	if selections[0].tunnel != dc || selections[0].kind != smartTransportDC || selections[0].reason != "dc-ready" {
		t.Fatalf("unexpected selection: %#v", selections[0])
	}
}

func TestSmartSelectorUsesVideoAfterGraceExpires(t *testing.T) {
	video := &selectorTestTunnel{}
	var selections []smartSelection
	selector := newSmartTransportSelector(time.Hour, func(selection smartSelection) {
		selections = append(selections, selection)
	})
	defer selector.stop()

	selector.videoReady(video)
	selector.expireVideoGrace()

	if len(selections) != 1 {
		t.Fatalf("selections=%d, want 1", len(selections))
	}
	if selections[0].tunnel != video || selections[0].kind != smartTransportVideo || selections[0].reason != "dc-timeout" {
		t.Fatalf("unexpected selection: %#v", selections[0])
	}
}

func TestSmartSelectorFailsOverFromDCToPreparedVideo(t *testing.T) {
	video := &selectorTestTunnel{}
	dc := &selectorTestTunnel{}
	var selections []smartSelection
	selector := newSmartTransportSelector(time.Hour, func(selection smartSelection) {
		selections = append(selections, selection)
	})
	defer selector.stop()

	selector.videoReady(video)
	selector.dcReady(dc)
	selector.dcClosed(dc)

	if len(selections) != 2 {
		t.Fatalf("selections=%d, want 2", len(selections))
	}
	if selections[1].tunnel != video || selections[1].kind != smartTransportVideo || selections[1].reason != "dc-closed" {
		t.Fatalf("unexpected failover: %#v", selections[1])
	}
}

func TestSmartSelectorUsesVideoWhenDCClosesBeforeVideoIsReady(t *testing.T) {
	video := &selectorTestTunnel{}
	dc := &selectorTestTunnel{}
	var selections []smartSelection
	selector := newSmartTransportSelector(time.Hour, func(selection smartSelection) {
		selections = append(selections, selection)
	})
	defer selector.stop()

	selector.dcReady(dc)
	selector.dcClosed(dc)
	selector.videoReady(video)

	if len(selections) != 2 {
		t.Fatalf("selections=%d, want 2", len(selections))
	}
	if selections[1].tunnel != video || selections[1].reason != "dc-unavailable" {
		t.Fatalf("unexpected delayed failover: %#v", selections[1])
	}
}

func TestSmartSelectorDoesNotSwitchToLateDC(t *testing.T) {
	video := &selectorTestTunnel{}
	dc := &selectorTestTunnel{}
	var selections []smartSelection
	selector := newSmartTransportSelector(time.Hour, func(selection smartSelection) {
		selections = append(selections, selection)
	})
	defer selector.stop()

	selector.videoReady(video)
	selector.expireVideoGrace()
	selector.dcReady(dc)

	if len(selections) != 1 || selections[0].tunnel != video {
		t.Fatalf("late DC changed selection: %#v", selections)
	}
}

func TestSmartSelectorRealTimerSelectsVideo(t *testing.T) {
	video := &selectorTestTunnel{}
	selected := make(chan smartSelection, 1)
	selector := newSmartTransportSelector(time.Millisecond, func(selection smartSelection) {
		selected <- selection
	})
	defer selector.stop()

	selector.videoReady(video)
	select {
	case selection := <-selected:
		if selection.tunnel != video || selection.reason != "dc-timeout" {
			t.Fatalf("unexpected timer selection: %#v", selection)
		}
	case <-time.After(time.Second):
		t.Fatal("smart grace timer did not select Video")
	}
}

func TestSmartSelectorSelectionFiresOnceUnderConcurrentReadyCallbacks(t *testing.T) {
	dc := &selectorTestTunnel{}
	var mu sync.Mutex
	selections := 0
	selector := newSmartTransportSelector(time.Hour, func(smartSelection) {
		mu.Lock()
		selections++
		mu.Unlock()
	})
	defer selector.stop()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			selector.dcReady(dc)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if selections != 1 {
		t.Fatalf("selections=%d, want 1", selections)
	}
}

func TestServerRearmsAutoDetectionWhenActiveDCCloses(t *testing.T) {
	dc := &tunnel.DCTunnel{}
	session := NewSession(SessionConfig{LogFn: func(string, ...any) {}})
	session.mu.Lock()
	session.tunFired = true
	session.activeTunnel = dc
	session.dctun = dc
	session.mu.Unlock()

	session.onDCTunnelClosed(dc)

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.tunFired || session.activeTunnel != nil {
		t.Fatalf("server auto-detect was not rearmed: fired=%v active=%T", session.tunFired, session.activeTunnel)
	}
}

func TestServerIncomingVideoPayloadReplacesActiveDC(t *testing.T) {
	session := NewSession(SessionConfig{LogFn: func(string, ...any) {}})
	defer session.stopTunnels()
	dc := &tunnel.DCTunnel{}
	video := tunnel.NewMultiTrackTunnel(nil)
	session.mu.Lock()
	session.tunFired = true
	session.activeTunnel = dc
	session.dctun = dc
	session.vp8tun = video
	session.mu.Unlock()
	selected := make(chan tunnel.DataTunnel, 1)
	session.OnConnected = func(candidate tunnel.DataTunnel) { selected <- candidate }

	session.activate(video, []byte{1, 2, 3})

	select {
	case candidate := <-selected:
		if _, ok := candidate.(*tunnel.KCPTunnel); !ok {
			t.Fatalf("replacement=%T, want KCP Video", candidate)
		}
	case <-time.After(time.Second):
		t.Fatal("incoming Video payload did not replace active DC")
	}
}

func TestSmartSessionFailsOverWhenDCReceivesNoPeerPayload(t *testing.T) {
	selected := make(chan tunnel.DataTunnel, 2)
	var logsMu sync.Mutex
	var logs []string
	session := NewSession(SessionConfig{
		TunnelMode:        TunnelModeSmart,
		SmartDCGrace:      time.Hour,
		SmartDCValidation: 10 * time.Millisecond,
		LogFn: func(format string, args ...any) {
			logsMu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			logsMu.Unlock()
		},
	})
	defer session.stopTunnels()
	session.OnConnected = func(candidate tunnel.DataTunnel) { selected <- candidate }
	video := &selectorTestTunnel{}
	dc := &tunnel.DCTunnel{}
	dc.SetOnInternalClose(func() { session.onDCTunnelClosed(dc) })

	session.smart.videoReady(video)
	session.smart.dcReady(dc)
	if first := <-selected; first != dc {
		t.Fatalf("first selection=%T, want DC", first)
	}
	select {
	case second := <-selected:
		if second != video {
			t.Fatalf("failover selection=%T, want Video candidate", second)
		}
	case <-time.After(time.Second):
		t.Fatal("DC without inbound payload did not fail over")
	}
	logsMu.Lock()
	joined := strings.Join(logs, "\n")
	logsMu.Unlock()
	if !strings.Contains(joined, "reason=no-inbound-data") || !strings.Contains(joined, "reason=dc-no-inbound-data") {
		t.Fatalf("missing health/failover diagnostics:\n%s", joined)
	}
}

var _ tunnel.DataTunnel = (*selectorTestTunnel)(nil)
