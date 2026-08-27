package registry

import (
	"math"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/detect"
)

var epoch = time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

func at(h float64) time.Time { return epoch.Add(time.Duration(h * float64(time.Hour))) }

func ledger(n int32) *detect.Ledger {
	led := &detect.Ledger{}
	for i := int32(0); i <= n; i++ {
		led.Accounts = append(led.Accounts, "bank|acct")
	}
	return led
}

// The property everything else depends on. A registry entry dated after a
// window closed must be invisible to that window, or the feature is reading the
// answer: in deployment the list simply does not contain that account yet.
func TestARegistryEntryIsInvisibleBeforeItIsListed(t *testing.T) {
	r := &Registry{listed: map[int32]time.Time{7: at(100)}}

	if r.ListedAt(7, at(99)) {
		t.Error("an account was visible an hour before it was reported")
	}
	if !r.ListedAt(7, at(100)) {
		t.Error("an account was invisible at the instant it was reported")
	}
	if !r.ListedAt(7, at(200)) {
		t.Error("an account stopped being listed")
	}
	if r.ListedAt(8, at(200)) {
		t.Error("an account nobody reported is on the list")
	}
}

// A real registry is partial, late and noisy. Simulating a perfect one would
// make every number that follows meaningless.
func TestTheSimulatedRegistryIsPartialLateAndNoisy(t *testing.T) {
	led := ledger(2000)
	var id int32 = 1
	// 500 accounts that launder, 1500 that do not.
	for ; id <= 500; id++ {
		led.Txns = append(led.Txns, detect.Txn{ID: id, TS: at(float64(id % 100)),
			From: id, To: id + 2000, Amount: 1000, IsLaundering: true, PatternID: id%20 + 1})
	}
	for ; id <= 2000; id++ {
		led.Txns = append(led.Txns, detect.Txn{ID: id, TS: at(float64(id % 100)),
			From: id, To: id + 2000, Amount: 1000})
	}

	opts := SimOpts{Coverage: 0.3, ReportDelay: 48 * time.Hour, FalseRate: 0.1, Seed: 1}
	r := Simulate(led, opts)

	if r.Size() == 0 {
		t.Fatal("the simulated registry is empty")
	}

	launderer := make(map[int32]bool)
	for _, tn := range led.Txns {
		if tn.IsLaundering {
			launderer[tn.From] = true
		}
	}

	var covered, false_ int
	for acct := range r.listed {
		if launderer[acct] {
			covered++
		} else {
			false_++
		}
	}

	share := float64(covered) / float64(len(launderer))
	if math.Abs(share-0.3) > 0.08 {
		t.Errorf("coverage of laundering accounts is %.2f, asked for 0.30", share)
	}
	if false_ == 0 {
		t.Error("no false reports at all; a registry with perfect precision is not one")
	}

	// Nothing is listed before it acted.
	firstSeen := make(map[int32]time.Time)
	for _, tn := range led.Txns {
		if prev, ok := firstSeen[tn.From]; !ok || tn.TS.Before(prev) {
			firstSeen[tn.From] = tn.TS
		}
	}
	for acct, when := range r.listed {
		if seen, ok := firstSeen[acct]; ok && when.Before(seen) {
			t.Fatalf("account %d was listed at %v, before it ever transacted at %v", acct, when, seen)
		}
	}
}

// The same seed must give the same registry, or no comparison against it means
// anything twice.
func TestSimulationIsReproducible(t *testing.T) {
	led := ledger(400)
	for id := int32(1); id <= 200; id++ {
		led.Txns = append(led.Txns, detect.Txn{ID: id, TS: at(float64(id)),
			From: id, To: id + 200, Amount: 100, IsLaundering: id%2 == 0, PatternID: id % 7})
	}
	opts := SimOpts{Coverage: 0.5, ReportDelay: time.Hour, FalseRate: 0.1, Seed: 42}

	a, b := Simulate(led, opts), Simulate(led, opts)
	if a.Size() != b.Size() {
		t.Fatalf("two runs listed %d and %d accounts", a.Size(), b.Size())
	}
	for acct, when := range a.listed {
		if got, ok := b.listed[acct]; !ok || !got.Equal(when) {
			t.Fatalf("account %d listed at %v then %v", acct, when, got)
		}
	}

	opts.Seed = 43
	if c := Simulate(led, opts); c.Size() == a.Size() && sameKeys(a, c) {
		t.Error("a different seed produced an identical registry")
	}
}

