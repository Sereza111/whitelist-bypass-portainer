package tunnel

import (
	"encoding/binary"
	"testing"
)

func TestMultiTrackRawMuxKeepsConnectionAffinity(t *testing.T) {
	m := NewMultiTrackTunnel(nil)
	frame := make([]byte, 9)
	binary.BigEndian.PutUint32(frame[4:8], 7)

	for i := 0; i < 20; i++ {
		if got := m.selectTrackIndexLocked(frame, 4); got != 3 {
			t.Fatalf("raw conn affinity changed: got track %d, want 3", got)
		}
	}
}

func TestMultiTrackKCPRoundRobinUsesEveryTrack(t *testing.T) {
	m := NewMultiTrackTunnel(nil)
	m.EnableRoundRobinStriping()
	counts := make([]int, 4)
	for i := 0; i < 400; i++ {
		counts[m.selectTrackIndexLocked([]byte("same KCP packet"), len(counts))]++
	}
	for track, count := range counts {
		if count != 100 {
			t.Fatalf("track %d received %d packets, want 100; distribution=%v", track, count, counts)
		}
	}
}

func TestMultiTrackRoundRobinHandlesDynamicTrackCount(t *testing.T) {
	m := NewMultiTrackTunnel(nil)
	m.EnableRoundRobinStriping()
	for i := 0; i < 9; i++ {
		if got := m.selectTrackIndexLocked(nil, 1); got != 0 {
			t.Fatalf("single track selection=%d, want 0", got)
		}
	}
	for i := 0; i < 40; i++ {
		got := m.selectTrackIndexLocked(nil, 3)
		if got < 0 || got >= 3 {
			t.Fatalf("dynamic track selection=%d outside [0,3)", got)
		}
	}
}

type stripingProbeTunnel struct {
	enabled bool
}

func (s *stripingProbeTunnel) EnableRoundRobinStriping() { s.enabled = true }
func (*stripingProbeTunnel) SendData([]byte)             {}
func (*stripingProbeTunnel) SetOnData(func([]byte))      {}
func (*stripingProbeTunnel) SetOnClose(func())           {}
func (*stripingProbeTunnel) Reconfigure(int, int)        {}

func TestNewKCPTunnelEnablesInnerStriping(t *testing.T) {
	inner := &stripingProbeTunnel{}
	kcp := NewKCPTunnel(inner, func(string, ...any) {})
	defer kcp.Stop()
	if !inner.enabled {
		t.Fatal("KCP wrapper did not enable packet striping on its inner tunnel")
	}
}
