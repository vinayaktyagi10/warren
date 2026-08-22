package detect

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Split divides candidates by time into a fitting set and a held-out set.
//
// The split is temporal, never random. Windows overlap, so the same ring
// surfaces in several neighbouring candidates; shuffling rows would put those
// near-duplicates on both sides of the split and the model would be scored on
// groups it had effectively already seen. Cutting on time also matches how the
// system would actually run — fitted on what has happened, judged on what comes
// next.
func Split(candidates []Candidate, cut time.Time) (train, test []Candidate) {
	for _, c := range candidates {
		if c.Window.Before(cut) {
			train = append(train, c)
		} else {
			test = append(test, c)
		}
	}
	return train, test
}

// SplitTime returns the timestamp that puts the given fraction of the ledger's
// span into the fitting set.
func SplitTime(led *Ledger, trainFraction float64) time.Time {
	if len(led.Txns) == 0 {
		return time.Time{}
	}
	first := led.Txns[0].TS
	last := led.Txns[len(led.Txns)-1].TS
	return first.Add(time.Duration(float64(last.Sub(first)) * trainFraction))
}

// Labels marks which candidates contain a labelled ring.
func Labels(led *Ledger, candidates []Candidate) []bool {
	ringTxn := make(map[int32]bool)
	for _, t := range led.Txns {
		if t.PatternID != 0 {
			ringTxn[t.ID] = true
		}
	}
	out := make([]bool, len(candidates))
	for i, c := range candidates {
		for _, id := range c.TxnIDs {
			if ringTxn[id] {
				out[i] = true
				break
			}
		}
	}
	return out
}

// Vectors renders candidate features for the model.
func Vectors(candidates []Candidate) [][]float64 {
	out := make([][]float64, len(candidates))
	for i, c := range candidates {
		out[i] = c.Features.Vector()
	}
	return out
}

// RankedReport measures what an analyst would actually see: not every candidate
// the graph pass raised, but the top few hundred by score, which is as many
// alerts as a team can work through in a day.
type RankedReport struct {
	Rows []RankedRow
}

type RankedRow struct {
	TopK        int
	RingsFound  int
	TotalRings  int
	TxnTP       int
	TxnFP       int
	TxnFN       int
	FPValue     float64
	PrecisionAt float64
}

// EvaluateRanked scores the held-out candidates at a series of alert budgets.
func EvaluateRanked(led *Ledger, candidates []Candidate, scores []float64, budgets []int) *RankedReport {
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })

	// Ground truth restricted to the transfers present in this split.
	ringTxns := make(map[int32]map[int32]bool)
	inSplit := make(map[int32]bool)
	for _, c := range candidates {
		for _, id := range c.TxnIDs {
			inSplit[id] = true
		}
	}
	launderingTotal := 0
	txnByID := make(map[int32]*Txn, len(led.Txns))
	for i := range led.Txns {
		t := &led.Txns[i]
		txnByID[t.ID] = t
		if !inSplit[t.ID] {
			continue
		}
		if t.IsLaundering {
			launderingTotal++
		}
		if t.PatternID != 0 {
			if ringTxns[t.PatternID] == nil {
				ringTxns[t.PatternID] = make(map[int32]bool)
			}
			ringTxns[t.PatternID][t.ID] = true
		}
	}

	rep := &RankedReport{}
	for _, k := range budgets {
		if k > len(order) {
			k = len(order)
		}
		flagged := make(map[int32]bool)
		hits := make(map[int32]int)
		for _, ci := range order[:k] {
			for _, id := range candidates[ci].TxnIDs {
				flagged[id] = true
				if t := txnByID[id]; t != nil && t.PatternID != 0 {
					hits[t.PatternID]++
				}
			}
		}

		row := RankedRow{TopK: k, TotalRings: len(ringTxns)}
		for ring, n := range hits {
			if float64(n)/float64(len(ringTxns[ring])) >= coverThreshold {
				row.RingsFound++
			}
		}
		for id := range flagged {
			t := txnByID[id]
			if t == nil {
				continue
			}
			if t.IsLaundering {
				row.TxnTP++
			} else {
				row.TxnFP++
				row.FPValue += t.Amount
			}
		}
		row.TxnFN = launderingTotal - row.TxnTP
		row.PrecisionAt = precision(row.TxnTP, row.TxnFP)
		rep.Rows = append(rep.Rows, row)
	}
	return rep
}

func (r *RankedReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%8s %14s %11s %10s %10s %16s\n",
		"alerts", "rings found", "precision", "recall", "f1", "fp value held")
	for _, row := range r.Rows {
		p := precision(row.TxnTP, row.TxnFP)
		rc := recall(row.TxnTP, row.TxnFN)
		fmt.Fprintf(&b, "%8d %6d/%-7d %11.4f %10.4f %10.4f %16.0f\n",
			row.TopK, row.RingsFound, row.TotalRings, p, rc, f1(p, rc), row.FPValue)
	}
	return b.String()
}