func sameKeys(a, b *Registry) bool {
	for k := range a.listed {
		if _, ok := b.listed[k]; !ok {
			return false
		}
	}
	return true
}

// The feature is the share of a candidate's accounts already on the list at the
// moment its window closed — never at the moment the report is written.
func TestAnnotateScoresAgainstTheWindowNotTheReport(t *testing.T) {
	r := &Registry{listed: map[int32]time.Time{1: at(10), 2: at(500)}}
	cands := []detect.Candidate{
		{Accounts: []int32{1, 2, 3, 4}, Window: at(100)},
		{Accounts: []int32{1, 2, 3, 4}, Window: at(600)},
	}
	Annotate(cands, r)

	if got := cands[0].Features.RegistryShare; math.Abs(got-0.25) > 1e-9 {
		t.Errorf("early window: %g, want 0.25 — only account 1 was listed by then", got)
	}
	if got := cands[1].Features.RegistryShare; math.Abs(got-0.5) > 1e-9 {
		t.Errorf("late window: %g, want 0.5", got)
	}
}

func TestAnnotateWithNoRegistryLeavesTheFeatureAtZero(t *testing.T) {
	cands := []detect.Candidate{{Accounts: []int32{1, 2}, Window: at(0),
		Features: detect.Features{RegistryShare: 0.9}}}
	Annotate(cands, nil)
	if cands[0].Features.RegistryShare != 0 {
		t.Errorf("got %g, want 0", cands[0].Features.RegistryShare)
	}
}

// The measurement the registry exists to support: a list names accounts, and
// the point of a graph is that each named account implicates others. Counting
// the listed accounts back as a success would be counting the input as output.
func TestAmplificationExcludesTheAccountsTheListAlreadyNamed(t *testing.T) {
	led := ledger(10)
	for i := int32(1); i <= 4; i++ {
		led.Txns = append(led.Txns, detect.Txn{ID: i, TS: at(float64(i)),
			From: i, To: i + 1, Amount: 1000, IsLaundering: true, PatternID: 1})
	}
	r := &Registry{listed: map[int32]time.Time{2: at(0)}}
	cands := []detect.Candidate{{
		TxnIDs: []int32{1, 2, 3, 4}, Accounts: []int32{1, 2, 3, 4, 5}, Window: at(10)}}

	a := Amplification(led, cands, []float64{0.9}, 1, r)
	if a.Listed != 1 {
		t.Errorf("listed accounts in scope = %d, want 1", a.Listed)
	}
	if a.Implicated != 4 {
		t.Errorf("implicated = %d, want the 4 laundering accounts the list did not name",
			a.Implicated)
	}
	if math.Abs(a.PerListedAccount()-4) > 1e-9 {
		t.Errorf("amplification = %g, want 4", a.PerListedAccount())
	}
}

// A candidate the ranker never surfaced within the budget implicates nobody,
// however many listed accounts it contains.
func TestAmplificationOnlyCountsWhatTheBudgetReached(t *testing.T) {
	led := ledger(10)
	for i := int32(1); i <= 4; i++ {
		led.Txns = append(led.Txns, detect.Txn{ID: i, TS: at(float64(i)),
			From: i, To: i + 1, Amount: 1000, IsLaundering: true, PatternID: 1})
	}
	r := &Registry{listed: map[int32]time.Time{2: at(0)}}
	cands := []detect.Candidate{
		{TxnIDs: []int32{9}, Accounts: []int32{9}, Window: at(10)},
		{TxnIDs: []int32{1, 2, 3, 4}, Accounts: []int32{1, 2, 3, 4, 5}, Window: at(10)},
	}
	a := Amplification(led, cands, []float64{0.9, 0.1}, 1, r)
	if a.Implicated != 0 {
		t.Errorf("implicated %d accounts from a candidate outside the budget", a.Implicated)
	}
}

func TestAmplificationWithAnEmptyRegistry(t *testing.T) {
	a := Amplification(ledger(2), nil, nil, 10, &Registry{listed: map[int32]time.Time{}})
	if a.PerListedAccount() != 0 {
		t.Errorf("got %g, want 0 rather than a division by zero", a.PerListedAccount())
	}
}
