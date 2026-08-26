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
	// the result is a number with nothing to be judged against.
	CeilingValue float64
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

// Replay walks the held-out period forward in window order, decides the alerts
// each window closure raises, imposes the leases those decisions authorise, and
// then checks the transfers that follow against the register.
//
// It is an interception measurement against the recorded ledger, and that is a
// weaker claim than a counterfactual. Two things it deliberately does not model:
// a stopped transfer is not removed from what later windows detect, and an
// operator whose accounts are frozen does not adapt. Both would make the number
// look different, and neither can be simulated honestly from a fixed file — so
// the number is reported as what it is, the value the leases would have sat in
// front of, not the value the ring would ultimately have failed to move.
func Replay(led *detect.Ledger, cands []detect.Candidate, scores []float64,
	cfg detect.Config, rc ReplayConfig, decide Decider) (ReplayResult, *Register) {

	res := ReplayResult{RingsHit: make(map[int32]bool)}
	if len(led.Txns) == 0 || len(cands) == 0 {
		return res, &Register{}
	}

	width := time.Duration(cfg.WindowHours) * time.Hour

	// The alert budget becomes a score threshold, so enforcement is measured at
	// the same operating point as the published precision and recall.
	threshold := budgetThreshold(scores, rc.Budget)

	// Candidates grouped by the window that raised them, windows in time order.
	byWindow := make(map[time.Time][]int)
	for i, c := range cands {
		byWindow[c.Window] = append(byWindow[c.Window], i)
	}
	closures := make([]time.Time, 0, len(byWindow))
	for ws := range byWindow {
		closures = append(closures, ws.Add(width))
	}
	sort.Slice(closures, func(i, j int) bool { return closures[i].Before(closures[j]) })

	reg := &Register{}
	frozen := make(map[int32]bool)
	watched := make(map[int32]bool)

	res.From = closures[0]
	res.To = led.Txns[len(led.Txns)-1].TS
	res.Runway = res.To.Sub(res.From)

	// Walk transfers and closures together. Both are in time order, so a single
	// forward pass over each is enough: no transfer is ever revisited and the
	// register is only ever consulted at instants at or after the leases in it
	// were imposed, which is what makes lazy expiry safe.
	txnIdx := sort.Search(len(led.Txns), func(i int) bool {
		return !led.Txns[i].TS.Before(res.From)
	})
	for _, t := range led.Txns[:txnIdx] {
		res.Unprotected++
		if t.IsLaundering {
			res.ValueLaunderingUnprot += t.Amount
		}
	}

	for ci, C := range closures {
		for _, idx := range byWindow[C.Add(-width)] {
			if scores[idx] < threshold {
				continue
			}
			c := cands[idx]
			ev := agent.Evidence{
				RingID: idx, Typology: c.Typology,
				Accounts: c.Features.Accounts, Txns: c.Features.Txns,
				TotalAmount: c.Features.TotalAmount, MaxAmount: c.Features.MaxAmount,
				Conservation: c.Features.Conservation, PassThrough: c.Features.PassThroughRatio,
				SpanHours: c.Features.SpanHours, Score: scores[idx], WindowStart: c.Window,
			}
			a := decide(ev)
			res.Decisions++
			switch a.Action {
			case agent.ActionBlock:
				res.Blocks++
			case agent.ActionHold:
				res.Holds++
			default:
				res.Allows++
			}

			leases, bounds := Restrictions(a, c, C, rc.Limits)
			if len(bounds) > 0 {
				res.Declined++
			}
			for _, l := range leases {
				reg.Impose(l)
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
		if ci+1 < len(closures) {
			next = closures[ci+1]
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
	res.CeilingValue = MeasureCeilingIn(led, cfg, res.From, res.To.Add(time.Nanosecond)).StoppableValue
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
	return b.String()
}
