package detect

import "testing"

// Evaluation decides what every headline number in this project means, so the
// two thresholds it turns on are pinned here rather than left to be discovered
// by whoever next reads a percentage.

func ring(id int32, ids ...int32) []Txn {
	out := make([]Txn, 0, len(ids))
	for i, tid := range ids {
		out = append(out, Txn{ID: tid, TS: at(float64(i)), From: 1, To: 2,
			Amount: 100, IsLaundering: true, PatternID: id})
	}
	return out
}

func noise(from int32, n int) []Txn {
	out := make([]Txn, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Txn{ID: from + int32(i), TS: at(float64(i)), From: 50, To: 51, Amount: 10})
	}
	return out
}

// A candidate holding half a ring counts as having found it: that is the point
// past which an investigator opening the alert sees the ring rather than a
// fragment. Below it, nothing is claimed.
func TestARingIsFoundOnlyWhenACandidateCoversHalfOfIt(t *testing.T) {
	led := ledgerOf(ring(1, 1, 2, 3, 4)...)
	typ := map[int32]string{1: "CYCLE"}

	half := Evaluate(led, []Candidate{{TxnIDs: []int32{1, 2}}}, typ)
	if half.RingsFound != 1 {
		t.Errorf("2 of 4 transfers: found %d, want 1", half.RingsFound)
	}

	quarter := Evaluate(led, []Candidate{{TxnIDs: []int32{1}}}, typ)
	if quarter.RingsFound != 0 {
		t.Errorf("1 of 4 transfers: found %d, want 0", quarter.RingsFound)
	}
}

// Purity is what stops "flag everything" scoring as a success. A candidate that
// swallowed a ring plus a hundred innocent transfers has not found anything an
// analyst can act on.
func TestACandidateBuriedInNoiseIsNotATruePositive(t *testing.T) {
	txns := append(ring(1, 1, 2, 3, 4), noise(100, 100)...)
	led := ledgerOf(txns...)
	typ := map[int32]string{1: "CYCLE"}

	ids := []int32{1, 2, 3, 4}
	for i := int32(0); i < 100; i++ {
		ids = append(ids, 100+i)
	}
	rep := Evaluate(led, []Candidate{{TxnIDs: ids}}, typ)

	if rep.RingsFound != 1 {
		t.Errorf("the ring is inside the candidate: found %d, want 1", rep.RingsFound)
	}
	if rep.CandidatesTP != 0 {
		t.Errorf("4 real of 104 flagged counted as a true positive")
	}
	if rep.FalsePositiveTxns != 100 || rep.FalsePositiveValue != 1000 {
		t.Errorf("false positive cost = %d transfers / %.0f, want 100 / 1000",
			rep.FalsePositiveTxns, rep.FalsePositiveValue)
	}
}

// Recall is reported per shape because one average hides a structure the
// detector never finds at all — which is exactly the case for BIPARTITE and
// STACK here.
func TestRecallIsBrokenOutPerShape(t *testing.T) {
	txns := append(ring(1, 1, 2, 3, 4), ring(2, 5, 6, 7, 8)...)
	led := ledgerOf(txns...)
	typ := map[int32]string{1: "CYCLE", 2: "STACK"}

	rep := Evaluate(led, []Candidate{{TxnIDs: []int32{1, 2, 3, 4}}}, typ)
	if rep.LabelledRings != 2 {
		t.Fatalf("labelled rings = %d, want 2", rep.LabelledRings)
	}
	if got := rep.PerTypology["CYCLE"]; got.Found != 1 || got.Labelled != 1 {
		t.Errorf("CYCLE = %d/%d, want 1/1", got.Found, got.Labelled)
	}
	if got := rep.PerTypology["STACK"]; got.Found != 0 || got.Labelled != 1 {
		t.Errorf("STACK = %d/%d, want 0/1 — the shape that was missed must show as missed",
			got.Found, got.Labelled)
	}
}

// Laundering transfers that belong to no labelled ring still count against
// transaction-level recall. Scoring only the labelled ones would quietly shrink
// the denominator.
func TestUnlabelledLaunderingStillCountsAgainstRecall(t *testing.T) {
	txns := append(ring(1, 1, 2, 3, 4),
		Txn{ID: 9, TS: at(9), From: 7, To: 8, Amount: 100, IsLaundering: true})
	led := ledgerOf(txns...)

	rep := Evaluate(led, []Candidate{{TxnIDs: []int32{1, 2, 3, 4}}}, map[int32]string{1: "CYCLE"})
	if rep.TxnTP != 4 || rep.TxnFN != 1 {
		t.Errorf("tp/fn = %d/%d, want 4/1", rep.TxnTP, rep.TxnFN)
	}
}

// --------------------------------------------------------------------------
// ranked evaluation — what an analyst with a fixed budget actually sees
// --------------------------------------------------------------------------

func TestRankedEvaluationSpendsTheBudgetOnTheHighestScores(t *testing.T) {
	txns := append(ring(1, 1, 2, 3, 4), noise(100, 4)...)
	led := ledgerOf(txns...)

	cands := []Candidate{
		{TxnIDs: []int32{100, 101, 102, 103}}, // noise
		{TxnIDs: []int32{1, 2, 3, 4}},         // the ring
	}
	scores := []float64{0.1, 0.9}

	rep := EvaluateRanked(led, cands, scores, []int{1, 2})
	if rep.Rows[0].RingsFound != 1 {
		t.Errorf("budget 1 spent on the wrong candidate: found %d rings", rep.Rows[0].RingsFound)
	}
	if rep.Rows[0].TxnFP != 0 {
		t.Errorf("budget 1 raised %d false positives, want 0", rep.Rows[0].TxnFP)
	}
	if rep.Rows[1].TxnFP != 4 {
		t.Errorf("budget 2 must take the noise too: fp = %d, want 4", rep.Rows[1].TxnFP)
	}
	if p := rep.Rows[1].PrecisionAt; p != 0.5 {
		t.Errorf("precision at 2 = %g, want 0.5", p)
	}
}

// Recall at a budget is measured against the laundering present in this split,
// not against the whole ledger — otherwise a held-out report silently carries
// the training set's denominator.
func TestRankedRecallUsesTheSplitsOwnDenominator(t *testing.T) {
	txns := append(ring(1, 1, 2, 3, 4), ring(2, 5, 6, 7, 8)...)
	led := ledgerOf(txns...)

	// Only ring 1's transfers appear in any candidate, so ring 2 is not in this split.
	rep := EvaluateRanked(led, []Candidate{{TxnIDs: []int32{1, 2, 3, 4}}}, []float64{0.9}, []int{1})
	row := rep.Rows[0]
	if row.TotalRings != 1 {
		t.Errorf("total rings = %d, want 1 — ring 2 is not in this split", row.TotalRings)
	}
	if row.TxnFN != 0 {
		t.Errorf("false negatives = %d, want 0; ring 2's transfers leaked into the denominator", row.TxnFN)
	}
}

func TestBudgetLargerThanTheQueueIsClamped(t *testing.T) {
	led := ledgerOf(ring(1, 1, 2)...)
	rep := EvaluateRanked(led, []Candidate{{TxnIDs: []int32{1, 2}}}, []float64{0.5}, []int{1000})
	if rep.Rows[0].TopK != 1 {
		t.Errorf("TopK = %d, want the queue length 1", rep.Rows[0].TopK)
	}
}
