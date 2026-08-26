// Package latency measures how long WARREN takes, and is careful about which
// question that answers.
//
// The India BFSI literature sets a sub-100ms bar for fraud decisioning. That bar
// was written for per-transaction scoring, and WARREN's unit of decision is not
// a transaction — it is a group of accounts observed over a 72-hour window. So
// "latency" here splits into three different numbers that must not be quoted for
// one another:
//
//	Score       a candidate's feature vector to a ranked score. Microseconds.
//	            Real, but it excludes everything that made the vector exist.
//	Amortised   detection work divided by the transfers it covered. A throughput
//	            figure, reported per-window so it has a distribution at all.
//	Decision    a transfer lands; when does a bounded decision covering it exist?
//	            Hours, by construction, because the window has to close first.
//
// Only the first clears 100ms, and quoting only the first would be the flattering
// framing rather than the honest one. The third is the number an operator lives
// with, and the argument for it is not that it is fast but that per-transaction
// scoring at 10ms cannot see the thing being detected at all.
//
// Percentiles are nearest-rank over the retained samples, not interpolated and
// not bucketed. At this project's sample counts — tens of thousands per run — an
// exact sort costs under a megabyte, and a sketch would put approximation error
// into the one number that exists to be defended.
package latency

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Recorder collects durations. It keeps every sample rather than summarising as
// it goes, so the percentiles are exact and can be recomputed at any point.
//
// The zero value is ready to use. It is not safe for concurrent use; the callers
// here record from a single goroutine.
type Recorder struct {
	samples []time.Duration
	total   time.Duration
}

// Observe records one measurement.
func (r *Recorder) Observe(d time.Duration) {
	r.samples = append(r.samples, d)
	r.total += d
}

// Since records the time elapsed since t. Call as defer r.Since(time.Now()) only
// where the measured work is long enough that the deferred call is noise.
func (r *Recorder) Since(t time.Time) { r.Observe(time.Since(t)) }

// N is how many samples have been recorded.
func (r *Recorder) N() int { return len(r.samples) }

// Summary is the distribution of what was recorded.
type Summary struct {
	N             int
	Mean          time.Duration
	P50, P95, P99 time.Duration
	Min, Max      time.Duration
}

// Summary computes the distribution. It sorts a copy, so it can be called more
// than once and does not disturb the recording order.
func (r *Recorder) Summary() Summary {
	if len(r.samples) == 0 {
		return Summary{}
	}
	s := make([]time.Duration, len(r.samples))
	copy(s, r.samples)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })

	return Summary{
		N:    len(s),
		Mean: r.total / time.Duration(len(s)),
		P50:  quantile(s, 0.50),
		P95:  quantile(s, 0.95),
		P99:  quantile(s, 0.99),
		Min:  s[0],
		Max:  s[len(s)-1],
	}
}

// SummaryOf computes the distribution of an already-collected slice.
func SummaryOf(d []time.Duration) Summary {
	var r Recorder
	for _, v := range d {
		r.Observe(v)
	}
	return r.Summary()
}

