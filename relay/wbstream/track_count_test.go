package wbstream

import "testing"

func TestClampTrackCount(t *testing.T) {
	for input, want := range map[int]int{-10: 1, 0: 1, 1: 1, 4: 4, 8: 8, 9: 8, 100: 8} {
		if got := ClampTrackCount(input); got != want {
			t.Fatalf("ClampTrackCount(%d)=%d, want %d", input, got, want)
		}
	}
}

func TestDefaultTrackCountUsesWideWBTopology(t *testing.T) {
	if DefaultTrackCount != MaxTrackCount {
		t.Fatalf("DefaultTrackCount=%d, want MaxTrackCount=%d", DefaultTrackCount, MaxTrackCount)
	}
	if DefaultTrackCount != 8 {
		t.Fatalf("DefaultTrackCount=%d, want 8", DefaultTrackCount)
	}
}

func TestWideCarrierAdvertisesFullHD(t *testing.T) {
	if carrierVideoWidth != 1920 || carrierVideoHeight != 1080 {
		t.Fatalf("carrier dimensions=%dx%d, want 1920x1080", carrierVideoWidth, carrierVideoHeight)
	}
}
