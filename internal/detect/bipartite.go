package detect

import "math"

// Bipartite features: what can be said about a group that has no intermediaries.
//
// The model's three strongest features — conservation, pass_through and
// fast_forward — are all defined over accounts that both receive and send. A
// bipartite group has none by construction: classify names a group BIPARTITE
// precisely when no account does both. Measured over the full pass, all three
// are exactly zero on all 5,297 such candidates, without one exception
// (docs/FINDINGS.md §18). No fourth intermediary feature can reach that shape.
//
// The three measures here are therefore built only from the two partitions and
// the edges between them. They are scale-free for the same reason the temporal
// features are: group size is already carried by log_txns and log_accounts, and
// a feature correlated with it would earn a coefficient that means size.
//
// They are exploratory. §18 records what they were worth.

// partitionBalance is how evenly the accounts that play exactly one role split
// between the sending side and the receiving side: 1 when the two sides are the
// same size, falling toward 0 as one side dominates.
//
// The motivation is what a labelled BIPARTITE ring in IBM AML actually is — a
// set of disjoint sender->receiver pairs, so as many senders as receivers —
// against the fans that dominate ordinary traffic, where one account faces
// many. Accounts that both send and receive are excluded rather than assigned
// to a side, because an intermediary belongs to neither partition and counting
// it in both would make the feature rise with pass_through, which the model
// already has.
//
// A group with no one-sided accounts — a pure cycle — has no partitions to
// balance and scores 0. That is the same "no evidence" convention conservation
// and fast_forward use, rather than 1 for trivially balanced.
//
// Failure mode to watch: any 1:1 group scores 1, including an entirely ordinary
// pair of unrelated payments that happened to land in the same window. The
// feature says a group is two-sided, never that it is criminal.
func partitionBalance(senders, receivers, accounts map[int32]bool) float64 {
	onlySends, onlyReceives := 0, 0
	for a := range accounts {
		s, r := senders[a], receivers[a]
		switch {
		case s && !r:
			onlySends++
		case r && !s:
			onlyReceives++
		}
	}
	total := onlySends + onlyReceives
	if total == 0 {
		return 0
	}
	lo := onlySends
	if onlyReceives < lo {
		lo = onlyReceives
	}
	return 2 * float64(lo) / float64(total)
}

// pairReuse is the share of a group's transfers that are not the first to run
// along their own sender->receiver relationship.
//
// Ordinary counterparties transact repeatedly: a customer pays the same
// supplier every month. A generated ring spends each relationship once, because
// its purpose is to move value across as many distinct edges as it can. So the
// expectation is that laundering scores low and the coefficient comes out
// negative — which is a claim the fit can refuse.
//
// The pair is directed. A paying B is a different relationship from B paying A,
// and conflating them would read an ordinary two-way settlement as a repeat.
//
// Failure mode to watch: this is close to density (transfers per account), and
// on many groups the two move together. They come apart where a group has many
// distinct pairs at high density, which is exactly the disjoint-pair structure
// this is aimed at; the fit prices whether the difference is worth a slot.
func pairReuse(group []Txn) float64 {
	if len(group) == 0 {
		return 0
	}
	type pair struct{ from, to int32 }
	seen := make(map[pair]bool, len(group))
	for _, t := range group {
		seen[pair{t.From, t.To}] = true
	}
	return 1 - float64(len(seen))/float64(len(group))
}

// amountUniformity is how alike a group's transfer amounts are: 1 when every
// transfer moves the same amount, falling toward 0 as they scatter.
//
// It is 1/(1 + cv) over the coefficient of variation, which is scale-free by
// construction — multiplying every amount by a thousand leaves it unchanged.
// That matters because the model already carries three amount features and a
// fourth that read size would tell it nothing new. What this reads is spread.
//
// The motivation is that a group of disjoint pairs moving near-identical
// amounts is a structured payout, while ordinary traffic through the same
// accounts varies by orders of magnitude.
//
// Amountless transfers are 0 rather than 1: nothing to compare is not the same
// as everything matching, and the group's amounts are already described by
// three other features that would notice.
//
// Failure mode to watch: a merchant selling one fixed-price product scores 1 as
// readily as a payout ring, and a three-transfer group's cv is unstable.
func amountUniformity(group []Txn) float64 {
	if len(group) == 0 {
		return 0
	}
	var mean float64
	for _, t := range group {
		mean += t.Amount
	}
	mean /= float64(len(group))
	if mean <= 0 {
		return 0
	}
	var variance float64
	for _, t := range group {
		d := t.Amount - mean
		variance += d * d
	}
	cv := math.Sqrt(variance/float64(len(group))) / mean
	return 1 / (1 + cv)
}
