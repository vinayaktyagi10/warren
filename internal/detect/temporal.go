package detect

import (
	"math"
	"sort"
	"time"
)

// Temporal features: what the timestamps say that the shape does not.
//
// Until now the only thing the ranker knew about time was span_hours, and the
// fit says span_hours carries nothing — which is unsurprising, because a span
// is a single number about the ends of a group and says nothing about the
// arrangement in between. Twenty transfers in an hour and one a week later have
// the same span as twenty spread evenly, and only one of those is structuring.
//
// The three measures here are all scale-free on purpose. Labelled rings here
// complete in anywhere from 21 to 129 hours, so anything denominated in hours
// would mostly be measuring ring size.
//
// There is no SMURFING label in IBM AML, so none of this can be validated as a
// typology. What it can be, and is, is measured: docs/FINDINGS.md records what
// these features did to ranked precision and recall against their own absence.

// burstiness measures whether a group's transfers arrive in a burst or evenly
// spread, on a scale where -1 is perfectly regular, 0 is Poisson, and +1 is a
// tight burst against a quiet background.
//
// The starting point is the Goh–Barabási parameter B = (sd - mean) / (sd +
// mean) over inter-arrival times. Its ratio form is what makes it scale-free:
// multiplying every gap by ten leaves it unchanged, so it separates arrangement
// from duration where a plain variance would confound the two. That matters
// here because labelled rings complete in anywhere from 21 to 129 hours.
//
// B alone would not do, though, and the reason is the whole point of this
// function. Its attainable maximum is (sqrt(m) - 1) / (sqrt(m) + 1) over m
// intervals, so a three-transfer group cannot score above 0.17 however tightly
// packed it is, while a fifty-transfer group reaches 0.75. Fed to the ranker
// raw, B would have been a proxy for group size — which the model already has
// two features for — and any coefficient it earned would have been unreadable.
//
// So this is the finite-size corrected form (Kim & Jo 2016), which rescales by
// the sample size to reach exactly -1 and +1 at the extremes for any m:
//
//	A = (sqrt(m+1)·r - sqrt(m-1)) / ((sqrt(m+1) - 2)·r + sqrt(m-1))
//
// with r = sd/mean and m the number of inter-arrival intervals. Since r is
// itself capped at sqrt(m-1), A reaches exactly +1 at the tightest burst any
// group of that size can form, and exactly -1 when the gaps are all equal.
func burstiness(group []Txn) float64 {
	if len(group) < 2 {
		return 0
	}
	ts := make([]time.Time, len(group))
	for i, t := range group {
		ts[i] = t.TS
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })

	gaps := make([]float64, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		gaps = append(gaps, ts[i].Sub(ts[i-1]).Hours())
	}

	var mean float64
	for _, g := range gaps {
		mean += g
	}
	mean /= float64(len(gaps))

	// Everything at the same instant: no spread and no mean, nothing to say.
	if mean == 0 {
		return 0
	}

	var variance float64
	for _, g := range gaps {
		d := g - mean
		variance += d * d
	}
	r := math.Sqrt(variance/float64(len(gaps))) / mean

	// A single interval carries no information about arrangement: two transfers
	// are neither regular nor bursty, and the correction divides by zero saying
	// so. Neutral is the honest answer, not -1.
	m := float64(len(gaps))
	hi, lo := math.Sqrt(m+1), math.Sqrt(m-1)
	den := (hi-2)*r + lo
	if den == 0 {
		return 0
	}
	a := (hi*r - lo) / den

	// r cannot exceed its own maximum, so a cannot leave [-1, 1] except by
	// rounding. Clamping keeps a NaN or a 1.0000000002 out of the alert queue,
	// where either would sort arbitrarily.
	return math.Max(-1, math.Min(1, a))
}

// maxHourShare is the largest share of a group's transfers falling inside any
// one hour.
//
// Buckets run from the group's own first transfer rather than from the wall
// clock, so a burst that straddles an hour boundary is not halved by an
// accident of alignment — which would turn the feature into a measure of what
// time of day a ring happened to run.
func maxHourShare(group []Txn) float64 {
	if len(group) == 0 {
		return 0
	}
	ts := make([]time.Time, len(group))
	for i, t := range group {
		ts[i] = t.TS
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })

	// A sliding window over sorted times finds the densest hour anywhere, not
	// merely the fullest fixed bucket.
	best, lo := 1, 0
	for hi := range ts {
		for ts[hi].Sub(ts[lo]) > time.Hour {
			lo++
		}
		if n := hi - lo + 1; n > best {
			best = n
		}
	}
	return float64(best) / float64(len(group))
}

// fastForward measures how quickly intermediaries pass money on, as a share of
// the group's span: 1 is instant, 0 is holding it for the whole window.
//
// This is the other half of conservation, which is the strongest single feature
// in the model. Conservation says how much of what an account receives leaves
// again; on its own it also describes a business that happens to net out over a
// quarter. Speed is what separates the two, and neither number says it alone.
//
// A group with no intermediaries has no forwarding to observe and scores 0 —
// the same convention conservation uses, so "no evidence" reads the same way in
// both features rather than meaning the opposite in each.
func fastForward(group []Txn) float64 {
	firstIn := make(map[int32]time.Time)
	for _, t := range group {
		if prev, ok := firstIn[t.To]; !ok || t.TS.Before(prev) {
			firstIn[t.To] = t.TS
		}
	}

	// The first outflow at or after the account's first inflow. Money cannot be
	// forwarded before it arrives, so a send that precedes every receipt is not
	// evidence of anything and is skipped rather than counted as instant.
	forwardAt := make(map[int32]time.Time)
	for _, t := range group {
		in, ok := firstIn[t.From]
		if !ok || t.TS.Before(in) {
			continue
		}
		if prev, seen := forwardAt[t.From]; !seen || t.TS.Before(prev) {
			forwardAt[t.From] = t.TS
		}
	}

	delays := make([]float64, 0, len(forwardAt))
	for acct, out := range forwardAt {
		delays = append(delays, out.Sub(firstIn[acct]).Hours())
	}
	if len(delays) == 0 {
		return 0
	}

	first, last := group[0].TS, group[0].TS
	for _, t := range group {
		if t.TS.Before(first) {
			first = t.TS
		}
		if t.TS.After(last) {
			last = t.TS
		}
	}
	span := last.Sub(first).Hours()
	if span <= 0 {
		return 1 // everything at once: forwarded as fast as it is possible to
	}

	sort.Float64s(delays)
	median := delays[len(delays)/2]
	if len(delays)%2 == 0 {
		median = (delays[len(delays)/2-1] + delays[len(delays)/2]) / 2
	}

	v := 1 - median/span
	return math.Max(0, math.Min(1, v))
}
