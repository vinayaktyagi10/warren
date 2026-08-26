package enforce

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/detect"
)

// Decider turns evidence into a bounded decision. Replay takes one rather than
// calling the model directly so the measurement can be reproducible: the number
// published for enforcement should not move because a provider returned 503.
type Decider func(ev agent.Evidence) agent.Assessment

// ThresholdDecider is a deterministic stand-in for the model, for measurement
// only. It proposes a block wherever the ranker is confident enough that
// blocking is even available, and then hands the proposal to the same policy
// that governs the model's proposals.
//
// It is not a fallback tier and must never be one. RuleAssessor deliberately
// refuses to block on a degraded path; this exists so the enforcement layer can
// be measured without a network call, and it says so in its source name. What a
// block *does* is what is being measured here — which rings get blocked is the
// model's job, and swapping this for the real chain changes the former, not the
// latter.
func ThresholdDecider(p agent.Policy) Decider {
	return func(ev agent.Evidence) agent.Assessment {
		prop := agent.Proposal{
			Action:     agent.ActionAllow,
			Confidence: 1 - ev.Score,
			Rationale:  fmt.Sprintf("Deterministic replay decider: ranker scored %.3f.", ev.Score),
		}
		if ev.Score >= p.BlockMinScore {
			prop.Action = agent.ActionBlock
			prop.Confidence = ev.Score
		} else if ev.Score >= p.HoldMinScore {
			prop.Action = agent.ActionHold
			prop.Confidence = ev.Score
		}
		return p.Apply(ev, prop, "threshold-replay")
	}
}

// Source is one detection pass feeding the register: its geometry, the
// candidates it raised, what it scored them, how many of them the team agrees to
// work, and how long a lease it may impose.
//
// Several sources share one register on purpose. That is the whole dual-geometry
// argument in one data structure: a fast pass and a slow pass are not two systems
// with two answers, they are two views of the same accounts arriving at different
// times, and the register is where the later view gets to strengthen the earlier
// one.
type Source struct {
	Name       string
	Cfg        detect.Config
	Candidates []detect.Candidate
	Scores     []float64
	Budget     int
	Limits     Limits

	// Enforces says whether this pass may lease into the register. A pass that
	// detects without enforcing still raises alerts an analyst works — it simply
	// does not get to hold anyone's money, which is the right arrangement for a
	// geometry whose alarm arrives after the money has already moved.
	Enforces bool
}

// SourceStat is what one pass contributed.
type SourceStat struct {
	Name      string
	Window    int
	Stride    int
	Decisions int
	Blocks    int
	Holds     int
	Declined  int

	// Escalations counts leases that strengthened or lengthened one already in
	// force. On the slow pass this is confirmation arriving: accounts a thin
	// 24-hour read had frozen briefly, held longer because 72 hours of evidence
	// agreed.
	Escalations int
	New         int
}

// ReplayConfig parameterises the measurement.
type ReplayConfig struct {
	// Budget is how many alerts the team agrees to work over the whole replay
	// period. It sets the score threshold above which a candidate is decided at
	// all, so the enforcement result can be read against the same alert-budget
	// curve as precision and recall.
	Budget int

	Limits Limits
	Policy agent.Policy
}

// ReplayResult is what enforcement actually achieved against the recorded
// ledger.
type ReplayResult struct {
	Windows   int
	Decisions int
	Blocks    int
	Holds     int
	Allows    int
	Declined  int // blast-radius refusals: rings too large to act on automatically

	AccountsFrozen  int
	AccountsWatched int

	// From is the first instant enforcement could apply: nothing can be stopped
	// before the first window has closed and been decided. To is the end of the
	// ledger.
	From, To time.Time

	// Runway is how much ledger remains after the first alert can be acted on.
	// It is the binding constraint on this whole layer and it is easy to hide:
	// a held-out period shorter than the detection window produces a negative
	// runway and a silent zero everywhere else, which reads as "enforcement
	// caught nothing" when the truth is "enforcement was never given a chance".
	Runway time.Duration

	// Unprotected is what passed between the start of the replay and the first
	// closure, when nothing had been decided yet. It is the cold start of the
	// action layer and it is counted rather than trimmed away.
	Unprotected           int
	ValueLaunderingUnprot float64

	Checked           int // transfers inside the enforceable period
	LaunderingIn      int
	ValueIn           float64
	ValueLaunderingIn float64

	Stopped                int
	StoppedLaundering      int
	StoppedLegit           int
	ValueStopped           float64
	ValueLaunderingStopped float64
	ValueLegitStopped      float64

	RingsHit map[int32]bool // labelled rings whose transfers were stopped

	// CeilingValue is the laundering value a perfect oracle detector could have
	// stopped inside this same runway, at this same window geometry. Without it
	// the result is a number with nothing to be judged against. Where several
	// passes run together the ceiling is the narrowest one's, because the
	// earliest any account can be leased is the moment the fastest pass closes.
	CeilingValue float64

	PerSource []SourceStat
}

