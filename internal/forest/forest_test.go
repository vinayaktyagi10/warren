package forest

import (
	"math"
	"math/rand"
	"testing"
)

// The property the whole method rests on: a point far from the mass is isolated
// by fewer random splits than a point inside it, so its expected path length is
// shorter and its score higher.
func TestAnOutlierScoresAboveTheCrowd(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var rows [][]float64
	for i := 0; i < 500; i++ {
		rows = append(rows, []float64{rng.NormFloat64(), rng.NormFloat64()})
	}
	f := Train(rows, DefaultOpts())

	inlier := f.Score([]float64{0, 0})
	outlier := f.Score([]float64{20, -20})
	if outlier <= inlier {
		t.Errorf("outlier scored %.3f, inlier %.3f", outlier, inlier)
	}
	if outlier < 0.6 {
		t.Errorf("a point twenty standard deviations out scored only %.3f", outlier)
	}
}

// Scores are on a fixed [0,1] scale so they mean the same thing across runs and
// can be handed to a linear model as a feature without rescaling surprises.
func TestScoresStayInRange(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var rows [][]float64
	for i := 0; i < 300; i++ {
		rows = append(rows, []float64{rng.NormFloat64(), rng.Float64() * 1000})
	}
	f := Train(rows, DefaultOpts())

	for _, probe := range [][]float64{
		{0, 0}, {1e9, 1e9}, {-1e9, -1e9}, {math.SmallestNonzeroFloat64, 500},
	} {
		s := f.Score(probe)
		if math.IsNaN(s) || s < 0 || s > 1 {
			t.Errorf("Score(%v) = %g", probe, s)
		}
	}
}

// Every number this project publishes has to survive being produced twice.
func TestTrainingIsReproducible(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var rows [][]float64
	for i := 0; i < 200; i++ {
		rows = append(rows, []float64{rng.NormFloat64(), rng.NormFloat64()})
	}
	opts := DefaultOpts()

	a, b := Train(rows, opts), Train(rows, opts)
	probe := []float64{1.5, -0.5}
	if a.Score(probe) != b.Score(probe) {
		t.Errorf("same seed gave %.10f and %.10f", a.Score(probe), b.Score(probe))
	}

	opts.Seed = 99
	if c := Train(rows, opts); c.Score(probe) == a.Score(probe) {
		t.Error("a different seed gave an identical score")
	}
}

// c(n) is the average path length of an unsuccessful search in a binary search
// tree, and it is what normalises depth into a score. Getting it wrong does not
// fail loudly — it tilts every score in the same direction.
func TestAveragePathLength(t *testing.T) {
	if got := avgPathLength(1); got != 0 {
		t.Errorf("c(1) = %g, want 0", got)
	}
	if got := avgPathLength(2); got != 1 {
		t.Errorf("c(2) = %g, want 1", got)
	}
	// c(256) ~= 2*(ln 255 + gamma) - 2*255/256
	want := 2*(math.Log(255)+0.5772156649) - 2*255/256.0
	if got := avgPathLength(256); math.Abs(got-want) > 1e-9 {
		t.Errorf("c(256) = %g, want %g", got, want)
	}
	if avgPathLength(1000) <= avgPathLength(100) {
		t.Error("c(n) must grow with n")
	}
}

// A constant column has no split to make. Left unhandled it is an infinite loop
// or a divide by zero, depending on where the split point is drawn.
func TestConstantAndDegenerateInputs(t *testing.T) {
	rows := [][]float64{{1, 5}, {2, 5}, {3, 5}, {4, 5}}
	f := Train(rows, DefaultOpts())
	if s := f.Score([]float64{2.5, 5}); math.IsNaN(s) || s < 0 || s > 1 {
		t.Errorf("score with a constant column = %g", s)
	}

	// Every row identical: nothing can be isolated, and every score must be
	// finite rather than a division by a zero path length.
	same := [][]float64{{1, 1}, {1, 1}, {1, 1}}
	g := Train(same, DefaultOpts())
	if s := g.Score([]float64{1, 1}); math.IsNaN(s) || s < 0 || s > 1 {
		t.Errorf("score on identical rows = %g", s)
	}

	if h := Train(nil, DefaultOpts()); h.Score([]float64{1}) != 0 {
		t.Error("a forest trained on nothing claimed to know something")
	}
}

// Subsampling is not an optimisation here. A tree grown on the whole set drowns
// small anomalous clusters in the mass around them; 256 rows per tree is what
// keeps them isolable, and it also bounds the depth.
func TestTreesSubsampleAndStayShallow(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	var rows [][]float64
	for i := 0; i < 5000; i++ {
		rows = append(rows, []float64{rng.NormFloat64()})
	}
	opts := DefaultOpts()
	opts.SampleSize = 256
	f := Train(rows, opts)

	if len(f.trees) != opts.Trees {
		t.Fatalf("grew %d trees, want %d", len(f.trees), opts.Trees)
	}
	limit := int(math.Ceil(math.Log2(256)))
	for i, tr := range f.trees {
		if d := depth(tr); d > limit {
			t.Errorf("tree %d is %d deep, past the %d limit", i, d, limit)
		}
	}
}

func depth(n *node) int {
	if n == nil || n.leaf {
		return 0
	}
	l, r := depth(n.left), depth(n.right)
	if l > r {
		return l + 1
	}
	return r + 1
}
