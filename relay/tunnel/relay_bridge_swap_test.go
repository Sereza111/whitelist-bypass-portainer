package tunnel

import (
	"sync"
	"testing"
)

type swapTestTunnel struct {
	mu      sync.Mutex
	onData  func([]byte)
	onClose func()
}

func (*swapTestTunnel) SendData([]byte)             {}
func (*swapTestTunnel) Reconfigure(int, int)        {}
func (t *swapTestTunnel) SetOnData(fn func([]byte)) { t.mu.Lock(); t.onData = fn; t.mu.Unlock() }
func (t *swapTestTunnel) SetOnClose(fn func())      { t.mu.Lock(); t.onClose = fn; t.mu.Unlock() }
func (t *swapTestTunnel) close() {
	t.mu.Lock()
	fn := t.onClose
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func TestRelayBridgeSwapDetachesOldCarrierAndUpdatesReadSize(t *testing.T) {
	oldTunnel := &swapTestTunnel{}
	newTunnel := &swapTestTunnel{}
	bridge := NewRelayBridge(oldTunnel, "joiner", 32768, func(string, ...any) {})
	defer bridge.Close()

	bridge.SwapTunnelWithReadBuf(newTunnel, 1126)
	oldTunnel.close()

	if bridge.closed.Load() {
		t.Fatal("old carrier close closed the bridge after swap")
	}
	if got := bridge.readBuf.Load(); got != 1126 {
		t.Fatalf("read buffer=%d, want 1126", got)
	}
	bridge.handshakeMu.Lock()
	maxPayload := bridge.localHello.MaxCarrierPayload
	bridge.handshakeMu.Unlock()
	if maxPayload != 1126 {
		t.Fatalf("handshake max payload=%d, want 1126", maxPayload)
	}

	newTunnel.close()
	if !bridge.closed.Load() {
		t.Fatal("active carrier close did not close the non-persistent bridge")
	}
}
