package agent

import (
	"context"
	"fmt"
	"log"
	"time"
)

// RuleAssessor is the assessor of last resort: a deterministic reading of the
// detector's own score, with no network call and no dependency that can fail.
//
// Every chain ends here. A risk system that stops deciding when its model
// provider is unreachable has simply moved the outage onto the merchant, so the
// question is never whether to have a fallback but what it does. This one
// declines to block — it will hold, but an autonomous block on a degraded path
// is exactly the decision that should wait for a person.
type RuleAssessor struct {
	Policy Policy
}

func (RuleAssessor) Name() string { return "deterministic-rule" }

func (r RuleAssessor) Assess(_ context.Context, ev Evidence) (Proposal, error) {
	switch {
	case ev.Score >= r.Policy.HoldMinScore:
		return Proposal{
			Action:     ActionHold,
			Confidence: ev.Score,
			Rationale: fmt.Sprintf(
				"Automated fallback. The ranker scored this group %.3f, at or above the %.2f review threshold. "+
					"%d accounts moved %.2f across %d transfers in %.0f hours, with %.0f%% of accounts forwarding "+
					"what they received. No narrative assessment was available, so the group is held for a person "+
					"to read rather than actioned automatically.",
				ev.Score, r.Policy.HoldMinScore, ev.Accounts, ev.TotalAmount, ev.Txns,
				ev.SpanHours, 100*ev.PassThrough),
		}, nil
	default:
		return Proposal{
			Action:     ActionAllow,
			Confidence: 1 - ev.Score,
			Rationale: fmt.Sprintf(
				"Automated fallback. The ranker scored this group %.3f, below the %.2f review threshold, "+
					"so it is released without a narrative assessment.",
				ev.Score, r.Policy.HoldMinScore),
		}, nil
	}
}

// Chain tries each assessor in order and uses the first that answers, then
// applies the policy to whatever came back.
//
// Assess never returns an error. A bounded decision is always produced, because
// the alternative — no decision — is itself a decision, made silently and
// without a record.
type Chain struct {
	Tiers   []Assessor
	Policy  Policy
	Timeout time.Duration

	// OnDegrade is called whenever a tier fails and the chain moves down. The
	// audit log subscribes to this: a decision made on the fallback path must be
	// distinguishable afterwards from one made on the primary path, or the audit
	// trail records a confidence the system did not actually have.
	OnDegrade func(ringID int, tier string, err error)
}

// NewChain builds a chain that always terminates in the deterministic rule,
// appending it if the caller did not.
func NewChain(policy Policy, tiers ...Assessor) *Chain {
	if len(tiers) == 0 || tiers[len(tiers)-1].Name() != (RuleAssessor{}).Name() {
		tiers = append(tiers, RuleAssessor{Policy: policy})
	}
	return &Chain{
		Tiers:   tiers,
		Policy:  policy,
		Timeout: 30 * time.Second,
		OnDegrade: func(ringID int, tier string, err error) {
			log.Printf("ring %d: %s unavailable (%v), falling through", ringID, tier, err)
		},
	}
}

func (c *Chain) Assess(ctx context.Context, ev Evidence) Assessment {
	var degradations []string

	for _, tier := range c.Tiers {
		tctx := ctx
		var cancel context.CancelFunc
		if c.Timeout > 0 {
			tctx, cancel = context.WithTimeout(ctx, c.Timeout)
		}
		prop, err := tier.Assess(tctx, ev)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			if c.OnDegrade != nil {
				c.OnDegrade(ev.RingID, tier.Name(), err)
			}
			degradations = append(degradations,
				fmt.Sprintf("%s unavailable: %v", tier.Name(), err))
			continue
		}

		a := c.Policy.Apply(ev, prop, tier.Name())
		// Degradations are recorded on the assessment itself, not only in logs,
		// so the reason a lesser tier answered travels with the decision.
		a.Adjustments = append(degradations, a.Adjustments...)
		return a
	}

	// Unreachable while the chain ends in RuleAssessor, but a risk system should
	// not depend on that invariant holding after a future edit.
	return Assessment{
		RingID:      ev.RingID,
		Action:      ActionHold,
		Confidence:  0,
		Rationale:   "No assessor produced a decision. Held for review by default.",
		Source:      "chain-exhausted",
		Proposed:    ActionHold,
		Adjustments: append(degradations, "every assessor failed; defaulted to review"),
		DecidedAt:   time.Now().UTC(),
	}
}
