package web

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/vinayaktyagi10/warren/internal/baseline"
	"github.com/vinayaktyagi10/warren/internal/detect"
)

// alertLine is where a row-level scorer is assumed to alert: the riskiest 10% of
// transfers. Stated rather than tuned, and deliberately generous — a team
// alerting on a tenth of all traffic would already be drowning.
const alertLine = 0.10

// transferView is one transfer, with the score a per-transaction system gave it.
type transferView struct {
	From       string
	To         string
	Amount     float64
	Percentile float64 // 0 = most suspicious in the ledger, 1 = least
	Flagged    bool    // would a top-10% row-level alert have caught it
	Laundering bool
	SelfLoop   bool
}

// heroCase is the labelled ring the console opens on, and what each system made
// of it.
//
// The case is built around the ring itself rather than around WARREN's candidate
// for it. A candidate is a connected group containing a ring plus whatever
// ordinary traffic touches it, so its laundering fraction understates the ring
// and its transfer list pads the table. The ring's own transfers are the honest
// unit: these are the transfers that are laundering, and this is where a
// row-level scorer put them.
type heroCase struct {
	PatternID int32
	Typology  string

	Transfers    []transferView
	Best         float64 // best rank any single transfer reached
	Median       float64
	FlaggedCount int
	AlertsNeeded int

	// The WARREN alert that surfaced this ring, and how much of it that covered.
	CoveringRank int
	Covered      int
}

// ringTransfers groups held-out labelled transfers by the ring they belong to.
func (s *Server) ringTransfers() map[int32][]*detect.Txn {
	out := make(map[int32][]*detect.Txn)
	for i := range s.ledger.Txns {
		t := &s.ledger.Txns[i]
		if t.PatternID == 0 {
			continue
		}
		if _, scored := s.txnPercentile[t.ID]; !scored {
			continue // not in the held-out period
		}
		out[t.PatternID] = append(out[t.PatternID], t)
	}
	return out
}

func (s *Server) buildHeroCase(patternID int32, txns []*detect.Txn) *heroCase {
	h := &heroCase{PatternID: patternID, Typology: s.typologies[patternID], Best: 1, Median: 1}

	var pcts []float64
	for _, t := range txns {
		pct := s.txnPercentile[t.ID]
		tv := transferView{
			From:       accountLabel(s.ledger.Accounts[t.From]),
			To:         accountLabel(s.ledger.Accounts[t.To]),
			Amount:     t.Amount,
			Percentile: pct,
			Flagged:    pct <= alertLine,
			Laundering: t.IsLaundering,
			SelfLoop:   t.From == t.To,
		}
		if tv.Flagged {
			h.FlaggedCount++
		}
		h.Transfers = append(h.Transfers, tv)
		pcts = append(pcts, pct)
	}
	if len(pcts) == 0 {
		return nil
	}

	// Most suspicious first, so the strongest case against the claim leads.
	sort.Slice(h.Transfers, func(i, j int) bool {
		return h.Transfers[i].Percentile < h.Transfers[j].Percentile
	})
	sort.Float64s(pcts)
	h.Best = pcts[0]
	h.Median = pcts[len(pcts)/2]
	h.AlertsNeeded = int(h.Best * float64(len(s.txnPercentile)))
	return h
}

// findCovering locates the highest-ranked WARREN alert containing this ring.
func (s *Server) findCovering(txns []*detect.Txn) (rank, covered int) {
	want := make(map[int32]bool, len(txns))
	for _, t := range txns {
		want[t.ID] = true
	}
	for r := 1; r <= len(s.order); r++ {
		c, _, ok := s.ringAt(r)
		if !ok {
			continue
		}
		n := 0
		for _, id := range c.TxnIDs {
			if want[id] {
				n++
			}
		}
		if n*2 >= len(txns) { // the same "found it" rule the evaluation uses
			return r, n
		}
	}
	return 0, 0
}

