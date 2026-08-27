package forest

import (
	"math/rand"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/detect"
)

// The forest's input must not contain the registry share. A group whose
// accounts are on a suspect list is unusual by construction, so including it
// would let the forest rediscover the list and report it back as novelty.
func TestTheForestDoesNotSeeTheRegistryOrItsOwnOutput(t *testing.T) {
	names := detect.FeatureNamesFor(InputSet)
	for _, n := range names {
		if n == "registry_share" || n == "anomaly" {
			t.Errorf("the forest's input space includes %q", n)
		}
	}
}

// Every candidate comes back scored, and an unusual group scores above the mass.
func TestAnnotateScoresEveryCandidate(t *testing.T) {
	// The mass has to vary across several features, not one. With a single
	// varying column the forest can only split on it, and a point outside the
	// training range then follows the extreme branch to an ordinary depth —
	// which is a real property of the method, not a defect of the fixture.
	rng := rand.New(rand.NewSource(7))
	var train []detect.Candidate
	for i := 0; i < 400; i++ {
		train = append(train, detect.Candidate{
			Window: time.Now(),
			Features: detect.Features{
				Txns: 3 + rng.Intn(5), Accounts: 3 + rng.Intn(4),
				TotalAmount:      1000 + rng.Float64()*4000,
				MaxAmount:        500 + rng.Float64()*1500,
				Conservation:     rng.Float64() * 0.4,
				PassThroughRatio: rng.Float64() * 0.4,
				Density:          0.8 + rng.Float64()*0.6,
				SpanHours:        rng.Float64() * 40,
				Burstiness:       -0.6 + rng.Float64()*0.4,
				FastForward:      rng.Float64() * 0.3,
			},
		})
	}
	odd := detect.Candidate{Features: detect.Features{
		Txns: 90, Accounts: 55, TotalAmount: 9e9, MaxAmount: 9e9,
		Conservation: 0.99, PassThroughRatio: 1, Density: 20, SpanHours: 0.1,
		Burstiness: 1, FastForward: 1,
	}}
	all := append(append([]detect.Candidate(nil), train...), odd)

	Annotate(train, all, DefaultOpts())

	for i, c := range all {
		if c.Features.Anomaly <= 0 || c.Features.Anomaly > 1 {
			t.Fatalf("candidate %d scored %g", i, c.Features.Anomaly)
		}
	}
	ordinary := all[0].Features.Anomaly
	unusual := all[len(all)-1].Features.Anomaly
	if unusual <= ordinary {
		t.Errorf("the odd group scored %.3f against an ordinary %.3f", unusual, ordinary)
	}
}
