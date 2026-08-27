package baseline

import (
	"math"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/detect"
)

var epoch = time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

func tx(id int32, hours float64, from, to int32, amount float64) detect.Txn {
	return detect.Txn{ID: id, TS: epoch.Add(time.Duration(hours * float64(time.Hour))),
		From: from, To: to, Amount: amount}
}

// The baseline exists to be a fair opponent. If its account history is built
// over the whole ledger it knows the answer: a mule's counterparty count is
// inflated by the very laundering it is being asked to predict, and it scores
// well for a reason that would not exist in deployment.
func TestAggregatesSeeOnlyTheFittingPeriod(t *testing.T) {
	led := &detect.Ledger{Txns: []detect.Txn{
		tx(1, 0, 1, 2, 100),
		tx(2, 1, 1, 3, 100),
		tx(3, 500, 1, 4, 100), // after the cut
		tx(4, 501, 1, 5, 100),
	}}
	cut := epoch.Add(100 * time.Hour)
	agg := BuildAggregates(led, func(x detect.Txn) bool { return x.TS.Before(cut) })

	v := agg.Vector(tx(9, 502, 1, 6, 100))
	const distinctOut, sentCount = 2, 3
	if v[distinctOut] != 2 {
		t.Errorf("sender has %g distinct counterparties, want the 2 seen before the cut", v[distinctOut])
	}
	if v[sentCount] != 2 {
		t.Errorf("sender sent %g transfers, want the 2 before the cut", v[sentCount])
	}
}

// An account with no history must produce a usable vector rather than a panic
// or a NaN: in deployment most accounts in a given window are new.
func TestAnUnseenAccountScoresWithoutHistory(t *testing.T) {
	agg := BuildAggregates(&detect.Ledger{}, nil)
	v := agg.Vector(tx(1, 0, 77, 88, 5000))
	for i, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			t.Fatalf("feature %d = %g for an account with no history", i, x)
		}
	}
	if len(v) != len(FeatureNames) {
		t.Fatalf("vector has %d entries, FeatureNames declares %d", len(v), len(FeatureNames))
	}
}

// The amount-against-mean features are the ones a legacy system leans on. With
// no mean to compare against, "one times normal" is the only honest answer;
// dividing by zero would hand every new account an infinite score.
func TestRatioAgainstNoHistoryIsNeutral(t *testing.T) {
	if got := ratio(5000, 0); got != 1 {
		t.Errorf("ratio against no history = %g, want 1", got)
	}
	if got := ratio(5000, 1000); got != 5 {
		t.Errorf("ratio = %g, want 5", got)
	}
}

// The comparison is held at an equal number of flagged transfers, because the
// two systems raise different objects — WARREN raises groups, the baseline
// raises rows — and any other denominator lets one side spend more than the
// other.
func TestTheComparisonSpendsTheSameBudgetOnBothSides(t *testing.T) {
	var txns []detect.Txn
	for i := int32(0); i < 100; i++ {
		txns = append(txns, tx(i+1, float64(i), 1, 2, 100))
	}
	led := &detect.Ledger{Txns: txns}

	agg := BuildAggregates(led, nil)
	m := Train(led, agg, func(detect.Txn) bool { return true })

	warren := map[int32]bool{1: true, 2: true, 3: true}
	c := Compare(led, m, func(detect.Txn) bool { return true }, warren)
	if c.FlaggedTxns != 3 {
		t.Errorf("baseline budget = %d, want the 3 transfers WARREN flagged", c.FlaggedTxns)
	}

	// A budget larger than the pool is clamped rather than over-spent.
	big := map[int32]bool{}
	for i := int32(1); i <= 500; i++ {
		big[i] = true
	}
	if c := Compare(led, m, func(detect.Txn) bool { return true }, big); c.FlaggedTxns != 100 {
		t.Errorf("budget = %d, want the pool size 100", c.FlaggedTxns)
	}
}

// Both sides are counted with the same "found it" rule the ring evaluation
// uses. A comparison where one side gets a looser definition of success is not
// a comparison.
func TestBothSidesAreCountedWithTheSameRecoveryRule(t *testing.T) {
	rings := map[int32]map[int32]bool{7: {1: true, 2: true, 3: true, 4: true}}
	if n := countRecovered(map[int32]int{7: 2}, rings); n != 1 {
		t.Errorf("half a ring counted as %d, want 1", n)
	}
	if n := countRecovered(map[int32]int{7: 1}, rings); n != 0 {
		t.Errorf("a quarter of a ring counted as %d, want 0", n)
	}
}

// Only transfers in scope for both systems count, on either side.
func TestOutOfScopeTransfersCountForNeitherSide(t *testing.T) {
	led := &detect.Ledger{Txns: []detect.Txn{
		{ID: 1, TS: epoch, From: 1, To: 2, Amount: 100, IsLaundering: true},
		{ID: 2, TS: epoch.Add(time.Hour), From: 3, To: 4, Amount: 100, IsLaundering: true},
	}}
	agg := BuildAggregates(led, nil)
	m := Train(led, agg, func(detect.Txn) bool { return true })

	inScope := func(x detect.Txn) bool { return x.ID == 1 }
	c := Compare(led, m, inScope, map[int32]bool{1: true, 2: true})
	if c.TotalLaundering != 1 {
		t.Errorf("laundering in scope = %d, want 1", c.TotalLaundering)
	}
	if c.WarrenTP != 1 {
		t.Errorf("WARREN was credited %d, want 1 — the out-of-scope hit must not count", c.WarrenTP)
	}
}
