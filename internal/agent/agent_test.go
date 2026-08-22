package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func strongEvidence() Evidence {
	return Evidence{
		RingID: 1, Typology: "CYCLE", Accounts: 6, Txns: 12,
		TotalAmount: 500_000, MaxAmount: 120_000,
		Conservation: 0.95, PassThrough: 1.0, SpanHours: 40, Score: 0.97,
	}
}

func weakEvidence() Evidence {
	return Evidence{
		RingID: 2, Typology: "MIXED", Accounts: 4, Txns: 5,
		TotalAmount: 8_000, MaxAmount: 3_000,
		Conservation: 0.05, PassThrough: 0.2, SpanHours: 60, Score: 0.10,
	}
}

func TestBlockAllowedWhenEveryGateIsCleared(t *testing.T) {
	got := DefaultPolicy().Apply(strongEvidence(),
		Proposal{Action: ActionBlock, Confidence: 0.93}, "test")

	if got.Action != ActionBlock {
		t.Errorf("action = %q, want %q", got.Action, ActionBlock)
	}
	if got.Adjusted() {
		t.Errorf("unexpected adjustments: %v", got.Adjustments)
	}
}

// The ceiling: each gate must independently be able to stop a block.
func TestBlockDowngradedByEachGate(t *testing.T) {
	tests := []struct {
		name    string
		ev      Evidence
		prop    Proposal
		wantHas string
	}{
		{
			name:    "detector score too low",
			ev:      func() Evidence { e := strongEvidence(); e.Score = 0.60; return e }(),
			prop:    Proposal{Action: ActionBlock, Confidence: 0.99},
			wantHas: "detector score",
		},
		{
			name:    "assessor not confident enough",
			ev:      strongEvidence(),
			prop:    Proposal{Action: ActionBlock, Confidence: 0.40},
			wantHas: "assessor confidence",
		},
		{
			name:    "sum above the autonomous ceiling",
			ev:      func() Evidence { e := strongEvidence(); e.TotalAmount = 50_000_000; return e }(),
			prop:    Proposal{Action: ActionBlock, Confidence: 0.99},
			wantHas: "a person decides",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultPolicy().Apply(tt.ev, tt.prop, "test")
			if got.Action != ActionHold {
				t.Errorf("action = %q, want %q", got.Action, ActionHold)
			}
			if !got.Adjusted() {
				t.Error("expected the assessment to record an adjustment")
			}
			if !strings.Contains(strings.Join(got.Adjustments, " "), tt.wantHas) {
				t.Errorf("adjustments %v, want one mentioning %q", got.Adjustments, tt.wantHas)
			}
		})
	}
}

// The floor: the envelope binds in both directions. A model that waves through
// a well-scored ring is overridden exactly as one that blocks a weak one is.
func TestAllowWithheldOnAWellScoredRing(t *testing.T) {
	got := DefaultPolicy().Apply(strongEvidence(),
		Proposal{Action: ActionAllow, Confidence: 0.99}, "test")

	if got.Action != ActionHold {
		t.Errorf("action = %q, want %q", got.Action, ActionHold)
	}
	if got.Proposed != ActionAllow {
		t.Errorf("proposed = %q, should preserve what the assessor asked for", got.Proposed)
	}
}

func TestAllowSurvivesOnAWeakRing(t *testing.T) {
	got := DefaultPolicy().Apply(weakEvidence(),
		Proposal{Action: ActionAllow, Confidence: 0.9}, "test")

	if got.Action != ActionAllow {
		t.Errorf("action = %q, want %q", got.Action, ActionAllow)
	}
	if got.Adjusted() {
		t.Errorf("unexpected adjustments: %v", got.Adjustments)
	}
}

