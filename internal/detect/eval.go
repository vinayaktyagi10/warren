package detect

import (
	"fmt"
	"sort"
	"strings"
)

// Matching thresholds. Both are stated rather than tuned, because a detector
// that picks its own definition of "found it" can report any number it likes.
const (
	// coverThreshold: a labelled ring counts as found when a single candidate
	// contains at least this share of its transfers. Half is the point past
	// which an investigator opening the alert would see the ring rather than a
	// fragment of it.
	coverThreshold = 0.5

	// purityThreshold: a candidate counts as a true positive when at least this
	// share of what it flags belongs to one labelled ring. Without it, a
	// candidate that swallowed a ring plus ten thousand innocent transfers
	// would score as a success.
	purityThreshold = 0.5
)

// Report is the full accounting for one detector run.
type Report struct {
	Candidates      int
	FlaggedTxns     int
	FlaggedAccounts int

	// Ring level: did we surface the actual rings, and is what we surfaced real?
	LabelledRings int
	RingsFound    int
	CandidatesTP  int

	// Transaction level, against the is_laundering flag.
	TxnTP, TxnFP, TxnFN int

	// The cost of being wrong, in the currency the business cares about.
	FalsePositiveValue float64
	FalsePositiveTxns  int

	PerTypology map[string]*TypologyStat
}

// TypologyStat reports recall for one ring shape. Averaging across shapes would
// hide a structure the detector never finds at all.
type TypologyStat struct {
	Typology string
	Labelled int
	Found    int
}

// Evaluate scores candidates against the labelled rings and the laundering flag.
func Evaluate(led *Ledger, candidates []Candidate, typologies map[int32]string) *Report {
	rep := &Report{
		Candidates:  len(candidates),
		PerTypology: make(map[string]*TypologyStat),
	}

	// Ground truth: which transfers belong to which labelled ring.
	ringTxns := make(map[int32]map[int32]bool)
	txnByID := make(map[int32]*Txn, len(led.Txns))
	for i := range led.Txns {
		t := &led.Txns[i]
		txnByID[t.ID] = t
		if t.PatternID != 0 {
			if ringTxns[t.PatternID] == nil {
				ringTxns[t.PatternID] = make(map[int32]bool)
			}
			ringTxns[t.PatternID][t.ID] = true
		}
	}
	rep.LabelledRings = len(ringTxns)
	for id := range ringTxns {
		ty := typologies[id]
		if rep.PerTypology[ty] == nil {
			rep.PerTypology[ty] = &TypologyStat{Typology: ty}
		}
		rep.PerTypology[ty].Labelled++
	}

	// Overlap between every candidate and every ring it touches, computed once.
	type overlapKey struct {
		cand int
		ring int32
	}
	overlap := make(map[overlapKey]int)
	flagged := make(map[int32]bool)
	accounts := make(map[int32]bool)

	for ci, c := range candidates {
		for _, id := range c.TxnIDs {
			flagged[id] = true
			if t, ok := txnByID[id]; ok && t.PatternID != 0 {
				overlap[overlapKey{ci, t.PatternID}]++
			}
		}
		for _, a := range c.Accounts {
			accounts[a] = true
		}
	}
	rep.FlaggedTxns = len(flagged)
	rep.FlaggedAccounts = len(accounts)

	// A ring is found when one candidate covers enough of it.
	found := make(map[int32]bool)
	// A candidate is a true positive when enough of it is one ring.
	tp := make(map[int]bool)
	for k, n := range overlap {
		if float64(n)/float64(len(ringTxns[k.ring])) >= coverThreshold {
			found[k.ring] = true
		}
		if float64(n)/float64(len(candidates[k.cand].TxnIDs)) >= purityThreshold {
			tp[k.cand] = true
		}
	}
	rep.RingsFound = len(found)
	rep.CandidatesTP = len(tp)
	for id := range found {
		rep.PerTypology[typologies[id]].Found++
	}

	// Transaction level against the laundering flag, which includes laundering
	// transfers that belong to no labelled ring.
	for _, t := range led.Txns {
		switch {
		case flagged[t.ID] && t.IsLaundering:
			rep.TxnTP++
		case flagged[t.ID] && !t.IsLaundering:
			rep.TxnFP++
			rep.FalsePositiveTxns++
			rep.FalsePositiveValue += t.Amount
		case !flagged[t.ID] && t.IsLaundering:
			rep.TxnFN++
		}
	}
	return rep
}

func precision(tp, fp int) float64 {
	if tp+fp == 0 {
		return 0
	}
	return float64(tp) / float64(tp+fp)
}

func recall(tp, fn int) float64 {
	if tp+fn == 0 {
		return 0
	}
	return float64(tp) / float64(tp+fn)
}

func f1(p, r float64) float64 {
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// String renders the report. Every rate is printed with the counts behind it,
// so a good-looking percentage over three cases is visibly a percentage over
// three cases.
func (r *Report) String() string {
	var b strings.Builder

	p := precision(r.TxnTP, r.TxnFP)
	rc := recall(r.TxnTP, r.TxnFN)

	fmt.Fprintf(&b, "candidates=%d flagged_txns=%d flagged_accounts=%d\n",
		r.Candidates, r.FlaggedTxns, r.FlaggedAccounts)

	fmt.Fprintf(&b, "\nring level\n")
	fmt.Fprintf(&b, "  rings found       %d/%d (%.1f%%)  [candidate covers >=%.0f%% of the ring]\n",
		r.RingsFound, r.LabelledRings, 100*recall(r.RingsFound, r.LabelledRings-r.RingsFound), 100*coverThreshold)
	fmt.Fprintf(&b, "  candidate purity  %d/%d (%.1f%%)  [>=%.0f%% of the candidate is one ring]\n",
		r.CandidatesTP, r.Candidates, 100*precision(r.CandidatesTP, r.Candidates-r.CandidatesTP), 100*purityThreshold)

	fmt.Fprintf(&b, "\ntransaction level (vs is_laundering)\n")
	fmt.Fprintf(&b, "  precision %.4f (%d/%d)\n", p, r.TxnTP, r.TxnTP+r.TxnFP)
	fmt.Fprintf(&b, "  recall    %.4f (%d/%d)\n", rc, r.TxnTP, r.TxnTP+r.TxnFN)
	fmt.Fprintf(&b, "  f1        %.4f\n", f1(p, rc))

	fmt.Fprintf(&b, "\nfalse positive cost\n")
	fmt.Fprintf(&b, "  %d legitimate transfers flagged, %.2f in value held\n",
		r.FalsePositiveTxns, r.FalsePositiveValue)
	if r.Candidates > 0 {
		fmt.Fprintf(&b, "  %.1f legitimate transfers per candidate raised\n",
			float64(r.FalsePositiveTxns)/float64(r.Candidates))
	}

	fmt.Fprintf(&b, "\nrecall by ring shape\n")
	stats := make([]*TypologyStat, 0, len(r.PerTypology))
	for _, s := range r.PerTypology {
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Typology < stats[j].Typology })
	for _, s := range stats {
		fmt.Fprintf(&b, "  %-16s %3d/%-3d (%5.1f%%)\n",
			s.Typology, s.Found, s.Labelled, 100*float64(s.Found)/float64(s.Labelled))
	}
	return b.String()
}
