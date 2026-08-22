// Package baseline is the system WARREN has to beat: a per-transaction risk
// scorer of the kind that sits in most anti-money-laundering stacks today.
//
// It exists to be a fair opponent, not a straw man. A rigged baseline proves
// nothing and would not survive being asked about, so this one is built the way
// a competent tabular AML model is built — amount, channel, timing, and, most
// importantly, account-level aggregates. Velocity and counterparty counts are
// exactly what legacy systems do have, and leaving them out would be cheating.
//
// Its account aggregates are built from the fitting period only. Computing them
// over the whole ledger leaks the answer: a mule's counterparty count is
// inflated by the very laundering the model is being asked to predict, and the
// baseline scores far better for a reason that would not exist in deployment.
//
// The point it demonstrates is structural rather than numerical. 95% of
// laundering transfers here sit inside the ordinary amount range for their
// channel. There is no row-level threshold that separates them, because the
// evidence of laundering does not exist in any single row — it exists in the
// relationship between rows. This is what the literature calls relational
// blindness, and no amount of feature engineering on a single transaction fixes
// it.
package baseline

import (
	"math"
	"sort"

	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/score"
)

// FeatureNames documents the vector layout.
var FeatureNames = []string{
	"log_amount",
	"hour_of_day",
	"sender_out_degree",
	"sender_txn_count",
	"log_sender_volume",
	"receiver_in_degree",
	"receiver_txn_count",
	"log_receiver_volume",
	"amount_vs_sender_mean",
	"amount_vs_receiver_mean",
}

// accountStats is the history a legacy system would hold per account.
type accountStats struct {
	out, in          map[int32]bool // distinct counterparties
	sentN, recvN     int
	sentSum, recvSum float64
}

// Aggregates holds per-account history for the whole ledger.
type Aggregates struct {
	stats map[int32]*accountStats
}

// BuildAggregates accumulates per-account history from the transfers accepted by
// include. Pass the fitting period only: history is what a deployed system knows
// at decision time, not what the ledger eventually contained.
func BuildAggregates(led *detect.Ledger, include func(detect.Txn) bool) *Aggregates {
	a := &Aggregates{stats: make(map[int32]*accountStats)}
	get := func(id int32) *accountStats {
		s, ok := a.stats[id]
		if !ok {
			s = &accountStats{out: make(map[int32]bool), in: make(map[int32]bool)}
			a.stats[id] = s
		}
		return s
	}
	for _, t := range led.Txns {
		if include != nil && !include(t) {
			continue
		}
		from, to := get(t.From), get(t.To)
		from.out[t.To] = true
		from.sentN++
		from.sentSum += t.Amount
		to.in[t.From] = true
		to.recvN++
		to.recvSum += t.Amount
	}
	return a
}

// Vector renders one transfer's row-level features.
func (a *Aggregates) Vector(t detect.Txn) []float64 {
	from := a.stats[t.From]
	to := a.stats[t.To]
	if from == nil {
		from = &accountStats{out: map[int32]bool{}, in: map[int32]bool{}}
	}
	if to == nil {
		to = &accountStats{out: map[int32]bool{}, in: map[int32]bool{}}
	}

	senderMean := 0.0
	if from.sentN > 0 {
		senderMean = from.sentSum / float64(from.sentN)
	}
	receiverMean := 0.0
	if to.recvN > 0 {
		receiverMean = to.recvSum / float64(to.recvN)
	}

	// Channel is deliberately absent. Both systems are scored over the same
	// ACH-filtered ledger, so it is constant within scope, and a constant
	// feature is noise dressed as information.
	return []float64{
		math.Log1p(t.Amount),
		float64(t.TS.Hour()),
		float64(len(from.out)),
		float64(from.sentN),
		math.Log1p(from.sentSum),
		float64(len(to.in)),
		float64(to.recvN),
		math.Log1p(to.recvSum),
		ratio(t.Amount, senderMean),
		ratio(t.Amount, receiverMean),
	}
}

func ratio(amount, mean float64) float64 {
	if mean <= 0 {
		return 1
	}
	return amount / mean
}

// Model is a fitted per-transaction scorer.
type Model struct {
	model *score.Model
	agg   *Aggregates
}

// Train fits the baseline on the transfers before cut, the same split WARREN's
// ranker is fitted on.
func Train(led *detect.Ledger, agg *Aggregates, isTrain func(detect.Txn) bool) *Model {
	var rows [][]float64
	var labels []bool
	for _, t := range led.Txns {
		if !isTrain(t) {
			continue
		}
		rows = append(rows, agg.Vector(t))
		labels = append(labels, t.IsLaundering)
	}
	m := score.Train(rows, labels, score.DefaultTrainOpts())
	m.Names = FeatureNames
	return &Model{model: m, agg: agg}
}

func (m *Model) Score(t detect.Txn) float64 {
	return m.model.Predict(m.agg.Vector(t))
}

