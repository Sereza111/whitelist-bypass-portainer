package wbstream

import "testing"

func TestClampTrackCount(t *testing.T) {
	for input, want := range map[int]int{-10: 1, 0: 1, 1: 1, 4: 4, 5: 4, 100: 4} {
		if got := ClampTrackCount(input); got != want {
			t.Fatalf("ClampTrackCount(%d)=%d, want %d", input, got, want)
		}
	}
}
