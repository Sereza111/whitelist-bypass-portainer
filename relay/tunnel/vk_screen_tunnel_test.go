package tunnel

import (
	"encoding/binary"
	"testing"
)

func TestSymmetricScreenRawMuxKeepsConnectionAffinity(t *testing.T) {
	s := &SymmetricScreenTunnel{}
	s.trackCount.Store(2)
	frame := make([]byte, 9)
	binary.BigEndian.PutUint32(frame[4:8], 7)
	for i := 0; i < 20; i++ {
		if got := s.selectTrack(frame); got != 1 {
			t.Fatalf("raw conn affinity changed: got %d, want 1", got)
		}
	}
}

func TestSymmetricScreenKCPRoundRobinUsesBothTracks(t *testing.T) {
	s := &SymmetricScreenTunnel{}
	s.trackCount.Store(2)
	s.EnableRoundRobinStriping()
	counts := [2]int{}
	for i := 0; i < 100; i++ {
		counts[s.selectTrack([]byte("KCP segment"))]++
	}
	if counts != [2]int{50, 50} {
		t.Fatalf("unexpected distribution: %v", counts)
	}
}
