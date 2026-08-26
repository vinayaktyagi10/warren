// Package enforce turns a decision into an action, and bounds the action
// separately from the decision that authorised it.
//
// Up to this point WARREN produces an opinion: a ring, an explanation, and one
// of three actions, recorded tamper-evidently. Nothing moves. This package is
// where the system finally touches money, which makes it the place where being
// wrong costs someone something real, so the rules are deliberately narrow:
//
//	Only a policy-approved block stops money. A hold produces a watch, which is
//	visible and expires and stops nothing. The gates the agent policy already
//	applies to blocking are therefore the gates on enforcement, and there is no
//	second, looser path to freezing an account.
//
//	Every restriction expires. An automated decision may not impose anything
//	permanent, so the register is a set of leases, not a blacklist.
//
//	One decision may not freeze an unbounded number of people. Past the cap the
//	system declines to act at all rather than freezing an arbitrary subset of a
//	large ring, because "these 25 of your 59" is not a defensible choice.
//
//	Lifting is a first-class operation. A bounded action that cannot be undone
//	before its expiry is not bounded, it is just slow.
package enforce

import (
	"fmt"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/detect"
)

// Tier is what a restriction is allowed to do.
type Tier string

const (
	// TierFrozen stops transfers out of the account. Reachable only from a block
	// that already cleared every gate in the agent policy.
	TierFrozen Tier = "frozen"

	// TierWatch stops nothing. It records that the account was part of a group
	// routed to a human, so the next analyst to see it knows, and so a later
	// candidate containing it can be surfaced. Money is untouched.
	TierWatch Tier = "watch"
)

// stops reports whether this tier prevents a transfer.
func (t Tier) stops() bool { return t == TierFrozen }

// Restriction is one bounded lease over one account.
type Restriction struct {
	Account int32
	Tier    Tier

	// RingID and DecisionSeq tie the restriction back to the decision that
	// authorised it and the audit entry that recorded it. A restriction whose
	// authority cannot be traced is not one this system is willing to impose.
	RingID      int
	DecisionSeq int64
	Reason      string

	// Pass names the detection geometry that raised the ring. Two passes with
	// different window widths carry different evidential weight, and a lease
	// that cannot say which one imposed it cannot justify its own length.
	Pass string

	Imposed time.Time
	Expires time.Time

	// Lifted is set when the restriction was released early. Lifting does not
	// delete the row: the record that money was held, and then released, is
	// exactly what an audit needs to see.
	Lifted     time.Time
	LiftReason string
}

// Active reports whether the restriction is in force at t. The window is
// half-open: a restriction imposed at its start instant is in force, and at its
// expiry instant it is not, so a 72-hour lease holds money for 72 hours and not
// a moment longer.
func (r Restriction) Active(t time.Time) bool {
	if !r.Lifted.IsZero() && !t.Before(r.Lifted) {
		return false
	}
	return !t.Before(r.Imposed) && t.Before(r.Expires)
}

// Limits bound the action itself, on top of whatever bounded the decision.
type Limits struct {
	// MaxAccountsPerRing caps the blast radius of a single automated decision.
	// The largest labelled ring here holds 45 accounts; a cap below that is
	// deliberate, because a group big enough to need a larger action is big
	// enough to deserve a person.
	MaxAccountsPerRing int

	// FrozenFor is how long a freeze lasts. One detection window: the hold is
	// bounded by the same span of evidence that produced it.
	FrozenFor time.Duration

	// WatchFor is how long a watch lasts. Longer than a freeze because it costs
	// nobody anything — it is a note, not a hold.
	WatchFor time.Duration

	// Pass names the detection geometry these limits belong to. Lease length is
	// the lever that makes evidence weight visible: a pass that sees 24 hours
	// before acting should not buy the same 72-hour hold as one that saw 72.
	Pass string
}

func DefaultLimits() Limits {
	return Limits{
		MaxAccountsPerRing: 25,
		FrozenFor:          72 * time.Hour,
		WatchFor:           14 * 24 * time.Hour,
	}
}

