package detect

import (
	"time"

	"github.com/vinayaktyagi10/warren/internal/latency"
)

// MeasureLatency assembles the three latency answers for one detection pass.
//
// Score latency is timed per candidate across the whole scoring path a deployed
// system would run — building the feature vector, standardising it, and applying
// the coefficients — not just the dot product. Timing the dot product alone would
// produce a smaller number that no operator ever experiences.
//
// The timer's own floor is measured on the same machine in the same run, because
// this path is fast enough that the instrument is a visible share of the reading
// and a percentile that sits at the floor is a measurement of the clock.
func MeasureLatency(led *Ledger, candidates []Candidate, windows []latency.Window,
	cfg Config, wall time.Duration, rank *Ranker) latency.Report {

	var score latency.Recorder
	sink := 0.0
	for _, c := range candidates {
		t0 := time.Now()
		p := rank.Score(c)
		score.Observe(time.Since(t0))
		sink += p // keep the work from being optimised away
	}
	_ = sink

	arrivals := make([]time.Time, len(led.Txns))
	for i, t := range led.Txns {
		arrivals[i] = t.TS
	}
	width := time.Duration(cfg.WindowHours) * time.Hour
	stride := time.Duration(cfg.StrideHours) * time.Hour
	all, uncovered := latency.TimeToDecision(arrivals, windows, width)

	// Steady state begins once every window that would have covered a transfer
	// actually exists. The earliest such window opens at t-width+stride, so a
	// transfer is only measured on equal terms once that window is inside the
	// run. Before then the ledger, not the detector, is what made it wait.
	steady := arrivals
	warmup := 0
	if len(windows) > 0 {
		from := windows[0].Start.Add(width - stride)
		for i, t := range arrivals {
			if !t.Before(from) {
				steady = arrivals[i:]
				warmup = i
				break
			}
		}
	}
	decision, _ := latency.TimeToDecision(steady, windows, width)

	return latency.Report{
		Score:       score.Summary(),
		Amortised:   latency.PerTransferCost(windows),
		Decision:    decision,
		DecisionAll: all,
		Warmup:      warmup,
		Uncovered:   uncovered,
		Floor:       latency.TimerFloor(20000),
		Transfers:   len(led.Txns),
		Candidates:  len(candidates),
		Windows:     len(windows),
		Wall:        wall,
	}
}
