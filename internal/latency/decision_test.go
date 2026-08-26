package latency

import (
	"testing"
	"time"
)

func base() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) }

// Windows 72h wide advancing every 24h: whichever transfer you pick, the
// earliest window containing it closes within 24h, because a closure happens
// every stride. The wait is therefore bounded by the stride, not the width.
func TestTimeToDecisionBoundedByStride(t *testing.T) {
	b := base()
	var windows []Window
	for i := 0; i < 30; i++ {
		windows = append(windows, Window{Start: b.Add(time.Duration(i) * 24 * time.Hour)})
	}
	var arrivals []time.Time
	// Sample inside the region every window can reach, hourly.
	for h := 72; h < 96; h++ {
		arrivals = append(arrivals, b.Add(time.Duration(h)*time.Hour))
	}

	got, uncovered := TimeToDecision(arrivals, windows, 72*time.Hour)
	if uncovered != 0 {
		t.Fatalf("uncovered = %d, want 0", uncovered)
	}
	if got.Max > 24*time.Hour {
		t.Errorf("max wait %v exceeds the 24h stride", got.Max)
	}
	if got.Min <= 0 {
		t.Errorf("min wait %v, want positive: a window cannot close before the transfer arrives", got.Min)
	}
}

// A transfer arriving after the last window has closed is never covered. It must
// be counted, not silently averaged away.
func TestTimeToDecisionCountsUncovered(t *testing.T) {
	b := base()
	windows := []Window{{Start: b}}
	arrivals := []time.Time{
		b.Add(-time.Hour),      // before any window opened
		b.Add(time.Hour),       // inside
		b.Add(100 * time.Hour), // past the only window's close
	}
	got, uncovered := TimeToDecision(arrivals, windows, 72*time.Hour)
	if uncovered != 2 {
		t.Errorf("uncovered = %d, want 2", uncovered)
	}
	if got.N != 1 {
		t.Errorf("N = %d, want 1", got.N)
	}
	if want := 71 * time.Hour; got.P50 != want {
		t.Errorf("p50 = %v, want %v", got.P50, want)
	}
}

// The detector's own time on the covering window is part of the wait: a decision
// does not exist the instant the window closes, it exists once the window is
// processed.
func TestTimeToDecisionIncludesProcessing(t *testing.T) {
	b := base()
	windows := []Window{{Start: b, Process: 90 * time.Second}}
	arrivals := []time.Time{b.Add(71 * time.Hour)}

	got, _ := TimeToDecision(arrivals, windows, 72*time.Hour)
	if want := time.Hour + 90*time.Second; got.P50 != want {
		t.Errorf("p50 = %v, want %v", got.P50, want)
	}
}

// Amortised cost is per window, so a busy window and a quiet one are separate
// samples rather than being fused into one average.
func TestPerTransferCost(t *testing.T) {
	windows := []Window{
		{Txns: 100, Process: 100 * time.Millisecond}, // 1ms each
		{Txns: 10, Process: 100 * time.Millisecond},  // 10ms each
		{Txns: 0, Process: time.Second},              // skipped: nothing to divide by
	}
	got := PerTransferCost(windows)
	if got.N != 2 {
		t.Fatalf("N = %d, want 2 (the empty window contributes nothing)", got.N)
	}
	if got.Min != time.Millisecond || got.Max != 10*time.Millisecond {
		t.Errorf("range %v..%v, want 1ms..10ms", got.Min, got.Max)
	}
}
