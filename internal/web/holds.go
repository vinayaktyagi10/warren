package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/enforce"
)

// enforceDecision turns a recorded decision into the holds it authorises, in
// memory and on disk, and returns how many accounts it held plus any bound that
// stopped it acting.
//
// It runs after the decision is in the audit log, never before, and it carries
// that entry's sequence number onto every lease. A hold whose authority cannot
// be named is one this system will not place, and doing it in this order is what
// makes that true rather than merely intended: if the log write fails, nothing
// is held.
//
// `enforcing` is which pass the ring came from, and it is a policy input rather
// than a detail. Only the narrow geometry leases. The wide pass feeds the
// analyst queue and holds nothing, because letting it lease was measured at
// +8.4% innocent money held for +1.9% of the laundering value — see
// docs/FINDINGS.md §14. A decision made on a wide-pass alert is still recorded
// in full; it simply does not touch anyone's money without a person.
func (s *Server) enforceDecision(ctx context.Context, c detect.Candidate,
	a agent.Assessment, decisionSeq int64, lim enforce.Limits, enforcing bool) (int, []string) {

	leases, bounds := enforce.Restrictions(a, c, time.Now().UTC(), lim)
	if !enforcing {
		if len(leases) > 0 {
			bounds = append(bounds, fmt.Sprintf(
				"no hold placed: the %s pass feeds the analyst queue and does not lease; "+
					"enforcement runs on the %s pass", lim.Pass, s.enforcer.limits.Pass))
		}
		return 0, bounds
	}
	if len(leases) == 0 {
		return 0, bounds
	}

	s.mu.Lock()
	for _, l := range leases {
		s.register.Impose(l)
	}
	s.mu.Unlock()

	held := 0
	for _, l := range leases {
		label := s.accountLabel(l.Account)
		if _, _, err := s.holds.Impose(ctx, l, label, &decisionSeq); err != nil {
			// A lease that could not be recorded is a lease that did not happen.
			// Saying so loudly beats holding money with no record of why.
			log.Printf("hold on %s not recorded, not enforced: %v", label, err)
			continue
		}
		held++
	}
	return held, bounds
}

func (s *Server) accountLabel(idx int32) string {
	if s.ledger == nil || int(idx) >= len(s.ledger.Accounts) {
		return fmt.Sprintf("account-%d", idx)
	}
	return s.ledger.Accounts[idx]
}

// holdView is one account currently under a lease, with the authority behind it.
type holdView struct {
	Account     string
	Tier        string
	RingID      int
	Pass        string
	Reason      string
	DecisionSeq int64
	HasDecision bool
	Imposed     time.Time
	Expires     time.Time
	Remaining   string
	Expired     bool

	// Orphaned marks a hold whose authorising decision no longer verifies. This
	// is the join the two chains exist to make: not "a hash is wrong somewhere"
	// but "these accounts are having their money held on an authority that no
	// longer checks out".
	Orphaned bool
}

// enforceView is one alert from the pass that may act, with everything the
// block gate reads, so a page showing no freezes shows why on the same row.
type enforceView struct {
	Rank       int
	Score      float64
	Typology   string
	Accounts   int
	Txns       int
	Total      float64
	Decision   string
	Proposed   string
	Confidence float64
	Source     string
	Held       int
	Blocks     bool // the ranker score clears the block gate
}

// enforcementQueue renders the top of the narrow pass, joined to whatever the
// system has already decided about each entry.
func (s *Server) enforcementQueue(ctx context.Context, limit int) []enforceView {
	decided := map[int]auditRow{}
	rows, err := s.pool.Query(ctx,
		`SELECT ring_id, action, proposed, confidence, source FROM audit_log WHERE ring_id >= $1`,
		enforceRingBase)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d auditRow
			var ring int
			if err := rows.Scan(&ring, &d.action, &d.proposed, &d.confidence, &d.source); err != nil {
				break
			}
			decided[ring-enforceRingBase] = d
		}
	}

	held := map[int]int{}
	if events, err := s.holds.Events(ctx); err == nil {
		for _, e := range events {
			if e.Event == enforce.EventImpose && e.RingID >= enforceRingBase {
				held[e.RingID-enforceRingBase]++
			}
		}
	}

	policy := s.chain.Policy
	out := make([]enforceView, 0, limit)
	for rank := 1; rank <= limit; rank++ {
		c, sc, ok := s.enforcer.at(rank)
		if !ok {
			break
		}
		v := enforceView{
			Rank: rank, Score: sc, Typology: c.Typology,
			Accounts: c.Features.Accounts, Txns: c.Features.Txns,
			Total:  c.Features.TotalAmount,
			Held:   held[rank],
			Blocks: sc >= policy.BlockMinScore && c.Features.TotalAmount <= policy.BlockMaxAmount,
		}
		if d, ok := decided[rank]; ok {
			v.Decision, v.Proposed, v.Confidence, v.Source = d.action, d.proposed, d.confidence, d.source
		}
		out = append(out, v)
	}
	return out
}

type auditRow struct {
	action, proposed, source string
	confidence               float64
}

