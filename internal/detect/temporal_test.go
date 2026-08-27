package detect

import (
	"math"
	"testing"

	"github.com/vinayaktyagi10/warren/internal/score"
)

func defaultOpts() score.TrainOpts { return score.DefaultTrainOpts() }

// Three temporal features, tested before they exist, because each is a claim
// about what laundering looks like in time and a claim has to be falsifiable.

// Burstiness is the finite-size corrected Goh–Barabási parameter. Perfectly
// regular traffic is -1, Poisson arrivals sit near 0, and a tight burst against
// a quiet background approaches +1 — for any number of transfers, which is what
// the correction buys.
func TestBurstiness(t *testing.T) {
	cases := []struct {
		name  string
		hours []float64
		want  float64
		tol   float64
	}{
		{"perfectly regular is -1", []float64{0, 1, 2, 3, 4, 5}, -1, 1e-9},
		{"a tight burst then a long wait approaches +1",
			[]float64{0, 0.01, 0.02, 0.03, 100}, 1.0, 0.02},
		// One interval says nothing about arrangement, and the correction
		// divides by zero saying so. Neutral, not -1.
		{"two transfers cannot be called either", []float64{0, 5}, 0, 1e-9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var g []Txn
			for i, h := range c.hours {
				g = append(g, tx(int32(i+1), h, 1, 2, 100))
			}
			got := burstiness(g)
			if math.Abs(got-c.want) > c.tol {
				t.Errorf("burstiness = %.4f, want %.4f ± %.2f", got, c.want, c.tol)
			}
			if got < -1 || got > 1 {
				t.Errorf("burstiness = %.4f, outside [-1, 1]", got)
			}
		})
	}
}

// The reason for the correction, stated as a test. Raw Goh–Barabási tops out at
// (sqrt(m)-1)/(sqrt(m)+1), so a three-transfer group could never score above
// 0.17 while a fifty-transfer one reached 0.75, and the ranker would have read
// the feature as a proxy for group size — which it already has two features for.
func TestBurstinessDoesNotSmuggleInGroupSize(t *testing.T) {
	pack := func(n int) []Txn {
		var g []Txn
		for i := 0; i < n; i++ {
			g = append(g, tx(int32(i+1), float64(i)*0.001, 1, 2, 100))
		}
		return append(g, tx(int32(n+1), 1000, 1, 2, 100))
	}
	small, large := burstiness(pack(3)), burstiness(pack(50))
	if small < 0.9 || large < 0.9 {
		t.Errorf("maximally bursty groups scored %.3f (3 transfers) and %.3f (50)", small, large)
	}
	if math.Abs(small-large) > 0.1 {
		t.Errorf("the same arrangement scores %.3f at 3 transfers and %.3f at 50; "+
			"the feature is measuring size", small, large)
	}

	even := func(n int) []Txn {
		var g []Txn
		for i := 0; i < n; i++ {
			g = append(g, tx(int32(i+1), float64(i), 1, 2, 100))
		}
		return g
	}
	if a, b := burstiness(even(3)), burstiness(even(50)); math.Abs(a-b) > 1e-9 {
		t.Errorf("perfectly regular scores %.3f at 3 and %.3f at 50", a, b)
	}
}

// A group cannot be called bursty or regular on one observation, and simultaneous
// transfers leave the ratio undefined. Both must land on the neutral value rather
// than on a NaN that would sort arbitrarily in the alert queue.
func TestBurstinessIsNeutralWhereItIsUndefined(t *testing.T) {
	if got := burstiness([]Txn{tx(1, 0, 1, 2, 100)}); got != 0 {
		t.Errorf("one transfer: %g, want 0", got)
	}
	if got := burstiness(nil); got != 0 {
		t.Errorf("no transfers: %g, want 0", got)
	}
	same := []Txn{tx(1, 5, 1, 2, 100), tx(2, 5, 1, 3, 100), tx(3, 5, 1, 4, 100)}
	got := burstiness(same)
	if math.IsNaN(got) || got != 0 {
		t.Errorf("simultaneous transfers: %g, want 0", got)
	}
}

// The concentration measure. Structuring pushes many transfers through a short
// interval, and span_hours cannot see that: a ring that fires twenty transfers
// in one hour and one a week later has the same span as one spread evenly.
func TestMaxHourShare(t *testing.T) {
	spread := []Txn{tx(1, 0, 1, 2, 100), tx(2, 24, 1, 3, 100), tx(3, 48, 1, 4, 100), tx(4, 72, 1, 5, 100)}
	if got := maxHourShare(spread); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("evenly spread over four days: %g, want 0.25", got)
	}

	concentrated := []Txn{
		tx(1, 0, 1, 2, 100), tx(2, 0.1, 1, 3, 100), tx(3, 0.2, 1, 4, 100), tx(4, 100, 1, 5, 100)}
	if got := maxHourShare(concentrated); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("three of four inside one hour: %g, want 0.75", got)
	}

	if got := maxHourShare(nil); got != 0 {
		t.Errorf("no transfers: %g, want 0", got)
	}
}

