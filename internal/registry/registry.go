// Package registry models a shared list of suspect accounts, of the kind India's
// I4C operates: 32.08 lakh mule accounts reported by victims and banks, against
// which ₹25,698 crore of transfers have been declined.
//
// It is here to answer one question, and the question is the project's thesis
// stated in someone else's terms. A list of accounts is a per-account control:
// it stops payments to the accounts on it. What a list cannot do is tell you
// about the accounts *around* the ones on it — and a ring is built so that most
// of its accounts have never been reported by anyone, because most of them
// never touched a victim. If detecting at the level of the group is worth
// anything, it should turn each reported account into several implicated ones.
// `Amplification` measures exactly that ratio and nothing more flattering.
//
// # Why the registry is simulated, and what that costs
//
// No public dataset ships a mule registry alongside a labelled transaction
// ledger, so this one is generated from the labels. That is a real limitation
// and it is worth being precise about where it bites: the registry knows which
// accounts laundered, so a feature built on it is partly reading the answer.
//
// Three things keep the number honest.
//
// A real registry is *partial*: victims report a fraction of the mules involved.
// A real registry is *late*: an account is reported days after it moved money.
// A real registry is *wrong* sometimes: not everyone reported is a launderer.
//
// The simulation has all three, and the timing discipline is enforced rather
// than intended — `ListedAt` takes the instant being asked about, and a
// candidate is scored against the moment its window closed, never against the
// state of the list today. Without that, the feature would be reading reports
// filed after the decision it is supposed to inform.
//
// What still cannot be claimed: this measures the value of a registry with the
// stated properties on this ledger. It does not measure the value of I4C's
// actual registry, whose coverage and latency are not public.
package registry

import (
	"math/rand"
	"sort"
	"time"

	"github.com/vinayaktyagi10/warren/internal/detect"
)

// Registry is a set of accounts and the instant each appeared on the list.
type Registry struct {
	listed map[int32]time.Time
}

// ListedAt reports whether the account was on the list at t.
//
// The time argument is the whole point. A registry read as a plain set answers
// "is this account known to be a mule now", which at scoring time is a question
// about the future.
func (r *Registry) ListedAt(account int32, t time.Time) bool {
	if r == nil {
		return false
	}
	when, ok := r.listed[account]
	return ok && !t.Before(when)
}

// Size is how many accounts the list holds in total.
func (r *Registry) Size() int {
	if r == nil {
		return 0
	}
	return len(r.listed)
}