// OfCeiling is the share of the achievable interception that was achieved. This
// is the honest score for the action layer: interception against the whole
// period conflates the layer being weak with the layer being given no time.
func (r ReplayResult) OfCeiling() float64 {
	if r.CeilingValue == 0 {
		return 0
	}
	return r.ValueLaunderingStopped / r.CeilingValue
}

// Precision is the share of stopped value that was actually laundering. This is
// the number that decides whether the action layer is deployable: stopping a
// criminal's money is worth very little if it means stopping nine ordinary
// people's alongside.
func (r ReplayResult) Precision() float64 {
	if r.ValueStopped == 0 {
		return 0
	}
	return r.ValueLaunderingStopped / r.ValueStopped
}

// Interception is the share of laundering value in the enforceable period that
// enforcement actually stopped.
func (r ReplayResult) Interception() float64 {
	if r.ValueLaunderingIn == 0 {
		return 0
	}
	return r.ValueLaunderingStopped / r.ValueLaunderingIn
}

// Replay measures a single detection pass. It is ReplayMulti with one source,
// kept because most of the analysis compares one geometry against another.
func Replay(led *detect.Ledger, cands []detect.Candidate, scores []float64,
	cfg detect.Config, rc ReplayConfig, decide Decider) (ReplayResult, *Register) {

	lim := rc.Limits
	if lim.Pass == "" {
		lim.Pass = fmt.Sprintf("%dh", cfg.WindowHours)
	}
	return ReplayMulti(led, []Source{{
		Name: lim.Pass, Cfg: cfg, Candidates: cands, Scores: scores,
		Budget: rc.Budget, Limits: lim, Enforces: true,
	}}, decide)
}

