package detect

import (
	"math"
	"testing"
)

// Three candidate features for the bipartite hole, tested before they exist.
//
// The motivating measurement is in docs/FINDINGS.md §18: every one of the 5,297
// candidates classify() names BIPARTITE has conservation, pass_through and
// fast_forward at exactly zero, because all three are defined over accounts
// that both receive and send and a bipartite group has none. Anything that
// could speak about such a group has to be built from the two partitions and
// from the edges between them, never from an intermediary.
//
// These tests pin what each feature claims. Whether the claims are worth
// anything is a separate question, settled by measurement and not here.

// --------------------------------------------------------------------------
// partition_balance — how evenly the one-sided accounts split
// --------------------------------------------------------------------------

// A ring of disjoint sender->receiver pairs — which is what a labelled
// BIPARTITE ring in IBM AML actually is — has as many senders as receivers, so
// the two sides balance exactly.
func TestPartitionBalanceIsOneWhenTheSidesMatch(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 3, 4, 1000),
		tx(3, 2, 5, 6, 1000),
	})
	if math.Abs(f.PartitionBalance-1) > 1e-9 {
		t.Errorf("partition balance %g over 3 senders and 3 receivers, want 1", f.PartitionBalance)
	}
}

// A fan is the lopsided case: one account against many. The feature has to say
// so, or it cannot tell a two-sided structure from a star.
func TestPartitionBalanceIsLowForAFan(t *testing.T) {
	fanOut := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 1, 3, 1000),
		tx(3, 2, 1, 4, 1000),
	})
	want := 2.0 * 1 / 4 // one sender against three receivers
	if math.Abs(fanOut.PartitionBalance-want) > 1e-9 {
		t.Errorf("fan-out balance %g, want %g", fanOut.PartitionBalance, want)
	}

	// And it must not depend on which way the fan points.
	fanIn := featuresOf([]Txn{
		tx(1, 0, 2, 1, 1000),
		tx(2, 1, 3, 1, 1000),
		tx(3, 2, 4, 1, 1000),
	})
	if math.Abs(fanIn.PartitionBalance-fanOut.PartitionBalance) > 1e-9 {
		t.Errorf("fan-in %g against fan-out %g: the feature reads direction",
			fanIn.PartitionBalance, fanOut.PartitionBalance)
	}
}

// A cycle has no one-sided accounts at all. There are no partitions to balance,
// so the honest answer is 0 — the same convention conservation and fast_forward
// use for "no evidence", rather than 1 for "trivially balanced".
func TestPartitionBalanceIsZeroWhenThereAreNoPartitions(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 3, 1000),
		tx(3, 2, 3, 1, 1000),
	})
	if f.PartitionBalance != 0 {
		t.Errorf("partition balance %g on a pure cycle, want 0", f.PartitionBalance)
	}
}

// The feature has to say something pass_through does not, or it is a second
// name for a number the model already has. These two groups have the same
// share of intermediaries and different balance.
func TestPartitionBalanceIsNotPassThroughRestated(t *testing.T) {
	// 1->2->3 plus a disjoint pair: intermediary 2, one-sided {1,4} vs {3,5}.
	even := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 3, 1000),
		tx(3, 2, 4, 5, 1000),
	})
	// 1->2->3 plus 4->3: intermediary 2, one-sided {1,4} vs {3}.
	lopsided := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 3, 1000),
		tx(3, 2, 4, 3, 1000),
	})
	if math.Abs(even.PassThroughRatio-lopsided.PassThroughRatio) > 0.2 {
		t.Fatalf("the two groups differ too much in pass_through (%g vs %g) for this to be a fair test",
			even.PassThroughRatio, lopsided.PassThroughRatio)
	}
	if even.PartitionBalance <= lopsided.PartitionBalance {
		t.Errorf("balance %g (even) is not above %g (lopsided): the feature adds nothing to pass_through",
			even.PartitionBalance, lopsided.PartitionBalance)
	}
}

// --------------------------------------------------------------------------
// pair_reuse — how often the same relationship carries money twice
// --------------------------------------------------------------------------

// A generated ring uses each sender->receiver relationship once. Ordinary
// counterparties transact repeatedly. The feature is the share of transfers
// that are not the first on their own pair.
func TestPairReuseIsZeroWhenEveryPairIsUsedOnce(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 3, 4, 1000),
		tx(3, 2, 5, 6, 1000),
	})
	if f.PairReuse != 0 {
		t.Errorf("pair reuse %g over three distinct pairs, want 0", f.PairReuse)
	}
}

func TestPairReuseCountsRepeatsOnly(t *testing.T) {
	// Three transfers, one pair: two of the three are repeats.
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 1, 2, 1000),
		tx(3, 2, 1, 2, 1000),
	})
	if math.Abs(f.PairReuse-2.0/3.0) > 1e-9 {
		t.Errorf("pair reuse %g, want 2/3", f.PairReuse)
	}
}

