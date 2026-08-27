package score

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

func TestSigmoidIsStableAtBothExtremes(t *testing.T) {
	// The naive 1/(1+exp(-z)) overflows for large negative z. The branch exists
	// to avoid producing NaN on a confident negative, which would then be sorted
	// into the alert queue in an arbitrary position.
	for _, z := range []float64{-1000, -50, 0, 50, 1000} {
		p := sigmoid(z)
		if math.IsNaN(p) || math.IsInf(p, 0) {
			t.Fatalf("sigmoid(%g) = %g", z, p)
		}
		if p < 0 || p > 1 {
			t.Fatalf("sigmoid(%g) = %g, outside [0,1]", z, p)
		}
	}
	if math.Abs(sigmoid(0)-0.5) > 1e-12 {
		t.Errorf("sigmoid(0) = %g, want 0.5", sigmoid(0))
	}
	if sigmoid(-1000) != 0 || sigmoid(1000) != 1 {
		t.Errorf("saturation wrong: %g / %g", sigmoid(-1000), sigmoid(1000))
	}
}

// --------------------------------------------------------------------------
// standardisation
// --------------------------------------------------------------------------

func TestFitProducesZeroMeanUnitVariance(t *testing.T) {
	rows := [][]float64{{1, 100}, {2, 200}, {3, 300}, {4, 400}}
	s := Fit(rows)

	var mean, sumsq float64
	for _, r := range rows {
		v := s.Apply(r)
		mean += v[1]
		sumsq += v[1] * v[1]
	}
	mean /= float64(len(rows))
	if math.Abs(mean) > 1e-9 {
		t.Errorf("standardised mean = %g, want 0", mean)
	}
	if sd := math.Sqrt(sumsq / float64(len(rows))); math.Abs(sd-1) > 1e-9 {
		t.Errorf("standardised sd = %g, want 1", sd)
	}
}

// A feature that never varies carries no information. Dividing by its zero
// standard deviation would put Inf into the gradient and destroy every weight
// in the model, not just its own.
func TestAConstantFeatureDoesNotPoisonTheFit(t *testing.T) {
	rows := [][]float64{{1, 5}, {2, 5}, {3, 5}}
	s := Fit(rows)
	for _, r := range rows {
		for j, v := range s.Apply(r) {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("feature %d standardised to %g", j, v)
			}
		}
	}
	m := Train(rows, []bool{true, false, true}, DefaultTrainOpts())
	for j, w := range m.Weights {
		if math.IsNaN(w) || math.IsInf(w, 0) {
			t.Fatalf("weight %d = %g", j, w)
		}
	}
}

func TestFitOnNoRows(t *testing.T) {
	if s := Fit(nil); len(s.Mean) != 0 || len(s.Std) != 0 {
		t.Errorf("got %+v, want an empty standardizer", s)
	}
	m := Train(nil, nil, DefaultTrainOpts())
	if m == nil || len(m.Weights) != 0 {
		t.Errorf("training on nothing produced %+v", m)
	}
}

// --------------------------------------------------------------------------
// learning
// --------------------------------------------------------------------------

// The point of the model in one test: on a set where one feature separates the
// classes and the other is noise, the fit must find the first and ignore the
// second. This is the property the console's coefficient table claims.
func TestTrainingRecoversTheSeparatingFeatureAndIgnoresNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var rows [][]float64
	var labels []bool
	for i := 0; i < 400; i++ {
		positive := i%2 == 0
		signal := rng.NormFloat64()
		if positive {
			signal += 3
		}
		rows = append(rows, []float64{signal, rng.NormFloat64()})
		labels = append(labels, positive)
	}

	m := Train(rows, labels, DefaultTrainOpts())
	if m.Weights[0] <= 0 {
		t.Errorf("separating feature got weight %g, want positive", m.Weights[0])
	}
	if math.Abs(m.Weights[1]) >= math.Abs(m.Weights[0])/3 {
		t.Errorf("noise weight %g is not small against signal weight %g",
			m.Weights[1], m.Weights[0])
	}

	correct := 0
	for i, r := range rows {
		if (m.Predict(r) >= 0.5) == labels[i] {
			correct++
		}
	}
	if acc := float64(correct) / float64(len(rows)); acc < 0.85 {
		t.Errorf("accuracy %.3f on a separable set", acc)
	}
}