// quantile returns the nearest-rank quantile of a sorted slice: the smallest
// sample at or above the q share of the data. It never interpolates between two
// samples, so every number reported is a measurement that actually happened.
func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(float64(len(sorted))*q + 0.999999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// TimerFloor measures the cost of taking a measurement: the elapsed time this
// machine reports for doing nothing between two clock reads.
//
// It exists because the score path is fast enough that the instrument is a
// meaningful share of the reading. A p99 of 400ns against a floor of 60ns is a
// real number; a p99 of 80ns against the same floor is mostly the clock, and
// reporting it as though it were the model would be measuring the wrong thing.
func TimerFloor(n int) Summary {
	if n <= 0 {
		n = 10000
	}
	var r Recorder
	for i := 0; i < n; i++ {
		t := time.Now()
		r.Observe(time.Since(t))
	}
	return r.Summary()
}

// Window is one closure of the detection window: when it opened, how many
// transfers it covered, and how long the detector took on it.
type Window struct {
	Start   time.Time
	Txns    int
	Process time.Duration
}

// PerTransferCost is the amortised view: how much detection work each transfer
// in a window cost. Reported as a distribution across windows, because a single
// total-over-total figure hides that a busy window and a quiet one are not the
// same machine.
func PerTransferCost(windows []Window) Summary {
	var r Recorder
	for _, w := range windows {
		if w.Txns == 0 {
			continue
		}
		r.Observe(w.Process / time.Duration(w.Txns))
	}
	return r.Summary()
}

// TimeToDecision measures, for every transfer, how long it waits before a
// bounded decision covering it can exist: until the earliest window containing
// it closes, plus the time the detector then spent on that window.
//
// It returns the distribution and the number of transfers no window covered.
// Those are not a rounding error to be quietly dropped — a transfer outside
// every window never receives a ring-level decision at all, and the count is the
// honest footnote to the percentiles.
//
// Windows must be sorted by Start, which is how the detector emits them.
func TimeToDecision(arrivals []time.Time, windows []Window, width time.Duration) (Summary, int) {
	var r Recorder
	uncovered := 0
	for _, t := range arrivals {
		// The earliest window that can still contain t is the first one starting
		// after t-width. If that window also starts at or before t, it covers t
		// and is the first to close over it.
		i := sort.Search(len(windows), func(i int) bool {
			return windows[i].Start.After(t.Add(-width))
		})
		if i == len(windows) || windows[i].Start.After(t) {
			uncovered++
			continue
		}
		w := windows[i]
		r.Observe(w.Start.Add(width).Sub(t) + w.Process)
	}
	return r.Summary(), uncovered
}

// Report is the three measurements together. They are always presented together
// on purpose: any one of them alone is a misleading answer to "how fast is it".
type Report struct {
	Score     Summary // feature vector to ranked score
	Amortised Summary // detection work per transfer, per window

	// Decision is arrival to a bounded decision, over the transfers that had a
	// full set of overlapping windows available to them. DecisionAll is the same
	// measurement over every transfer in the run, warm-up included.
	//
	// They differ because a batch run over a finite ledger has a cold start: the
	// windows that would have covered the first transfers begin before the data
	// does, so those transfers wait for a later window than a running system
	// would have made them wait. Reporting only DecisionAll would blame the
	// system for an artefact of the harness; reporting only Decision would hide
	// that the first day of any deployment genuinely is slower. Both are here.
	Decision    Summary
	DecisionAll Summary
	Warmup      int // transfers excluded from Decision as cold start
	Uncovered   int // transfers no window contained at all

	Floor      Summary // the timer's own cost, for calibrating Score
	Transfers  int
	Candidates int
	Windows    int
	Wall       time.Duration // whole detection pass, start to finish
}

func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-28s %10s %10s %10s %10s %10s\n", "measurement", "n", "p50", "p95", "p99", "max")
	row := func(name string, s Summary) {
		fmt.Fprintf(&b, "%-28s %10d %10s %10s %10s %10s\n", name, s.N,
			short(s.P50), short(s.P95), short(s.P99), short(s.Max))
	}
	row("score per candidate", r.Score)
	row("  timer floor", r.Floor)
	row("detection per transfer", r.Amortised)
	row("arrival to decision", r.Decision)
	row("  including cold start", r.DecisionAll)

	fmt.Fprintf(&b, "\n%d transfers, %d candidates, %d windows, %s wall\n",
		r.Transfers, r.Candidates, r.Windows, r.Wall.Round(time.Millisecond))
	if r.Warmup > 0 {
		fmt.Fprintf(&b, "%d transfers arrived before the windows overlapped fully and are excluded from the steady-state row\n", r.Warmup)
	}
	if r.Uncovered > 0 {
		fmt.Fprintf(&b, "%d transfers fell outside every window and never receive a ring-level decision\n", r.Uncovered)
	}
	fmt.Fprint(&b, "the 100ms bar is a per-transaction scoring bar. Scoring clears it by five orders of\n",
		"magnitude; arrival to decision does not and cannot, because the evidence does not exist\n",
		"until the window closes. That is the cost of seeing the ring rather than the payment.\n")
	return b.String()
}

// short renders a duration at a readable scale. time.Duration's own String is
// exact but prints things like 1.000342ms, which is precision nobody asked for
// in a table meant to be read at a glance.
func short(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.1fµs", float64(d.Nanoseconds())/1e3)
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Nanoseconds())/1e6)
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.1fm", d.Minutes())
	default:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
}

// Short is short, exported for the console to render the same way stdout does.
func Short(d time.Duration) string { return short(d) }