// ReplayMulti walks the held-out period forward through every pass's window
// closures in one merged timeline, decides the alerts each closure raises,
// imposes the leases those decisions authorise into a shared register, and then
// checks the transfers that follow against it.
//
// Merging the timelines rather than replaying each pass separately is the point.
// Run apart, a fast pass and a slow pass produce two numbers that cannot be
// added, because they lease the same accounts and would double-count what they
// stopped. Run together against one register, a lease that arrives second either
// extends the one already there or is ignored, and the result is what the
// combined system actually achieves.
//
// It is an interception measurement against the recorded ledger, and that is a
// weaker claim than a counterfactual. Two things it deliberately does not model:
// a stopped transfer is not removed from what later windows detect, and an
// operator whose accounts are frozen does not adapt. Both would make the number
// look different, and neither can be simulated honestly from a fixed file — so
// the number is reported as what it is, the value the leases would have sat in
// front of, not the value the ring would ultimately have failed to move.
func ReplayMulti(led *detect.Ledger, sources []Source, decide Decider) (ReplayResult, *Register) {
	res := ReplayResult{RingsHit: make(map[int32]bool)}
	if len(led.Txns) == 0 || len(sources) == 0 {
		return res, &Register{}
	}

	// One event per window closure per pass, in time order. Ties break on the
	// narrower window first: when a fast and a slow pass close at the same
	// instant, the thin lease should already be in place for the thick one to
	// extend, which is the escalation the design exists to demonstrate.
	type event struct {
		at    time.Time
		src   int
		cands []int
	}
	var events []event
	thresholds := make([]float64, len(sources))
	stats := make([]SourceStat, len(sources))

	for si, src := range sources {
		thresholds[si] = budgetThreshold(src.Scores, src.Budget)
		stats[si] = SourceStat{Name: src.Name, Window: src.Cfg.WindowHours, Stride: src.Cfg.StrideHours}
		width := time.Duration(src.Cfg.WindowHours) * time.Hour

		byWindow := make(map[time.Time][]int)
		for i, c := range src.Candidates {
			byWindow[c.Window] = append(byWindow[c.Window], i)
		}
		for ws, idxs := range byWindow {
			events = append(events, event{at: ws.Add(width), src: si, cands: idxs})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		return sources[events[i].src].Cfg.WindowHours < sources[events[j].src].Cfg.WindowHours
	})

	reg := &Register{}
	frozen := make(map[int32]bool)
	watched := make(map[int32]bool)

	res.From = events[0].at
	res.To = led.Txns[len(led.Txns)-1].TS
	res.Runway = res.To.Sub(res.From)

	txnIdx := sort.Search(len(led.Txns), func(i int) bool {
		return !led.Txns[i].TS.Before(res.From)
	})
	for _, t := range led.Txns[:txnIdx] {
		res.Unprotected++
		if t.IsLaundering {
			res.ValueLaunderingUnprot += t.Amount
		}
	}

	for ei, e := range events {
		src := sources[e.src]
		for _, idx := range e.cands {
			if src.Scores[idx] < thresholds[e.src] {
				continue
			}
			c := src.Candidates[idx]
			ev := agent.Evidence{
				RingID: idx, Typology: c.Typology,
				Accounts: c.Features.Accounts, Txns: c.Features.Txns,
				TotalAmount: c.Features.TotalAmount, MaxAmount: c.Features.MaxAmount,
				Conservation: c.Features.Conservation, PassThrough: c.Features.PassThroughRatio,
				SpanHours: c.Features.SpanHours, Score: src.Scores[idx], WindowStart: c.Window,
			}
			a := decide(ev)
			res.Decisions++
			stats[e.src].Decisions++
			switch a.Action {
			case agent.ActionBlock:
				res.Blocks++
				stats[e.src].Blocks++
			case agent.ActionHold:
				res.Holds++
				stats[e.src].Holds++
			default:
				res.Allows++
			}

			if !src.Enforces {
				continue
			}
			leases, bounds := Restrictions(a, c, e.at, src.Limits)
			if len(bounds) > 0 {
				res.Declined++
				stats[e.src].Declined++
			}
			for _, l := range leases {
				switch reg.Impose(l) {
				case ImposeNew:
					stats[e.src].New++
				case ImposeExtended:
					stats[e.src].Escalations++
				}
				if l.Tier.stops() {
					frozen[l.Account] = true
				} else {
					watched[l.Account] = true
				}
			}
		}
		res.Windows++

		// Enforce over the transfers between this closure and the next. Leases
		// imposed later cannot reach backwards, which is the whole point.
		next := res.To.Add(time.Nanosecond)
		if ei+1 < len(events) {
			next = events[ei+1].at
		}
		for ; txnIdx < len(led.Txns); txnIdx++ {
			t := led.Txns[txnIdx]
			if !t.TS.Before(next) {
				break
			}
			res.Checked++
			res.ValueIn += t.Amount
			if t.IsLaundering {
				res.LaunderingIn++
				res.ValueLaunderingIn += t.Amount
			}
			if _, stopped := reg.Stopped(t.From, t.TS); !stopped {
				continue
			}
			res.Stopped++
			res.ValueStopped += t.Amount
			if t.IsLaundering {
				res.StoppedLaundering++
				res.ValueLaunderingStopped += t.Amount
				if t.PatternID != 0 {
					res.RingsHit[t.PatternID] = true
				}
			} else {
				res.StoppedLegit++
				res.ValueLegitStopped += t.Amount
			}
		}
	}

	res.AccountsFrozen = len(frozen)
	res.AccountsWatched = len(watched)
	res.PerSource = stats

	// The ceiling belongs to the fastest pass: it sets the earliest instant any
	// account in the system can be under a lease.
	fastest := sources[0].Cfg
	seen := false
	for _, src := range sources {
		if !src.Enforces {
			continue
		}
		if !seen || src.Cfg.WindowHours < fastest.WindowHours {
			fastest, seen = src.Cfg, true
		}
	}
	res.CeilingValue = MeasureCeilingIn(led, fastest, res.From, res.To.Add(time.Nanosecond)).StoppableValue
	return res, reg
}