// Direction is part of the relationship: A paying B is not B paying A, and a
// feature that conflated them would call a two-way settlement a repeat.
func TestPairReuseIsDirected(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 1, 1000),
		tx(3, 2, 1, 3, 1000),
	})
	if f.PairReuse != 0 {
		t.Errorf("pair reuse %g: A->B and B->A were counted as the same pair", f.PairReuse)
	}
}

// density is transfers per account and would also rise on a repeating pair.
// The two have to come apart somewhere, or the model gains nothing.
func TestPairReuseIsNotDensityRestated(t *testing.T) {
	// Six transfers over six accounts in distinct pairs: density 1, reuse 0.
	distinct := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000), tx(2, 1, 3, 4, 1000), tx(3, 2, 5, 6, 1000),
		tx(4, 3, 1, 4, 1000), tx(5, 4, 3, 6, 1000), tx(6, 5, 5, 2, 1000),
	})
	if distinct.PairReuse != 0 {
		t.Errorf("pair reuse %g over six distinct pairs, want 0", distinct.PairReuse)
	}
	if distinct.Density != 1 {
		t.Fatalf("density %g, want 1 — the fixture is wrong", distinct.Density)
	}
	// Same density, same account count, but every transfer doubles a pair.
	repeated := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000), tx(2, 1, 1, 2, 1000), tx(3, 2, 3, 4, 1000),
		tx(4, 3, 3, 4, 1000), tx(5, 4, 5, 6, 1000), tx(6, 5, 5, 6, 1000),
	})
	if repeated.Density != distinct.Density {
		t.Fatalf("densities differ (%g vs %g) — the fixture is wrong",
			repeated.Density, distinct.Density)
	}
	if repeated.PairReuse <= distinct.PairReuse {
		t.Errorf("reuse %g against %g at equal density: the feature is density restated",
			repeated.PairReuse, distinct.PairReuse)
	}
}

// --------------------------------------------------------------------------
// amount_uniformity — whether the group moves one size of payment
// --------------------------------------------------------------------------

func TestAmountUniformityIsOneWhenEveryAmountMatches(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 5000),
		tx(2, 1, 3, 4, 5000),
		tx(3, 2, 5, 6, 5000),
	})
	if math.Abs(f.AmountUniformity-1) > 1e-9 {
		t.Errorf("amount uniformity %g on identical amounts, want 1", f.AmountUniformity)
	}
}

func TestAmountUniformityFallsAsAmountsScatter(t *testing.T) {
	tight := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000), tx(2, 1, 3, 4, 1010), tx(3, 2, 5, 6, 990),
	})
	loose := featuresOf([]Txn{
		tx(1, 0, 1, 2, 10), tx(2, 1, 3, 4, 1000), tx(3, 2, 5, 6, 100000),
	})
	if tight.AmountUniformity <= loose.AmountUniformity {
		t.Errorf("tight %g is not above loose %g", tight.AmountUniformity, loose.AmountUniformity)
	}
	if loose.AmountUniformity < 0 || loose.AmountUniformity > 1 {
		t.Errorf("uniformity %g outside [0, 1]", loose.AmountUniformity)
	}
}

// The feature has to be about spread and not about size, or it becomes a
// fourth amount feature and the model already has three. Multiplying every
// amount by a thousand must leave it untouched.
func TestAmountUniformityIsScaleFree(t *testing.T) {
	small := featuresOf([]Txn{
		tx(1, 0, 1, 2, 100), tx(2, 1, 3, 4, 250), tx(3, 2, 5, 6, 700),
	})
	large := featuresOf([]Txn{
		tx(1, 0, 1, 2, 100000), tx(2, 1, 3, 4, 250000), tx(3, 2, 5, 6, 700000),
	})
	if math.Abs(small.AmountUniformity-large.AmountUniformity) > 1e-9 {
		t.Errorf("uniformity %g against %g on the same amounts scaled 1000x: the feature reads size",
			small.AmountUniformity, large.AmountUniformity)
	}
}

// Nothing to compare is not the same as everything matching.
func TestAmountUniformityOnAmountlessTransfers(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 0), tx(2, 1, 3, 4, 0), tx(3, 2, 5, 6, 0),
	})
	if f.AmountUniformity != 0 {
		t.Errorf("amount uniformity %g where there are no amounts, want 0", f.AmountUniformity)
	}
}

// --------------------------------------------------------------------------
// the point of the exercise
// --------------------------------------------------------------------------