func (m *Model) Explain() string { return m.model.Explain() }

// scoredTxn pairs a transfer with its baseline score for ranking.
type scoredTxn struct {
	txn   detect.Txn
	score float64
}

// Comparison is the head-to-head at one alert budget.
//
// The budget is expressed in transfers rather than alerts, because the two
// systems raise different objects: WARREN raises groups, the baseline raises
// rows. Holding the number of flagged transfers equal is the comparison that
// cannot be gamed by either side.
type Comparison struct {
	FlaggedTxns int

	BaselineTP    int
	BaselineRings int

	WarrenTP    int
	WarrenRings int

	TotalRings      int
	TotalLaundering int
}

// Compare scores every held-out transfer with the baseline, takes its top
// flagged transfers up to the same budget WARREN used, and counts what each
// found.
func Compare(led *detect.Ledger, m *Model, inScope func(detect.Txn) bool,
	warrenFlagged map[int32]bool) Comparison {

	var pool []scoredTxn
	rings := make(map[int32]map[int32]bool)
	total := 0

	for _, t := range led.Txns {
		if !inScope(t) {
			continue
		}
		pool = append(pool, scoredTxn{t, m.Score(t)})
		if t.IsLaundering {
			total++
		}
		if t.PatternID != 0 {
			if rings[t.PatternID] == nil {
				rings[t.PatternID] = make(map[int32]bool)
			}
			rings[t.PatternID][t.ID] = true
		}
	}

	budget := len(warrenFlagged)
	if budget > len(pool) {
		budget = len(pool)
	}

	sort.Slice(pool, func(i, j int) bool { return pool[i].score > pool[j].score })

	c := Comparison{
		FlaggedTxns:     budget,
		TotalRings:      len(rings),
		TotalLaundering: total,
	}

	baselineHits := make(map[int32]int)
	for _, s := range pool[:budget] {
		if s.txn.IsLaundering {
			c.BaselineTP++
		}
		if s.txn.PatternID != 0 {
			baselineHits[s.txn.PatternID]++
		}
	}
	c.BaselineRings = countRecovered(baselineHits, rings)

	warrenHits := make(map[int32]int)
	for _, t := range led.Txns {
		if !warrenFlagged[t.ID] || !inScope(t) {
			continue
		}
		if t.IsLaundering {
			c.WarrenTP++
		}
		if t.PatternID != 0 {
			warrenHits[t.PatternID]++
		}
	}
	c.WarrenRings = countRecovered(warrenHits, rings)
	return c
}

// countRecovered applies the same "found it" rule the ring evaluation uses:
// at least half the ring's transfers surfaced.
func countRecovered(hits map[int32]int, rings map[int32]map[int32]bool) int {
	n := 0
	for id, got := range hits {
		if float64(got)/float64(len(rings[id])) >= 0.5 {
			n++
		}
	}
	return n
}

// RingBlindness measures how deep a per-transaction scorer would have to dig
// before it saw a given ring at all.
//
// This is the concrete form of the structural argument. A ring can be entirely
// real and entirely invisible row by row: each transfer sits at an unremarkable
// percentile, so no threshold surfaces it without also surfacing a large
// fraction of the ledger.
type RingBlindness struct {
	PatternID int32
	Txns      int
	// BestPercentile is the highest rank any single transfer in the ring reached,
	// as a share of the scored population. 0.30 means the most suspicious-looking
	// transfer in the ring only makes the top 30%.
	BestPercentile   float64
	MedianPercentile float64
	// FlaggedAt10Pct counts how many of the ring's transfers a scorer would catch
	// if it alerted on the riskiest 10% of all transfers.
	FlaggedAt10Pct int
}

// Blindness ranks every in-scope transfer by baseline score and reports, per
// labelled ring, where its transfers landed.
func Blindness(led *detect.Ledger, m *Model, inScope func(detect.Txn) bool) map[int32]RingBlindness {
	var pool []scoredTxn
	for _, t := range led.Txns {
		if inScope(t) {
			pool = append(pool, scoredTxn{t, m.Score(t)})
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].score > pool[j].score })

	n := float64(len(pool))
	pct := make(map[int32]float64, len(pool))
	for i, s := range pool {
		pct[s.txn.ID] = float64(i+1) / n
	}

	byRing := make(map[int32][]float64)
	for _, s := range pool {
		if s.txn.PatternID != 0 {
			byRing[s.txn.PatternID] = append(byRing[s.txn.PatternID], pct[s.txn.ID])
		}
	}

	out := make(map[int32]RingBlindness, len(byRing))
	for id, ps := range byRing {
		sort.Float64s(ps)
		rb := RingBlindness{PatternID: id, Txns: len(ps), BestPercentile: ps[0],
			MedianPercentile: ps[len(ps)/2]}
		for _, p := range ps {
			if p <= 0.10 {
				rb.FlaggedAt10Pct++
			}
		}
		out[id] = rb
	}
	return out
}
