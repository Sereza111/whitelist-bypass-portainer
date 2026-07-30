package tunnel

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestPerTrackKbpsUsesIntervalDeltas(t *testing.T) {
	got := perTrackKbps([]uint64{3000, 5000}, []uint64{1000, 2500}, 2)
	want := []float64{8, 10}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("perTrackKbps() = %v, want %v", got, want)
	}
}

func TestPerTrackAverageMillisUsesWrittenFrameDeltas(t *testing.T) {
	got := perTrackAverageMillis(
		[]uint64{uint64(9 * time.Millisecond)},
		[]uint64{uint64(3 * time.Millisecond)},
		[]uint64{14},
		[]uint64{10},
	)
	if len(got) != 1 || math.Abs(got[0]-1.5) > 0.001 {
		t.Fatalf("perTrackAverageMillis() = %v, want [1.5]", got)
	}
}