// Left unweighted, a model fitted on this data scores well by calling
// everything ordinary — one candidate in fifty is a ring. The default weight
// is the observed class ratio, and this test is what says so.
func TestClassImbalanceIsCompensatedByDefault(t *testing.T) {
	var rows [][]float64
	var labels []bool
	for i := 0; i < 500; i++ {
		positive := i%50 == 0
		v := 0.0
		if positive {
			v = 1
		}
		rows = append(rows, []float64{v})
		labels = append(labels, positive)
	}

	m := Train(rows, labels, DefaultTrainOpts())
	if p := m.Predict([]float64{1}); p < 0.5 {
		t.Errorf("a positive scored %.3f; the rare class was fitted away", p)
	}
	if p := m.Predict([]float64{0}); p > 0.5 {
		t.Errorf("a negative scored %.3f", p)
	}
}

// The alternative is what the flag exists to override, so it must actually
// change the fit.
func TestPositiveWeightIsHonouredWhenSet(t *testing.T) {
	rows := [][]float64{{0}, {0}, {0}, {1}}
	labels := []bool{false, false, false, true}

	opts := DefaultTrainOpts()
	opts.PositiveWeight = 1
	flat := Train(rows, labels, opts)

	opts.PositiveWeight = 50
	heavy := Train(rows, labels, opts)

	if heavy.Predict([]float64{1}) <= flat.Predict([]float64{1}) {
		t.Errorf("raising PositiveWeight did not raise the positive's score: %.4f vs %.4f",
			heavy.Predict([]float64{1}), flat.Predict([]float64{1}))
	}
}

// The ranker is refitted on every run and its coefficients are quoted in the
// write-up. Gradient descent from a fixed zero start on a fixed set must give
// the same answer twice, or those quoted numbers are not reproducible.
func TestFittingIsDeterministic(t *testing.T) {
	rows := [][]float64{{1, 2}, {2, 1}, {3, 4}, {4, 3}, {5, 6}}
	labels := []bool{false, false, true, true, true}

	a := Train(rows, labels, DefaultTrainOpts())
	b := Train(rows, labels, DefaultTrainOpts())
	for i := range a.Weights {
		if a.Weights[i] != b.Weights[i] {
			t.Fatalf("weight %d differs between runs: %g vs %g", i, a.Weights[i], b.Weights[i])
		}
	}
	if a.Bias != b.Bias {
		t.Fatalf("bias differs between runs: %g vs %g", a.Bias, b.Bias)
	}
}

// L2 is the only thing stopping a perfectly separable feature from running its
// weight off to infinity over 300 epochs.
func TestL2HoldsWeightsDown(t *testing.T) {
	rows := [][]float64{{-1}, {-1}, {1}, {1}}
	labels := []bool{false, false, true, true}

	loose := DefaultTrainOpts()
	loose.L2 = 0
	tight := DefaultTrainOpts()
	tight.L2 = 1

	if math.Abs(Train(rows, labels, tight).Weights[0]) >= math.Abs(Train(rows, labels, loose).Weights[0]) {
		t.Error("regularisation did not shrink the weight")
	}
}

// --------------------------------------------------------------------------
// explanation — the reason this model was chosen over something heavier
// --------------------------------------------------------------------------

func TestExplainNamesFeaturesAndOrdersByInfluence(t *testing.T) {
	m := &Model{
		Weights: []float64{0.1, -2.0, 0.5},
		Names:   []string{"small", "dominant", "middling"},
		Scaler:  &Standardizer{Mean: []float64{0, 0, 0}, Std: []float64{1, 1, 1}},
	}
	out := m.Explain()
	first := strings.Index(out, "dominant")
	second := strings.Index(out, "middling")
	third := strings.Index(out, "small")
	if first < 0 || second < 0 || third < 0 {
		t.Fatalf("a feature name is missing:\n%s", out)
	}
	if !(first < second && second < third) {
		t.Errorf("coefficients not ordered by magnitude:\n%s", out)
	}
	if !strings.Contains(out, "lowers suspicion") {
		t.Errorf("a negative coefficient must read as lowering suspicion:\n%s", out)
	}
}

// The same fitting code serves the candidate ranker and the per-transaction
// baseline, so an unnamed model of the ranker's shape falls back to the ring
// names and anything else falls back to indices rather than mislabelling.
func TestUnnamedModelsFallBackSafely(t *testing.T) {
	ring := &Model{Weights: make([]float64, len(RingFeatureNames)),
		Scaler: &Standardizer{}}
	if got := ring.featureName(3); got != RingFeatureNames[3] {
		t.Errorf("got %q, want %q", got, RingFeatureNames[3])
	}

	other := &Model{Weights: make([]float64, 3), Scaler: &Standardizer{}}
	if got := other.featureName(1); got != "feature_1" {
		t.Errorf("got %q, want feature_1", got)
	}
}
