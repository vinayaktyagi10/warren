package latency

import (
	"testing"
	"time"
)

func TestSummaryNearestRank(t *testing.T) {
	var r Recorder
	// Recorded out of order on purpose: Observe must not assume sortedness.
	for i := 100; i >= 1; i-- {
		r.Observe(time.Duration(i) * time.Millisecond)
	}
	got := r.Summary()

	want := []struct {
		name string
		have time.Duration
		want time.Duration
	}{
		{"p50", got.P50, 50 * time.Millisecond},
		{"p95", got.P95, 95 * time.Millisecond},
		{"p99", got.P99, 99 * time.Millisecond},
		{"max", got.Max, 100 * time.Millisecond},
		{"mean", got.Mean, 50500 * time.Microsecond},
	}
	for _, c := range want {
		if c.have != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.have, c.want)
		}
	}
	if got.N != 100 {
		t.Errorf("N = %d, want 100", got.N)
	}
}

func TestSummaryEmpty(t *testing.T) {
	var r Recorder
	if got := r.Summary(); got != (Summary{}) {
		t.Errorf("empty recorder summarised to %+v, want zero", got)
	}
}

func TestSummarySingle(t *testing.T) {
	var r Recorder
	r.Observe(7 * time.Second)
	got := r.Summary()
	if got.P50 != 7*time.Second || got.P99 != 7*time.Second || got.Max != 7*time.Second {
		t.Errorf("single sample summarised to %+v", got)
	}
}

// With ten samples there is no 95th element to point at. Nearest-rank rounds up
// rather than interpolating, so p95 and p99 both land on the largest sample.
// This is the honest behaviour: a tail percentile over ten points is the max.
func TestSummarySmallSampleTailIsMax(t *testing.T) {
	var r Recorder
	for i := 1; i <= 10; i++ {
		r.Observe(time.Duration(i) * time.Millisecond)
	}
	got := r.Summary()
	if got.P95 != 10*time.Millisecond || got.P99 != 10*time.Millisecond {
		t.Errorf("p95=%v p99=%v, want both 10ms", got.P95, got.P99)
	}
	if got.P50 != 5*time.Millisecond {
		t.Errorf("p50 = %v, want 5ms", got.P50)
	}
}

func TestSummaryIsRepeatable(t *testing.T) {
	var r Recorder
	for i := 1; i <= 50; i++ {
		r.Observe(time.Duration(i) * time.Millisecond)
	}
	first := r.Summary()
	r.Observe(1 * time.Millisecond)
	second := r.Summary()
	if first.N != 50 || second.N != 51 {
		t.Fatalf("summarising must not consume samples: N was %d then %d", first.N, second.N)
	}
}