// A hallucinated or renamed action is a malformed response. It must land on
// review, never be interpreted and never be passed through.
func TestUnknownActionFallsToReview(t *testing.T) {
	for _, bad := range []Action{"freeze_account", "", "BLOCK", "delete_customer"} {
		got := DefaultPolicy().Apply(weakEvidence(),
			Proposal{Action: bad, Confidence: 0.99}, "test")

		if got.Action != ActionHold {
			t.Errorf("action %q produced %q, want %q", bad, got.Action, ActionHold)
		}
		if !strings.Contains(strings.Join(got.Adjustments, " "), "not in the permitted set") {
			t.Errorf("action %q: adjustments %v should say it was rejected", bad, got.Adjustments)
		}
	}
}

func TestConfidenceOutOfRangeIsClamped(t *testing.T) {
	// An inflated confidence must not buy authority it did not earn: 4.2 is
	// clamped to 1, and the block still has to clear the score gate.
	got := DefaultPolicy().Apply(weakEvidence(),
		Proposal{Action: ActionBlock, Confidence: 4.2}, "test")

	if got.Confidence != 1 {
		t.Errorf("confidence = %v, want 1", got.Confidence)
	}
	if got.Action != ActionHold {
		t.Errorf("action = %q, want %q — a weak ring cannot be blocked at any confidence",
			got.Action, ActionHold)
	}
}

// --- chain behaviour ---

type failingAssessor struct{ name string }

func (f failingAssessor) Name() string { return f.name }
func (f failingAssessor) Assess(context.Context, Evidence) (Proposal, error) {
	return Proposal{}, errors.New("provider unreachable")
}

type fixedAssessor struct {
	name string
	prop Proposal
}

func (f fixedAssessor) Name() string { return f.name }
func (f fixedAssessor) Assess(context.Context, Evidence) (Proposal, error) {
	return f.prop, nil
}

func TestChainFallsThroughToAWorkingTier(t *testing.T) {
	chain := NewChain(DefaultPolicy(),
		failingAssessor{"primary"},
		fixedAssessor{"secondary", Proposal{Action: ActionHold, Confidence: 0.7, Rationale: "ok"}},
	)

	got := chain.Assess(context.Background(), strongEvidence())
	if got.Source != "secondary" {
		t.Errorf("source = %q, want the tier that answered", got.Source)
	}
	if !strings.Contains(strings.Join(got.Adjustments, " "), "primary unavailable") {
		t.Errorf("adjustments %v should record the degradation", got.Adjustments)
	}
}

// With every network tier down the system must still decide, and the decision
// must still be bounded — this is the failure the whole chain exists for.
func TestChainStillDecidesWhenEveryProviderIsDown(t *testing.T) {
	chain := NewChain(DefaultPolicy(),
		failingAssessor{"gemini"},
		failingAssessor{"ollama"},
	)

	got := chain.Assess(context.Background(), strongEvidence())
	if got.Action != ActionHold {
		t.Errorf("action = %q, want %q", got.Action, ActionHold)
	}
	if got.Source != (RuleAssessor{}).Name() {
		t.Errorf("source = %q, want the deterministic rule", got.Source)
	}
	if got.Rationale == "" {
		t.Error("a fallback decision still needs a stated reason")
	}
	joined := strings.Join(got.Adjustments, " ")
	for _, tier := range []string{"gemini", "ollama"} {
		if !strings.Contains(joined, tier) {
			t.Errorf("adjustments %v should name the failed tier %q", got.Adjustments, tier)
		}
	}
}

// The degraded path must not be able to block, however strong the evidence.
func TestFallbackNeverBlocks(t *testing.T) {
	chain := NewChain(DefaultPolicy(), failingAssessor{"gemini"})
	got := chain.Assess(context.Background(), strongEvidence())

	if got.Action == ActionBlock {
		t.Error("the deterministic fallback must never block autonomously")
	}
}

func TestChainAlwaysAppendsTheRule(t *testing.T) {
	chain := NewChain(DefaultPolicy(), fixedAssessor{"only", Proposal{Action: ActionAllow}})
	last := chain.Tiers[len(chain.Tiers)-1]
	if last.Name() != (RuleAssessor{}).Name() {
		t.Errorf("last tier = %q, want the deterministic rule", last.Name())
	}
}