// Buckets are measured from the group's own first transfer rather than from a
// wall clock, so a burst that straddles midnight is not split in half by an
// accident of alignment.
func TestMaxHourShareDoesNotDependOnClockAlignment(t *testing.T) {
	burst := []Txn{tx(1, 23.9, 1, 2, 100), tx(2, 24.0, 1, 3, 100), tx(3, 24.1, 1, 4, 100)}
	if got := maxHourShare(burst); got != 1 {
		t.Errorf("a burst across a clock hour boundary: %g, want 1", got)
	}
}

// Conservation says how much of what an intermediary receives leaves again.
// This says how fast. A mule forwards within minutes; a business that happens
// to net out does it over weeks. The two together are the mule signature, and
// neither says it alone.
func TestFastForward(t *testing.T) {
	// 2 receives at h0 and forwards at h0.1. The last pair is unrelated and
	// only sets the span; account 3 must not itself become an intermediary,
	// or the test would be measuring its delay too.
	quick := []Txn{tx(1, 0, 1, 2, 1000), tx(2, 0.1, 2, 3, 1000), tx(3, 100, 4, 5, 1000)}
	if got := fastForward(quick); got < 0.99 {
		t.Errorf("an immediate forward scored %g, want close to 1", got)
	}

	// 2 sits on the money for most of the span.
	slow := []Txn{tx(1, 0, 1, 2, 1000), tx(2, 95, 2, 3, 1000), tx(3, 100, 4, 5, 1000)}
	if got := fastForward(slow); got > 0.1 {
		t.Errorf("a forward after 95 of 100 hours scored %g, want close to 0", got)
	}

	// No intermediary at all is no forwarding evidence, which is the
	// un-mule-like end — the same convention conservation uses.
	none := []Txn{tx(1, 0, 1, 2, 100), tx(2, 1, 3, 4, 100), tx(3, 2, 5, 6, 100)}
	if got := fastForward(none); got != 0 {
		t.Errorf("no intermediaries: %g, want 0", got)
	}
}

// Money cannot be forwarded before it arrives. An outflow that precedes every
// inflow is not evidence of fast forwarding and must not be counted as a
// negative delay.
func TestFastForwardIgnoresOutflowsBeforeTheInflow(t *testing.T) {
	g := []Txn{
		tx(1, 10, 2, 9, 1000), // 2 sends before it ever receives
		tx(2, 50, 1, 2, 1000), // 2 receives
		tx(3, 99, 2, 3, 1000), // and forwards late
	}
	if got := fastForward(g); got > 0.6 {
		t.Errorf("scored %g; the pre-inflow send was counted as a fast forward", got)
	}
}

func TestFastForwardOnAZeroSpan(t *testing.T) {
	g := []Txn{tx(1, 5, 1, 2, 100), tx(2, 5, 2, 3, 100), tx(3, 5, 3, 4, 100)}
	got := fastForward(g)
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("zero span produced %g", got)
	}
	if got != 1 {
		t.Errorf("everything at once forwards instantly: %g, want 1", got)
	}
}

// --------------------------------------------------------------------------
// the two feature sets, so the temporal ones can be measured against their
// own absence rather than assumed to help
// --------------------------------------------------------------------------

func TestFeatureSetsAreSelfDescribing(t *testing.T) {
	f := Features{}
	for _, set := range []FeatureSet{FeatureSetBase, FeatureSetTemporal} {
		if got, want := len(f.VectorFor(set)), len(FeatureNamesFor(set)); got != want {
			t.Errorf("%s: vector has %d entries, %d names", set, got, want)
		}
	}
	if len(FeatureNamesFor(FeatureSetTemporal)) <= len(FeatureNamesFor(FeatureSetBase)) {
		t.Error("the temporal set does not add anything to the base set")
	}
}

// The base set has to stay exactly what it was, or the before-and-after
// comparison measures a rewrite rather than the new features.
func TestTheBaseSetIsUnchanged(t *testing.T) {
	want := []string{
		"log_total_amount", "log_max_amount", "log_mean_amount",
		"conservation", "pass_through", "log_txns", "log_accounts",
		"density", "span_hours",
	}
	got := FeatureNamesFor(FeatureSetBase)
	if len(got) != len(want) {
		t.Fatalf("base set has %d features, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("feature %d is %q, want %q", i, got[i], want[i])
		}
	}
}

