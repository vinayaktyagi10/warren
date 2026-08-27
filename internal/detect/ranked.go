package detect

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/score"
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
	// Rounded rather than truncated: the float product lands a nanosecond short
	// of the intended instant, which would put a candidate whose window sits
	// exactly on the boundary on the wrong side of the split.
	return first.Add(time.Duration(math.Round(float64(last.Sub(first)) * trainFraction)))
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

// Vectors renders candidate features for the model under the default set.
func Vectors(candidates []Candidate) [][]float64 {
	return VectorsFor(candidates, DefaultFeatureSet)
}

// VectorsFor renders candidate features under a named set.
func VectorsFor(candidates []Candidate, set FeatureSet) [][]float64 {
	out := make([][]float64, len(candidates))
	for i, c := range candidates {
		out[i] = c.Features.VectorFor(set)
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

	// PerTypology is recall by ring shape at this budget, present only when
	// Evaluate was given the labels. The unranked report has always broken
	// recall out per shape; without the same breakdown here, the one number an
	// analyst actually experiences is the average that hides BIPARTITE and
	// STACK.
	PerTypology map[string]*TypologyStat
}

// EvaluateRanked scores the held-out candidates at a series of alert budgets.
func EvaluateRanked(led *Ledger, candidates []Candidate, scores []float64, budgets []int) *RankedReport {
	return EvaluateRankedByShape(led, candidates, scores, budgets, nil)
}

// EvaluateRankedByShape is EvaluateRanked with recall broken out per ring shape.
func EvaluateRankedByShape(led *Ledger, candidates []Candidate, scores []float64,
	budgets []int, typologies map[int32]string) *RankedReport {
	order := RankOrder(scores)

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
		if typologies != nil {
			row.PerTypology = make(map[string]*TypologyStat)
			for ring := range ringTxns {
				ty := typologies[ring]
				if row.PerTypology[ty] == nil {
					row.PerTypology[ty] = &TypologyStat{Typology: ty}
				}
				row.PerTypology[ty].Labelled++
			}
		}
		for ring, n := range hits {
			if float64(n)/float64(len(ringTxns[ring])) >= coverThreshold {
				row.RingsFound++
				if row.PerTypology != nil {
					row.PerTypology[typologies[ring]].Found++
				}
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

// ShapesAt renders recall per ring shape at one budget, sorted worst first so
// the shapes the detector struggles with are the ones read first.
func (r *RankedReport) ShapesAt(budget int) string {
	var row *RankedRow
	for i := range r.Rows {
		if r.Rows[i].TopK == budget {
			row = &r.Rows[i]
			break
		}
	}
	if row == nil || row.PerTypology == nil {
		return ""
	}
	stats := make([]*TypologyStat, 0, len(row.PerTypology))
	for _, s := range row.PerTypology {
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool {
		a := float64(stats[i].Found) / float64(stats[i].Labelled)
		b := float64(stats[j].Found) / float64(stats[j].Labelled)
		if a != b {
			return a < b
		}
		return stats[i].Typology < stats[j].Typology
	})

	var b strings.Builder
	fmt.Fprintf(&b, "recall by ring shape at %d alerts\n", row.TopK)
	for _, s := range stats {
		fmt.Fprintf(&b, "  %-16s %3d/%-3d (%5.1f%%)\n",
			s.Typology, s.Found, s.Labelled, 100*float64(s.Found)/float64(s.Labelled))
	}
	return b.String()
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

// RankOrder returns candidate indices most suspicious first.
//
// Ties break by position rather than arbitrarily. Equal scores are common — a
// feature that is zero for nearly every candidate, such as the share of its
// accounts on a suspect list, produces long blocks of them — and an unstable
// sort resolves such a block differently per run. The alert budget then cuts
// the block in a different place each time, and the same pipeline reports
// several answers to the same question.
func RankOrder(scores []float64) []int {
	order := make([]int, len(scores))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if scores[order[a]] != scores[order[b]] {
			return scores[order[a]] > scores[order[b]]
		}
		return order[a] < order[b]
	})
	return order
}

// Ranker is a fitted model bound to the feature set it was fitted on.
//
// The two travel together because separating them is a silent failure rather
// than a loud one: scoring a candidate with a vector built from a different set
// either mislabels every coefficient the console prints, or — once the sets
// differ in length — quietly reads garbage through the standardiser. Binding
// them makes the mismatch unrepresentable instead of merely unlikely.
type Ranker struct {
	*score.Model
	Set FeatureSet
}

// TrainRanker fits the candidate ranker, naming its coefficients from the set
// they were built from.
func TrainRanker(candidates []Candidate, labels []bool, set FeatureSet, opts score.TrainOpts) *Ranker {
	m := score.Train(VectorsFor(candidates, set), labels, opts)
	m.Names = FeatureNamesFor(set)
	return &Ranker{Model: m, Set: set}
}

// Score returns the probability that one candidate is a ring.
func (r *Ranker) Score(c Candidate) float64 {
	return r.Predict(c.Features.VectorFor(r.Set))
}

// ScoreAll scores a slice in order.
func (r *Ranker) ScoreAll(candidates []Candidate) []float64 {
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		out[i] = r.Score(c)
	}
	return out
}