// Accounts returns the listed accounts in a stable order.
func (r *Registry) Accounts() []int32 {
	if r == nil {
		return nil
	}
	out := make([]int32, 0, len(r.listed))
	for a := range r.listed {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SimOpts describes the registry to generate.
type SimOpts struct {
	// Coverage is the share of laundering accounts that are ever reported.
	// I4C's registry is built from victim complaints, and a mule in the middle
	// of a layering chain has no victim to complain about it, so this is well
	// below 1 by the nature of the mechanism rather than by any failure of it.
	Coverage float64

	// ReportDelay is the mean lag between an account moving money and appearing
	// on the list. Drawn per account from an exponential distribution, because
	// reporting is a waiting time and a fixed lag would let the detector learn
	// the constant.
	ReportDelay time.Duration

	// FalseRate is the share of listed accounts that never laundered — accounts
	// caught up in a complaint that did not stand up. A registry with perfect
	// precision is not a registry.
	FalseRate float64

	Seed int64
}

func DefaultSimOpts() SimOpts {
	return SimOpts{Coverage: 0.30, ReportDelay: 72 * time.Hour, FalseRate: 0.05, Seed: 1}
}

// Simulate builds a registry from the ledger's labels.
func Simulate(led *detect.Ledger, opts SimOpts) *Registry {
	rng := rand.New(rand.NewSource(opts.Seed))
	r := &Registry{listed: make(map[int32]time.Time)}

	// First activity per account, so nothing is listed before it acted.
	firstSeen := make(map[int32]time.Time)
	launderers := make(map[int32]bool)
	clean := make(map[int32]bool)

	note := func(a int32, ts time.Time, laundering bool) {
		if prev, ok := firstSeen[a]; !ok || ts.Before(prev) {
			firstSeen[a] = ts
		}
		if laundering {
			launderers[a] = true
		} else if !launderers[a] {
			clean[a] = true
		}
	}
	for _, t := range led.Txns {
		note(t.From, t.TS, t.IsLaundering)
		note(t.To, t.TS, t.IsLaundering)
	}
	for a := range launderers {
		delete(clean, a)
	}

	// Iterate sorted, so the same seed gives the same registry regardless of
	// Go's map ordering. Reproducibility is not optional here: every
	// with-and-without comparison in docs/FINDINGS.md is against a specific
	// registry, and one that differed per run would make them incomparable.
	report := func(a int32) {
		lag := time.Duration(rng.ExpFloat64() * float64(opts.ReportDelay))
		r.listed[a] = firstSeen[a].Add(lag)
	}
	for _, a := range sortedKeys(launderers) {
		if rng.Float64() < opts.Coverage {
			report(a)
		}
	}

	// False reports, sized against the list rather than against the population,
	// so FalseRate reads as "this share of the list is wrong".
	if opts.FalseRate > 0 && opts.FalseRate < 1 {
		want := int(float64(len(r.listed)) * opts.FalseRate / (1 - opts.FalseRate))
		candidates := sortedKeys(clean)
		rng.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		for i := 0; i < want && i < len(candidates); i++ {
			report(candidates[i])
		}
	}
	return r
}

func sortedKeys(m map[int32]bool) []int32 {
	out := make([]int32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Annotate fills each candidate's RegistryShare: the fraction of its accounts
// already on the list when its window closed.
//
// A nil registry zeroes the feature rather than leaving whatever was there, so
// running without a registry is a run with the feature off and not a run with a
// stale one.
func Annotate(candidates []detect.Candidate, r *Registry) {
	for i := range candidates {
		c := &candidates[i]
		if r == nil || len(c.Accounts) == 0 {
			c.Features.RegistryShare = 0
			continue
		}
		hits := 0
		for _, a := range c.Accounts {
			if r.ListedAt(a, c.Window) {
				hits++
			}
		}
		c.Features.RegistryShare = float64(hits) / float64(len(c.Accounts))
	}
}

// Lift is what a graph adds to a list.
type Lift struct {
	// Listed is how many registry accounts appear anywhere in the scored set.
	Listed int

	// Implicated is how many *further* laundering accounts the alerts within
	// budget surfaced — accounts the list did not already name. The listed
	// accounts themselves are excluded on purpose: counting them would be
	// counting the input as output, and is how this measurement would flatter
	// itself if nobody was watching.
	Implicated int

	// Reached is how many alerts within budget contained at least one listed
	// account, so the ratio can be read against how often the list fired at all.
	Reached int
}

// PerListedAccount is the headline: laundering accounts implicated per account
// the registry named.
func (a Lift) PerListedAccount() float64 {
	if a.Listed == 0 {
		return 0
	}
	return float64(a.Implicated) / float64(a.Listed)
}

// Amplification measures, at one alert budget, how many laundering accounts the
// alerts containing a listed account surfaced beyond the listed accounts
// themselves.
func Amplification(led *detect.Ledger, candidates []detect.Candidate,
	scores []float64, budget int, r *Registry) Lift {

	launderer := make(map[int32]bool)
	for _, t := range led.Txns {
		if t.IsLaundering {
			launderer[t.From] = true
			launderer[t.To] = true
		}
	}

	inScope := make(map[int32]bool)
	for _, c := range candidates {
		for _, a := range c.Accounts {
			inScope[a] = true
		}
	}

	var out Lift
	onList := make(map[int32]bool)
	for _, a := range r.Accounts() {
		if inScope[a] {
			onList[a] = true
			out.Listed++
		}
	}

	order := detect.RankOrder(scores)
	if budget > len(order) {
		budget = len(order)
	}

	implicated := make(map[int32]bool)
	for _, ci := range order[:budget] {
		c := candidates[ci]
		hit := false
		for _, a := range c.Accounts {
			if r.ListedAt(a, c.Window) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		out.Reached++
		for _, a := range c.Accounts {
			if launderer[a] && !onList[a] {
				implicated[a] = true
			}
		}
	}
	out.Implicated = len(implicated)
	return out
}