// The reason these three exist: on a group with no intermediaries the model's
// three strongest features are all exactly zero, and these three are not. If
// this test ever fails, the features have stopped being about the structure
// they were built for.
func TestTheNewFeaturesSpeakWhereTheOldOnesAreSilent(t *testing.T) {
	// Disjoint sender->receiver pairs: no account both sends and receives.
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 5000),
		tx(2, 1, 3, 4, 5000),
		tx(3, 2, 5, 6, 5000),
	})
	for _, silent := range []struct {
		name string
		v    float64
	}{
		{"conservation", f.Conservation},
		{"pass_through", f.PassThroughRatio},
		{"fast_forward", f.FastForward},
	} {
		if silent.v != 0 {
			t.Errorf("%s = %g on a group with no intermediaries, want 0", silent.name, silent.v)
		}
	}
	for _, speaks := range []struct {
		name string
		v    float64
	}{
		{"partition_balance", f.PartitionBalance},
		{"pair_reuse", f.PairReuse},
		{"amount_uniformity", f.AmountUniformity},
	} {
		if speaks.name != "pair_reuse" && speaks.v == 0 {
			t.Errorf("%s = 0 on a bipartite group: it is as silent as the features it replaces", speaks.name)
		}
	}
	// pair_reuse is 0 here for a reason and that reason is the signal: a ring
	// of disjoint pairs never reuses a relationship. It is checked against
	// ordinary repeating traffic instead.
	ordinary := featuresOf([]Txn{
		tx(1, 0, 1, 2, 5000), tx(2, 1, 1, 2, 5000), tx(3, 2, 3, 4, 5000),
	})
	if ordinary.PairReuse <= f.PairReuse {
		t.Errorf("pair reuse does not separate repeated relationships (%g) from one-shot pairs (%g)",
			ordinary.PairReuse, f.PairReuse)
	}
}

// --------------------------------------------------------------------------
// the feature sets
// --------------------------------------------------------------------------

// The experimental sets exist so each new feature can be measured on its own
// against the temporal baseline. A set that carried all three at once would
// only ever say whether the bundle helped.
func TestTheBipartiteSetsExtendTemporalWithoutChangingIt(t *testing.T) {
	temporal := FeatureNamesFor(FeatureSetTemporal)
	for _, set := range []FeatureSet{
		FeatureSetBipBalance, FeatureSetBipReuse, FeatureSetBipUniform, FeatureSetBipartite,
	} {
		names := FeatureNamesFor(set)
		if len(names) <= len(temporal) {
			t.Errorf("%s carries %d features, temporal carries %d", set, len(names), len(temporal))
			continue
		}
		for i, n := range temporal {
			if names[i] != n {
				t.Errorf("%s position %d is %q, want %q", set, i, names[i], n)
			}
		}
	}
	// And the baseline itself is untouched by their existence.
	if len(temporal) != 12 {
		t.Errorf("the temporal set is %d features wide, want 12", len(temporal))
	}
	if len(FeatureNamesFor(FeatureSetBase)) != 9 {
		t.Errorf("the base set is %d features wide, want 9", len(FeatureNamesFor(FeatureSetBase)))
	}
}

func TestEachBipartiteSetCarriesOnlyItsOwnFeature(t *testing.T) {
	f := Features{PartitionBalance: 0.61, PairReuse: 0.62, AmountUniformity: 0.63}

	has := func(set FeatureSet, name string) (float64, bool) {
		names, v := FeatureNamesFor(set), f.VectorFor(set)
		for i, n := range names {
			if n == name {
				return v[i], true
			}
		}
		return 0, false
	}
	cases := []struct {
		set  FeatureSet
		name string
		want float64
	}{
		{FeatureSetBipBalance, "partition_balance", 0.61},
		{FeatureSetBipReuse, "pair_reuse", 0.62},
		{FeatureSetBipUniform, "amount_uniformity", 0.63},
		{FeatureSetBipartite, "partition_balance", 0.61},
		{FeatureSetBipartite, "pair_reuse", 0.62},
		{FeatureSetBipartite, "amount_uniformity", 0.63},
	}
	for _, c := range cases {
		got, ok := has(c.set, c.name)
		if !ok {
			t.Errorf("%s does not carry %s", c.set, c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %s = %g, want %g", c.set, c.name, got, c.want)
		}
	}
	// An ablation set carries one new feature and not the other two.
	for _, absent := range []struct {
		set  FeatureSet
		name string
	}{
		{FeatureSetBipBalance, "pair_reuse"},
		{FeatureSetBipBalance, "amount_uniformity"},
		{FeatureSetBipReuse, "partition_balance"},
		{FeatureSetBipUniform, "pair_reuse"},
	} {
		if _, ok := has(absent.set, absent.name); ok {
			t.Errorf("%s carries %s, which is another arm of the ablation", absent.set, absent.name)
		}
	}
	// The shipped sets must not have quietly acquired them.
	for _, set := range []FeatureSet{FeatureSetBase, FeatureSetTemporal, FeatureSetRegistry, FeatureSetAnomaly, FeatureSetAll} {
		for _, n := range []string{"partition_balance", "pair_reuse", "amount_uniformity"} {
			if _, ok := has(set, n); ok {
				t.Errorf("%s carries the experimental feature %s", set, n)
			}
		}
	}
}
