// Package score ranks candidate rings by how much they look like laundering.
//
// Detection and ranking are separate jobs. The graph pass is built for recall:
// it finds nearly every labelled ring but raises tens of thousands of ordinary
// account clusters alongside them, because most groups of people who transact
// together are just people who transact together. Ranking is what makes the
// output usable, and it is a far healthier learning problem than scoring
// transactions directly — roughly one candidate in fifty is a ring, against one
// transfer in a thousand.
//
// The model is logistic regression rather than something heavier. With nine
// features the gain from a gradient-boosted ensemble is small, and the cost is
// real: a linear model's coefficients state exactly what it believes, which is
// what has to be defended when the system asks to hold someone's money.
package score

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// RingFeatureNames documents the candidate-level vector layout. Order matters:
// coefficients are reported against these names.
var RingFeatureNames = []string{
	"log_total_amount",
	"log_max_amount",
	"log_mean_amount",
	"conservation",
	"pass_through",
	"log_txns",
	"log_accounts",
	"density",
	"span_hours",
}

// Standardizer rescales features to zero mean and unit variance. Without it the
// amount features, which run to hundreds of millions, would dominate the
// gradient and the model would effectively ignore everything else.
type Standardizer struct {
	Mean []float64
	Std  []float64
}

func Fit(rows [][]float64) *Standardizer {
	if len(rows) == 0 {
		return &Standardizer{}
	}
	n := len(rows[0])
	s := &Standardizer{Mean: make([]float64, n), Std: make([]float64, n)}
	for _, r := range rows {
		for j, v := range r {
			s.Mean[j] += v
		}
	}
	for j := range s.Mean {
		s.Mean[j] /= float64(len(rows))
	}
	for _, r := range rows {
		for j, v := range r {
			d := v - s.Mean[j]
			s.Std[j] += d * d
		}
	}
	for j := range s.Std {
		s.Std[j] = math.Sqrt(s.Std[j] / float64(len(rows)))
		if s.Std[j] < 1e-9 {
			s.Std[j] = 1 // a constant feature contributes nothing; avoid dividing by zero
		}
	}
	return s
}

func (s *Standardizer) Apply(row []float64) []float64 {
	out := make([]float64, len(row))
	for j, v := range row {
		out[j] = (v - s.Mean[j]) / s.Std[j]
	}
	return out
}

// Model is a fitted logistic regression.
type Model struct {
	Weights []float64
	Bias    float64
	Scaler  *Standardizer

	// Names labels the coefficients for Explain. Set by the caller, because the
	// same fitting code serves both the candidate ranker and the per-transaction
	// baseline the ranker is measured against.
	Names []string
}

// TrainOpts controls fitting.
type TrainOpts struct {
	Epochs       int
	LearningRate float64
	L2           float64

	// PositiveWeight compensates for imbalance. Left at 1 the model can score
	// well by calling everything ordinary, since it usually is.
	PositiveWeight float64
}

func DefaultTrainOpts() TrainOpts {
	return TrainOpts{Epochs: 300, LearningRate: 0.1, L2: 1e-4, PositiveWeight: 0}
}

// Train fits the model by gradient descent on the weighted log loss.
func Train(rows [][]float64, labels []bool, opts TrainOpts) *Model {
	scaler := Fit(rows)
	scaled := make([][]float64, len(rows))
	for i, r := range rows {
		scaled[i] = scaler.Apply(r)
	}

	posWeight := opts.PositiveWeight
	if posWeight <= 0 {
		// Default to balancing the classes by their observed ratio.
		pos := 0
		for _, y := range labels {
			if y {
				pos++
			}
		}
		if pos > 0 {
			posWeight = float64(len(labels)-pos) / float64(pos)
		} else {
			posWeight = 1
		}
	}

	if len(rows) == 0 {
		return &Model{Scaler: scaler}
	}
	numFeatures := len(rows[0])
	m := &Model{Weights: make([]float64, numFeatures), Scaler: scaler}
	n := float64(len(scaled))

	for epoch := 0; epoch < opts.Epochs; epoch++ {
		gradW := make([]float64, numFeatures)
		gradB := 0.0

		for i, x := range scaled {
			p := sigmoid(dot(m.Weights, x) + m.Bias)
			y := 0.0
			w := 1.0
			if labels[i] {
				y = 1
				w = posWeight
			}
			err := w * (p - y)
			for j, v := range x {
				gradW[j] += err * v
			}
			gradB += err
		}

		for j := range m.Weights {
			g := gradW[j]/n + opts.L2*m.Weights[j]
			m.Weights[j] -= opts.LearningRate * g
		}
		m.Bias -= opts.LearningRate * gradB / n
	}
	return m
}

// Predict returns the probability that a candidate is a ring.
func (m *Model) Predict(row []float64) float64 {
	x := m.Scaler.Apply(row)
	return sigmoid(dot(m.Weights, x) + m.Bias)
}

// Explain lists the coefficients by magnitude. These are on standardised
// features, so they are directly comparable: the largest is the feature the
// model leans on hardest.
func (m *Model) Explain() string {
	type wf struct {
		name string
		w    float64
	}
	ws := make([]wf, 0, len(m.Weights))
	for i := range m.Weights {
		ws = append(ws, wf{m.featureName(i), m.Weights[i]})
	}
	sort.Slice(ws, func(i, j int) bool { return math.Abs(ws[i].w) > math.Abs(ws[j].w) })

	var b strings.Builder
	fmt.Fprintf(&b, "model coefficients (standardised features, bias %.3f)\n", m.Bias)
	for _, x := range ws {
		dir := "raises"
		if x.w < 0 {
			dir = "lowers"
		}
		fmt.Fprintf(&b, "  %-18s %+8.3f  %s suspicion\n", x.name, x.w, dir)
	}
	return b.String()
}

// featureName labels a coefficient, falling back to the candidate-level names
// when the shape matches and to the index when nothing else is known.
func (m *Model) featureName(i int) string {
	if i < len(m.Names) {
		return m.Names[i]
	}
	if len(m.Names) == 0 && len(m.Weights) == len(RingFeatureNames) {
		return RingFeatureNames[i]
	}
	return fmt.Sprintf("feature_%d", i)
}

func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
