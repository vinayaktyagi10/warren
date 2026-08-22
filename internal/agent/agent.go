// Package agent turns a detected ring into an explained, bounded decision.
//
// The design rule throughout: the model proposes, the policy disposes. Anything
// a language model returns is untrusted input. It is parsed against a fixed
// schema, checked against the evidence it was given, and clamped to what the
// policy permits before it can affect anyone's money. A model that hallucinates
// an action, overstates its confidence, or is prompt-injected through a field it
// was shown cannot widen its own authority, because the authority does not live
// in the model.
//
// This is the difference between a system that uses an LLM and a system an LLM
// controls. Only the first can be operated on real payments.
package agent

import (
	"context"
	"fmt"
	"time"
)

// Action is the closed set of things the system may do about a ring. There is
// no "other": a proposal outside this set is a malformed response, not a novel
// decision.
type Action string

const (
	// ActionAllow lets the transfers stand. No money is touched.
	ActionAllow Action = "allow"

	// ActionHold routes the ring to a human investigator. Money is delayed, not
	// taken, which is why it is the safe landing spot for every downgrade.
	ActionHold Action = "hold_for_review"

	// ActionBlock stops the transfers outright. The most consequential action
	// and the most tightly gated.
	ActionBlock Action = "block"
)

// severity orders the actions so the policy can clamp in either direction.
func (a Action) severity() int {
	switch a {
	case ActionAllow:
		return 0
	case ActionHold:
		return 1
	case ActionBlock:
		return 2
	}
	return -1
}

func (a Action) valid() bool { return a.severity() >= 0 }

// Evidence is what the assessor is told about a ring. Every field is a measured
// quantity from the detector — nothing here is free text supplied by an outside
// party, which keeps the prompt free of anything an adversary could author.
type Evidence struct {
	RingID      int
	Typology    string
	Accounts    int
	Txns        int
	TotalAmount float64
	MaxAmount   float64

	// Conservation is the strongest single separator found in the data: how
	// closely value entering an intermediary leaves it again.
	Conservation float64
	PassThrough  float64
	SpanHours    float64

	// Score is the ranker's probability that this group is a ring. The policy
	// gates on this rather than on the model's own confidence, because the score
	// is calibrated against labelled outcomes and the confidence is not.
	Score float64

	WindowStart time.Time
}

// Proposal is what an assessor suggests. It is advisory.
type Proposal struct {
	Action     Action
	Confidence float64
	Rationale  string
}

// Assessment is the decision the system will actually stand behind, after the
// policy has had its say.
type Assessment struct {
	RingID     int
	Action     Action
	Confidence float64
	Rationale  string

	// Source names the tier that produced the proposal, so an audit reader can
	// tell a model's judgement from a fallback rule's.
	Source string

	// Proposed records what the tier originally asked for. When it differs from
	// Action the policy intervened, and Adjustments says why.
	Proposed    Action
	Adjustments []string

	DecidedAt time.Time
}

// Adjusted reports whether the policy overrode the proposal.
func (a Assessment) Adjusted() bool { return a.Action != a.Proposed }

// Assessor proposes an action for a ring. Implementations may fail; the chain
// handles that.
type Assessor interface {
	Name() string
	Assess(ctx context.Context, ev Evidence) (Proposal, error)
}

// Policy is the envelope. These bounds are the system's actual authority, and
// they are enforced in code rather than requested in a prompt, because a prompt
// is a suggestion and this is not.
type Policy struct {
	// BlockMinScore is the detector confidence required before blocking is even
	// available. Below it, block is downgraded to review.
	BlockMinScore float64

	// BlockMinConfidence is the assessor's own required confidence to block.
	BlockMinConfidence float64

	// BlockMaxAmount caps autonomous blocking by value. Above it a human decides,
	// however certain the machine is: the larger the sum, the worse a wrong
	// automated block is for the person on the other end of it.
	BlockMaxAmount float64

	// HoldMinScore is the score at or above which allowing is no longer
	// available. This is the other half of the envelope — the model cannot wave
	// through a group the detector scored highly, any more than it can block one
	// the detector scored low.
	HoldMinScore float64
}

func DefaultPolicy() Policy {
	return Policy{
		BlockMinScore:      0.90,
		BlockMinConfidence: 0.80,
		BlockMaxAmount:     10_000_000,
		HoldMinScore:       0.50,
	}
}

// Apply clamps a proposal to what the policy permits and records every
// intervention. It is total: it always returns a valid assessment, including
// when handed a malformed proposal.
func (p Policy) Apply(ev Evidence, prop Proposal, source string) Assessment {
	a := Assessment{
		RingID:     ev.RingID,
		Confidence: prop.Confidence,
		Rationale:  prop.Rationale,
		Source:     source,
		Proposed:   prop.Action,
		DecidedAt:  time.Now().UTC(),
	}

	action := prop.Action
	if !action.valid() {
		// An unrecognised action is a broken response, not a decision. Fall to
		// review rather than guessing at what was meant.
		a.Adjustments = append(a.Adjustments,
			fmt.Sprintf("proposed action %q is not in the permitted set; treated as review", string(prop.Action)))
		action = ActionHold
		a.Proposed = ActionHold
	}

	if prop.Confidence < 0 || prop.Confidence > 1 {
		a.Adjustments = append(a.Adjustments,
			fmt.Sprintf("confidence %.2f out of range; clamped", prop.Confidence))
		a.Confidence = clamp01(prop.Confidence)
	}

	// Ceiling: blocking must clear every gate.
	if action == ActionBlock {
		switch {
		case ev.Score < p.BlockMinScore:
			a.Adjustments = append(a.Adjustments,
				fmt.Sprintf("block requires detector score >= %.2f, ring scored %.3f", p.BlockMinScore, ev.Score))
			action = ActionHold
		case a.Confidence < p.BlockMinConfidence:
			a.Adjustments = append(a.Adjustments,
				fmt.Sprintf("block requires assessor confidence >= %.2f, stated %.2f", p.BlockMinConfidence, a.Confidence))
			action = ActionHold
		case ev.TotalAmount > p.BlockMaxAmount:
			a.Adjustments = append(a.Adjustments,
				fmt.Sprintf("ring moves %.2f, above the %.2f autonomous block ceiling; a person decides",
					ev.TotalAmount, p.BlockMaxAmount))
			action = ActionHold
		}
	}

	// Floor: a well-scored ring cannot be waved through.
	if action == ActionAllow && ev.Score >= p.HoldMinScore {
		a.Adjustments = append(a.Adjustments,
			fmt.Sprintf("allow withheld: detector score %.3f is at or above the %.2f review threshold",
				ev.Score, p.HoldMinScore))
		action = ActionHold
	}

	a.Action = action
	return a
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