// budgetThreshold returns the score a candidate must reach to be inside the top
// n. A budget at or past the whole set decides everything.
func budgetThreshold(scores []float64, n int) float64 {
	if n <= 0 || n >= len(scores) {
		return 0
	}
	s := make([]float64, len(scores))
	copy(s, scores)
	sort.Sort(sort.Reverse(sort.Float64Slice(s)))
	return s[n-1]
}

func (r ReplayResult) String() string {
	var b strings.Builder
	if r.Runway <= 0 {
		fmt.Fprintf(&b, "NO ENFORCEMENT RUNWAY. The first alert can be acted on at %s, and the ledger\n"+
			"ends at %s — %s earlier. Every window that raises an alert closes after the data\n"+
			"runs out, so nothing below could have been stopped by any system, however good.\n"+
			"Shorten the detection window or widen the held-out period.\n\n",
			r.From.Format("2006-01-02 15:04"), r.To.Format("2006-01-02 15:04"), (-r.Runway).Round(time.Hour))
	}
	fmt.Fprintf(&b, "enforceable period %s to %s (%s runway), %d window closures\n",
		r.From.Format("2006-01-02 15:04"), r.To.Format("2006-01-02 15:04"),
		r.Runway.Round(time.Hour), r.Windows)
	fmt.Fprintf(&b, "decisions %d: %d block, %d hold, %d allow", r.Decisions, r.Blocks, r.Holds, r.Allows)
	if r.Declined > 0 {
		fmt.Fprintf(&b, " (%d rings too large to act on automatically)", r.Declined)
	}
	fmt.Fprintf(&b, "\naccounts leased: %d frozen, %d watched\n\n", r.AccountsFrozen, r.AccountsWatched)

	fmt.Fprintf(&b, "%-30s %12s %20s\n", "", "transfers", "value")
	fmt.Fprintf(&b, "%-30s %12d %20.0f\n", "in the enforceable period", r.Checked, r.ValueIn)
	fmt.Fprintf(&b, "%-30s %12d %20.0f\n", "  of which laundering", r.LaunderingIn, r.ValueLaunderingIn)
	fmt.Fprintf(&b, "%-30s %12d %20.0f\n", "stopped", r.Stopped, r.ValueStopped)
	fmt.Fprintf(&b, "%-30s %12d %20.0f\n", "  laundering", r.StoppedLaundering, r.ValueLaunderingStopped)
	fmt.Fprintf(&b, "%-30s %12d %20.0f\n", "  legitimate", r.StoppedLegit, r.ValueLegitStopped)

	fmt.Fprintf(&b, "%-30s %12d %20.0f\n", "passed before the 1st alert", r.Unprotected, r.ValueLaunderingUnprot)

	fmt.Fprintf(&b, "\nprecision of the action  %.2f%% of stopped value was laundering\n", 100*r.Precision())
	fmt.Fprintf(&b, "interception             %.2f%% of laundering value in the period was stopped\n", 100*r.Interception())
	fmt.Fprintf(&b, "against the ceiling      %.2f%% of the %.0f a perfect oracle could have stopped here\n",
		100*r.OfCeiling(), r.CeilingValue)
	fmt.Fprintf(&b, "labelled rings hit       %d\n", len(r.RingsHit))

	if len(r.PerSource) > 1 {
		fmt.Fprintf(&b, "\n%-14s %8s %10s %8s %8s %10s %12s\n",
			"pass", "window", "decisions", "block", "hold", "new leases", "escalations")
		for _, st := range r.PerSource {
			fmt.Fprintf(&b, "%-14s %7dh %10d %8d %8d %10d %12d\n",
				st.Name, st.Window, st.Decisions, st.Blocks, st.Holds, st.New, st.Escalations)
		}
		fmt.Fprint(&b, "escalations are leases that strengthened one already in force: a slower pass\n"+
			"confirming accounts a faster one had frozen briefly on thinner evidence\n")
	}
	return b.String()
}