// handleEnforceAssess decides one alert from the pass that may act.
//
// It is a separate route from the analyst one rather than a flag on it, because
// the two differ in the only way that matters: this one can take someone's
// money. A single endpoint with a boolean would put that distinction in a
// parameter, where it is one typo away from being wrong.
func (s *Server) handleEnforceAssess(w http.ResponseWriter, r *http.Request) {
	rank, err := strconv.Atoi(r.PathValue("rank"))
	if err != nil {
		http.Error(w, "bad rank", http.StatusBadRequest)
		return
	}
	c, sc, ok := s.enforcer.at(rank)
	if !ok {
		http.NotFound(w, r)
		return
	}

	start := time.Now()
	ev := s.evidenceOf(enforceRingBase+rank, c, sc)
	a := s.chain.Assess(r.Context(), ev)
	seq, hash, err := s.log.Record(r.Context(), ev, a)
	if err != nil {
		http.Error(w, "record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	held, bounds := s.enforceDecision(r.Context(), c, a, seq, s.enforcer.limits, true)

	writeJSON(w, map[string]any{
		"action": string(a.Action), "proposed": string(a.Proposed),
		"adjusted": a.Adjusted(), "confidence": a.Confidence,
		"rationale": a.Rationale, "source": a.Source, "adjustments": a.Adjustments,
		"actionClass": actionClass(string(a.Action)),
		"held":        held, "heldBounds": bounds,
		"auditSeq": seq, "auditHash": hash,
		"took": time.Since(start).Round(time.Millisecond).String(),
	})
}

func (s *Server) handleHolds(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	held, err := s.holds.ActiveAt(r.Context(), now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// A broken decision chain taints every lease authorised at or after the
	// break, because every entry after a break inherits it.
	tainted := int64(-1)
	if res, err := s.log.Verify(r.Context()); err == nil && !res.Valid {
		tainted = res.BrokenSeq
	}

	views := make([]holdView, 0, len(held))
	frozen, watching, orphaned := 0, 0, 0
	for _, h := range held {
		v := holdView{
			Account: h.Account, Tier: string(h.Tier), RingID: h.RingID,
			Pass: h.Pass, Reason: h.Reason,
			Imposed: h.ImposedAt, Expires: h.ExpiresAt, Expired: h.Expired,
		}
		if h.DecisionSeq != nil {
			v.DecisionSeq, v.HasDecision = *h.DecisionSeq, true
			v.Orphaned = tainted >= 0 && *h.DecisionSeq >= tainted
		}
		if d := h.ExpiresAt.Sub(now); d > 0 {
			v.Remaining = humanDuration(d)
		} else {
			v.Remaining = "lapsed"
		}
		if h.Tier == enforce.TierFrozen && !h.Expired {
			frozen++
		}
		if h.Tier == enforce.TierWatch && !h.Expired {
			watching++
		}
		if v.Orphaned {
			orphaned++
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Orphaned != views[j].Orphaned {
			return views[i].Orphaned
		}
		if views[i].Tier != views[j].Tier {
			return views[i].Tier == string(enforce.TierFrozen)
		}
		return views[i].Expires.Before(views[j].Expires)
	})

	events, _ := s.holds.Events(r.Context())
	verify, _ := s.holds.Verify(r.Context())
	why := s.whyNotFrozen(r.Context())

	s.render(w, "holds.html", map[string]any{
		"Title":    "Holds in force",
		"Nav":      "holds",
		"Holds":    views,
		"Frozen":   frozen,
		"Watching": watching,
		"Orphaned": orphaned,
		"Events":   len(events),
		"Verify":   verify,
		"Limits":   s.enforcer.limits,
		"Queue":    s.enforcementQueue(r.Context(), 12),
		"Policy":   s.chain.Policy,
		"Analyst":  s.limits.Pass,
		"Enforcer": s.enforcer.limits.Pass,
		"Why":      why,
	})
}

// withheld explains why the frozen count is what it is.
//
// A page showing zero freezes reads as a broken feature. It is usually the
// opposite: the envelope refusing to act, which is the behaviour the whole
// design exists to produce. The reasons are read back out of the decisions'
// own recorded adjustments rather than restated here, so the explanation cannot
// drift from what the policy actually did.
type withheld struct {
	Decisions     int
	ProposedBlock int
	OnCeiling     int
	OnDegraded    int
	Any           bool
}

func (s *Server) whyNotFrozen(ctx context.Context) withheld {
	var w withheld
	rows, err := s.pool.Query(ctx, `SELECT proposed, source, adjustments FROM audit_log`)
	if err != nil {
		return w
	}
	defer rows.Close()
	for rows.Next() {
		var proposed, source string
		var adjustments []string
		if err := rows.Scan(&proposed, &source, &adjustments); err != nil {
			return w
		}
		w.Decisions++
		if proposed == string(agent.ActionBlock) {
			w.ProposedBlock++
		}
		if source == "deterministic-rule" {
			w.OnDegraded++
		}
		for _, adj := range adjustments {
			if strings.Contains(adj, "autonomous block ceiling") {
				w.OnCeiling++
				break
			}
		}
	}
	w.Any = w.Decisions > 0
	return w
}

func (s *Server) handleLift(w http.ResponseWriter, r *http.Request) {
	account := r.FormValue("account")
	if account == "" {
		http.Error(w, "account required", http.StatusBadRequest)
		return
	}
	reason := r.FormValue("reason")
	if reason == "" {
		reason = "Released by an operator from the console."
	}
	seq, hash, err := s.holds.Lift(r.Context(), account, reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"lifted": account, "seq": seq, "hash": hash})
}

func (s *Server) handleVerifyHolds(w http.ResponseWriter, r *http.Request) {
	res, err := s.holds.Verify(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"valid":  res.Valid,
		"events": res.Events,
		"seq":    res.BrokenSeq,
		"reason": res.BrokenReason,
	})
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.0fd %.0fh", d.Hours()/24, d.Hours()-24*float64(int(d.Hours()/24)))
	case d >= time.Hour:
		return fmt.Sprintf("%.0fh %.0fm", d.Hours(), d.Minutes()-60*float64(int(d.Hours())))
	default:
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
}
