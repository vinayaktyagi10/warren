// Package forest is an isolation forest over candidate features.
//
// It answers a different question from the ranker, which is the only reason to
// carry both. The logistic model is supervised: it has learned what the 1,200
// ring-bearing candidates in the fitting set looked like, and it scores a new
// group by how much it resembles them. That is exactly the right instrument for
// a typology someone has already labelled, and exactly the wrong one for a
// typology nobody has. IBM AML ships eight named shapes; a ninth would score
// low under the ranker precisely because it is new.
//
// An isolation forest needs no labels at all. It measures how few random splits
// it takes to cut a point away from the rest — an anomalous group falls out in
// two or three, an ordinary one takes the full depth — so it flags "unlike the
// mass of ordinary account clusters" rather than "like the rings we were shown".
//
// The two are combined by handing the forest's score to the logistic model as
// one more feature, rather than by averaging the two or picking a threshold.
// That way the fit decides what it is worth and reports the coefficient, so the
// question "did the unsupervised half earn its place" has a published answer
// instead of an assumption. See docs/FINDINGS.md §17 for what it was worth.
//
// Two details are load-bearing and easy to get wrong. Trees are grown on a
// subsample of 256 rows rather than the whole set: with everything in one tree
// a small anomalous cluster is buried in the mass around it and stops being
// isolable. And depth is capped at ceil(log2(256)), because past that point
// every remaining path length is normal-range and the extra work only sharpens
// distinctions between ordinary points.
package forest

import (
	"math"
	"math/rand"
)

// Opts controls the forest. The defaults are the ones from Liu, Ting & Zhou
// (2008); they are unusually insensitive, which is a large part of the method's
// appeal here — there is no hyperparameter to be accused of tuning.
type Opts struct {
	Trees      int
	SampleSize int
	Seed       int64
}

func DefaultOpts() Opts { return Opts{Trees: 100, SampleSize: 256, Seed: 1} }

type node struct {
	leaf        bool
	size        int // rows reaching this leaf, for the path-length correction
	feature     int
	split       float64
	left, right *node
}

// Forest is a fitted isolation forest.
type Forest struct {
	trees []*node
	norm  float64 // c(sampleSize): the depth a normal point is expected to reach
}

// Train grows the forest. It takes no labels, which is the point.
func Train(rows [][]float64, opts Opts) *Forest {
	if len(rows) == 0 {
		return &Forest{}
	}
	if opts.Trees <= 0 {
		opts.Trees = DefaultOpts().Trees
	}
	sample := opts.SampleSize
	if sample <= 0 || sample > len(rows) {
		sample = len(rows)
	}
	limit := int(math.Ceil(math.Log2(float64(max(sample, 2)))))

	rng := rand.New(rand.NewSource(opts.Seed))
	f := &Forest{norm: avgPathLength(sample)}
	for i := 0; i < opts.Trees; i++ {
		subset := make([][]float64, sample)
		for j := range subset {
			subset[j] = rows[rng.Intn(len(rows))]
		}
		f.trees = append(f.trees, grow(subset, 0, limit, rng))
	}
	return f
}

func grow(rows [][]float64, depth, limit int, rng *rand.Rand) *node {
	if depth >= limit || len(rows) <= 1 {
		return &node{leaf: true, size: len(rows)}
	}

	// Only features that actually vary can be split on. Choosing among all of
	// them would draw a constant column, find no valid split point, and either
	// spin or send every row down one side.
	var usable []int
	for j := range rows[0] {
		lo, hi := rows[0][j], rows[0][j]
		for _, r := range rows {
			if r[j] < lo {
				lo = r[j]
			}
			if r[j] > hi {
				hi = r[j]
			}
		}
		if hi > lo {
			usable = append(usable, j)
		}
	}
	if len(usable) == 0 {
		// Every row is identical: nothing left to isolate.
		return &node{leaf: true, size: len(rows)}
	}

	feature := usable[rng.Intn(len(usable))]
	lo, hi := rows[0][feature], rows[0][feature]
	for _, r := range rows {
		if r[feature] < lo {
			lo = r[feature]
		}
		if r[feature] > hi {
			hi = r[feature]
		}
	}
	split := lo + rng.Float64()*(hi-lo)

	var left, right [][]float64
	for _, r := range rows {
		if r[feature] < split {
			left = append(left, r)
		} else {
			right = append(right, r)
		}
	}
	return &node{
		feature: feature, split: split,
		left:  grow(left, depth+1, limit, rng),
		right: grow(right, depth+1, limit, rng),
	}
}

// Score returns how anomalous a row is, on [0, 1]. Around 0.5 is an ordinary
// point; well above it is one the forest isolated quickly.
func (f *Forest) Score(row []float64) float64 {
	if len(f.trees) == 0 || f.norm <= 0 {
		return 0
	}
	var total float64
	for _, t := range f.trees {
		total += pathLength(t, row, 0)
	}
	mean := total / float64(len(f.trees))
	return math.Pow(2, -mean/f.norm)
}

// ScoreAll scores a set of rows in order.
func (f *Forest) ScoreAll(rows [][]float64) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = f.Score(r)
	}
	return out
}

func pathLength(n *node, row []float64, depth int) float64 {
	if n.leaf {
		// The tree stopped early, so the remaining depth is estimated as the
		// average an unsuccessful search would take over what is left. Without
		// this every capped branch reads as equally anomalous.
		return float64(depth) + avgPathLength(n.size)
	}
	if n.feature < len(row) && row[n.feature] < n.split {
		return pathLength(n.left, row, depth+1)
	}
	return pathLength(n.right, row, depth+1)
}

// avgPathLength is c(n), the average path length of an unsuccessful search in a
// binary search tree of n nodes. It is what turns a raw depth into a score
// comparable across trees and sample sizes.
func avgPathLength(n int) float64 {
	switch {
	case n <= 1:
		return 0
	case n == 2:
		return 1
	}
	const euler = 0.5772156649015329
	h := math.Log(float64(n-1)) + euler
	return 2*h - 2*float64(n-1)/float64(n)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