// The temporal set extends the base set rather than reordering it, so a
// coefficient means the same thing in both.
func TestTheTemporalSetExtendsRatherThanReplaces(t *testing.T) {
	base := FeatureNamesFor(FeatureSetBase)
	all := FeatureNamesFor(FeatureSetTemporal)
	for i := range base {
		if all[i] != base[i] {
			t.Fatalf("position %d is %q in the base set and %q in the temporal set",
				i, base[i], all[i])
		}
	}

	f := Features{TotalAmount: 100, Conservation: 0.9, Burstiness: 0.5}
	bv, av := f.VectorFor(FeatureSetBase), f.VectorFor(FeatureSetTemporal)
	for i := range bv {
		if bv[i] != av[i] {
			t.Errorf("position %d differs between sets: %g vs %g", i, bv[i], av[i])
		}
	}
}

func TestUnknownFeatureSetFallsBackToTheDefault(t *testing.T) {
	if len(FeatureNamesFor("nonsense")) != len(FeatureNamesFor(DefaultFeatureSet)) {
		t.Error("an unrecognised set did not fall back to the default")
	}
}

// A ranker and the set it was fitted on cannot be separated, so a coefficient
// can never be printed against the wrong feature name.
func TestARankerCarriesItsOwnFeatureNames(t *testing.T) {
	cands := []Candidate{
		{Features: Features{TotalAmount: 100, Conservation: 0.9, Burstiness: 0.8}},
		{Features: Features{TotalAmount: 10, Conservation: 0.1, Burstiness: -0.8}},
		{Features: Features{TotalAmount: 200, Conservation: 0.95, Burstiness: 0.7}},
	}
	labels := []bool{true, false, true}

	for _, set := range []FeatureSet{FeatureSetBase, FeatureSetTemporal} {
		r := TrainRanker(cands, labels, set, defaultOpts())
		if len(r.Weights) != len(FeatureNamesFor(set)) {
			t.Errorf("%s: %d weights against %d names", set, len(r.Weights), len(FeatureNamesFor(set)))
		}
		if len(r.Names) != len(r.Weights) {
			t.Errorf("%s: the model carries %d names for %d weights", set, len(r.Names), len(r.Weights))
		}
		// Scoring goes through the same set it was fitted on.
		if p := r.Score(cands[0]); p < 0 || p > 1 {
			t.Errorf("%s: score %g", set, p)
		}
		if got := len(r.ScoreAll(cands)); got != len(cands) {
			t.Errorf("%s: scored %d of %d candidates", set, got, len(cands))
		}
	}
}

// Every selectable set must describe itself correctly. A set whose vector and
// name list disagree mislabels every coefficient the console prints, and
// nothing else in the system checks.
func TestEverySetIsSelfDescribing(t *testing.T) {
	f := Features{}
	seen := map[int]FeatureSet{}
	for _, set := range Sets() {
		v, names := f.VectorFor(set), FeatureNamesFor(set)
		if len(v) != len(names) {
			t.Errorf("%s: %d vector entries against %d names", set, len(v), len(names))
		}
		// Every set extends the base set in place.
		for i, n := range FeatureNamesFor(FeatureSetBase) {
			if names[i] != n {
				t.Errorf("%s: position %d is %q, want %q", set, i, names[i], n)
			}
		}
		if prev, dup := seen[len(v)]; dup && prev != set {
			t.Logf("note: %s and %s are both %d wide", prev, set, len(v))
		}
		seen[len(v)] = set
	}
}

// The values a set claims to carry must actually reach the vector, or a feature
// is declared, weighted, and always zero.
func TestEachSetCarriesItsOwnFeatures(t *testing.T) {
	f := Features{Burstiness: 0.11, MaxHourShare: 0.22, FastForward: 0.33,
		RegistryShare: 0.44, Anomaly: 0.55}

	carries := func(set FeatureSet, name string, want float64) {
		t.Helper()
		names := FeatureNamesFor(set)
		v := f.VectorFor(set)
		for i, n := range names {
			if n == name {
				if v[i] != want {
					t.Errorf("%s: %s = %g, want %g", set, name, v[i], want)
				}
				return
			}
		}
		t.Errorf("%s does not carry %s", set, name)
	}

	carries(FeatureSetTemporal, "burstiness", 0.11)
	carries(FeatureSetRegistry, "registry_share", 0.44)
	carries(FeatureSetAnomaly, "anomaly", 0.55)
	carries(FeatureSetAll, "registry_share", 0.44)
	carries(FeatureSetAll, "anomaly", 0.55)

	// And a set must not carry what it does not declare.
	for _, n := range FeatureNamesFor(FeatureSetTemporal) {
		if n == "registry_share" || n == "anomaly" {
			t.Errorf("the temporal set declares %q", n)
		}
	}
}