// pickHero chooses the case the console opens on.
//
// The rule is stated in the interface rather than hidden: a confirmed ring, big
// enough to be a structure and small enough to draw, that WARREN surfaced and a
// per-transaction scorer missed entirely. Among those it takes the one with the
// most transfers, because a larger ring makes the point harder to make, not
// easier. It is a chosen example, not a claim about the average — the queue and
// the performance page carry the distribution.
func (s *Server) pickHero() (int32, []*detect.Txn) {
	byRing := s.ringTransfers()

	type cand struct {
		id   int32
		txns []*detect.Txn
	}
	var eligible []cand

	for id, txns := range byRing {
		if len(txns) < 5 || len(txns) > 14 {
			continue
		}
		selfLoops := 0
		for _, t := range txns {
			if t.From == t.To {
				selfLoops++
			}
		}
		// A transfer from an account to itself draws as almost nothing and reads
		// as a mistake; a case built largely of them is not the one to lead with.
		if selfLoops*3 > len(txns) {
			continue
		}
		h := s.buildHeroCase(id, txns)
		if h == nil || h.FlaggedCount > 0 {
			continue // the baseline can see it
		}
		if rank, _ := s.findCovering(txns); rank == 0 {
			continue // WARREN did not surface it either; proves nothing
		}
		eligible = append(eligible, cand{id, txns})
	}

	if len(eligible) == 0 {
		return 0, nil
	}
	// Most transfers first, then lowest ring id. The second half is not a
	// tie-break for tidiness: eligible is built by ranging a map, so ordering on
	// transfer count alone leaves every tie at the top to Go's map seed, and the
	// console would open on a different case each restart. Sorting on a total
	// order is the same fix RankOrder already applies to candidate ordering.
	sort.Slice(eligible, func(i, j int) bool {
		if len(eligible[i].txns) != len(eligible[j].txns) {
			return len(eligible[i].txns) > len(eligible[j].txns)
		}
		return eligible[i].id < eligible[j].id
	})
	return eligible[0].id, eligible[0].txns
}

func accountLabel(full string) string {
	if len(full) > 14 {
		return "…" + full[len(full)-13:]
	}
	return full
}

// prepareBaseline fits the per-transaction scorer and records where every
// held-out transfer ranked under it.
//
// Aggregates come from the fitting period only. Building them over the whole
// ledger leaks the answer — a mule's counterparty count is inflated by the
// laundering being predicted — and it flattered the baseline enough to reverse
// the head-to-head result the first time this was measured.
func (s *Server) prepareBaseline(led *detect.Ledger, cut time.Time) {
	agg := baseline.BuildAggregates(led, func(t detect.Txn) bool { return t.TS.Before(cut) })
	s.baseline = baseline.Train(led, agg, func(t detect.Txn) bool { return t.TS.Before(cut) })

	inScope := func(t detect.Txn) bool { return !t.TS.Before(cut) }

	type sc struct {
		id    int32
		score float64
	}
	var pool []sc
	for _, t := range led.Txns {
		if inScope(t) {
			pool = append(pool, sc{t.ID, s.baseline.Score(t)})
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].score > pool[j].score })

	s.txnPercentile = make(map[int32]float64, len(pool))
	for i, p := range pool {
		s.txnPercentile[p.id] = float64(i+1) / float64(len(pool))
	}

	warrenFlagged := make(map[int32]bool)
	for rank := 1; rank <= 50 && rank <= len(s.order); rank++ {
		c, _, ok := s.ringAt(rank)
		if !ok {
			continue
		}
		for _, id := range c.TxnIDs {
			warrenFlagged[id] = true
		}
	}
	s.comparison = baseline.Compare(led, s.baseline, inScope, warrenFlagged)
	log.Printf("baseline: %d transfers scored, WARREN %d rings vs baseline %d at equal budget",
		len(pool), s.comparison.WarrenRings, s.comparison.BaselineRings)
}

// blindRingCount reports how many held-out rings a row-level scorer sees nothing
// of at the stated alert line.
func (s *Server) blindRingCount() (blind, total int) {
	for _, txns := range s.ringTransfers() {
		total++
		seen := false
		for _, t := range txns {
			if s.txnPercentile[t.ID] <= alertLine {
				seen = true
				break
			}
		}
		if !seen {
			blind++
		}
	}
	return blind, total
}

func (s *Server) handleHero(w http.ResponseWriter, r *http.Request) {
	if s.heroPattern == 0 {
		http.Redirect(w, r, "/queue", http.StatusFound)
		return
	}
	txns := s.heroTxns
	h := s.buildHeroCase(s.heroPattern, txns)
	h.CoveringRank, h.Covered = s.findCovering(txns)

	// The graph draws exactly the transfers in the table, so the two views agree.
	ids := make([]int32, 0, len(txns))
	for _, t := range txns {
		ids = append(ids, t.ID)
	}
	nodes, edges := s.layout(detect.Candidate{TxnIDs: ids})

	var ring *ringView
	if h.CoveringRank > 0 {
		ring, _ = s.buildRingView(h.CoveringRank, false)
	}

	blind, totalRings := s.blindRingCount()

	s.render(w, "hero.html", map[string]any{
		"Title":      "The blind spot",
		"Nav":        "hero",
		"Hero":       h,
		"Nodes":      nodes,
		"Edges":      edges,
		"ShowLabels": len(nodes) <= 24,
		"Ring":       ring,
		"AlertLine":  alertLine,
		"BlindRings": blind,
		"TotalRings": totalRings,
		"Cmp":        s.comparison,
		"Scored":     len(s.txnPercentile),
	})
}