// Restrictions renders a decision into the leases it authorises, effective at
// the moment the decision was taken. It returns the bounds that fired alongside,
// in the same spirit as the policy's adjustments: an action the system declined
// to take is a fact about the system, and recording it is the difference between
// a bound and a bug.
func Restrictions(a agent.Assessment, c detect.Candidate, effective time.Time, lim Limits) ([]Restriction, []string) {
	var tier Tier
	var ttl time.Duration
	switch a.Action {
	case agent.ActionBlock:
		tier, ttl = TierFrozen, lim.FrozenFor
	case agent.ActionHold:
		tier, ttl = TierWatch, lim.WatchFor
	default:
		// Allow touches nothing, and an action outside the set never reaches
		// here — the policy has already turned it into a hold.
		return nil, nil
	}

	var bounds []string
	if tier.stops() && len(c.Accounts) > lim.MaxAccountsPerRing {
		bounds = append(bounds, fmt.Sprintf(
			"declined to freeze: ring spans %d accounts, above the %d-account limit on a single automated action; a person decides",
			len(c.Accounts), lim.MaxAccountsPerRing))
		return nil, bounds
	}

	reason := fmt.Sprintf("ring %d, %s", a.RingID, a.Action)
	out := make([]Restriction, 0, len(c.Accounts))
	for _, acct := range c.Accounts {
		out = append(out, Restriction{
			Account: acct,
			Tier:    tier,
			RingID:  a.RingID,
			Pass:    lim.Pass,
			Reason:  reason,
			Imposed: effective,
			Expires: effective.Add(ttl),
		})
	}
	return out, bounds
}

// Register is the live restriction ledger: which accounts are under a lease
// right now, and why.
//
// It is a hash map from account to that account's current strongest lease, with
// expiry checked at lookup rather than swept in the background. Lazy expiry is
// safe here because every consumer walks the ledger forward in time, so a stale
// entry can never be consulted before the instant it lapsed — and it keeps the
// per-transfer check to a single map lookup, which matters when the check runs
// against every transfer in the ledger. A heap keyed on expiry would evict
// eagerly and buy nothing at this scale.
//
// History is kept separately and never overwritten, because "this account was
// frozen for 40 hours and then released" is the sentence an audit needs and a
// map of current state cannot say it.
type Register struct {
	current map[int32]Restriction
	history []Restriction
}

// ImposeResult says what a lease did to the register. It is reported rather than
// discarded because an extension is the observable event in an escalating
// design: a short lease from a fast, thin-evidence pass becoming a long one when
// a slower pass confirms the same accounts is the mechanism working, and a
// system that cannot count it cannot show it happened.
type ImposeResult int

const (
	ImposeNew      ImposeResult = iota // the account was unleased
	ImposeExtended                     // an existing lease was strengthened or lengthened
	ImposeIgnored                      // the existing lease already protected at least as well
)

// Impose records a lease. Where an account is already leased, the one that
// protects for longer wins, and a watch never displaces a live freeze: an
// account caught in two rings should not become easier to use because the
// second one was judged less harshly.
//
// That rule is also what makes escalation work without any special case. A fast
// pass acting on 24 hours of evidence imposes a short freeze; when a slower pass
// later confirms the same accounts on 72 hours of evidence, its longer lease
// simply wins. If the confirmation never comes, the short lease lapses on its own
// and nobody has to remember to release it.
func (r *Register) Impose(x Restriction) ImposeResult {
	if r.current == nil {
		r.current = make(map[int32]Restriction)
	}
	r.history = append(r.history, x)

	prev, ok := r.current[x.Account]
	if !ok {
		r.current[x.Account] = x
		return ImposeNew
	}
	switch {
	case prev.Tier.stops() && !x.Tier.stops():
		// A watch cannot weaken or extend a freeze.
		return ImposeIgnored
	case !prev.Tier.stops() && x.Tier.stops():
		r.current[x.Account] = x
		return ImposeExtended
	case x.Expires.After(prev.Expires):
		r.current[x.Account] = x
		return ImposeExtended
	}
	return ImposeIgnored
}

// Source names the detection pass a lease came from, so an audit reader can tell
// a freeze imposed on 24 hours of evidence from one imposed on 72.
func (x Restriction) Source() string { return x.Pass }

// Stopped reports whether a transfer out of this account at this instant is
// prevented, and by what.
func (r *Register) Stopped(account int32, t time.Time) (Restriction, bool) {
	x, ok := r.current[account]
	if !ok || !x.Active(t) || !x.Tier.stops() {
		return Restriction{}, false
	}
	return x, true
}

// Lookup returns the account's current lease of any tier, stopping or not.
func (r *Register) Lookup(account int32, t time.Time) (Restriction, bool) {
	x, ok := r.current[account]
	if !ok || !x.Active(t) {
		return Restriction{}, false
	}
	return x, true
}

// Lift releases a lease early, reporting whether there was one to release.
func (r *Register) Lift(account int32, t time.Time, reason string) bool {
	x, ok := r.current[account]
	if !ok || !x.Active(t) {
		return false
	}
	x.Lifted, x.LiftReason = t, reason
	r.current[account] = x
	r.history = append(r.history, x)
	return true
}

// History returns every lease ever imposed, in order, including the ones that
// were later lifted.
func (r *Register) History() []Restriction { return r.history }

// ActiveAt lists the leases in force at t.
func (r *Register) ActiveAt(t time.Time) []Restriction {
	var out []Restriction
	for _, x := range r.current {
		if x.Active(t) {
			out = append(out, x)
		}
	}
	return out
}
