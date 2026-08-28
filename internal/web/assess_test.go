package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/enforce"
)

// Assessment is the only route that calls a model and the only one that writes,
// and there are two of them because only one of the two can take someone's
// money. These tests run against Postgres, because what they are checking is
// that a hold is placed after its decision is committed and never before, and
// that ordering does not exist outside a database.

// assessResponse is what both assessment routes return. Keeping one struct for
// both is deliberate: the console's JavaScript reads them the same way, so a
// field that appears on one and not the other is a bug in whichever is missing.
type assessResponse struct {
	Action      string   `json:"action"`
	Proposed    string   `json:"proposed"`
	Adjusted    bool     `json:"adjusted"`
	Confidence  float64  `json:"confidence"`
	Rationale   string   `json:"rationale"`
	Source      string   `json:"source"`
	Adjustments []string `json:"adjustments"`
	ActionClass string   `json:"actionClass"`
	Held        int      `json:"held"`
	HeldBounds  []string `json:"heldBounds"`
	AuditSeq    int64    `json:"auditSeq"`
	AuditHash   string   `json:"auditHash"`
	Took        string   `json:"took"`
}

func decodeAssess(t *testing.T, res *http.Response) assessResponse {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("assess returned %d, want 200: %s", res.StatusCode, body(t, res))
	}
	var out assessResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode assessment: %v", err)
	}
	return out
}

func (s *testServer) events(t *testing.T) []enforce.Event {
	t.Helper()
	events, err := s.holds.Events(context.Background())
	if err != nil {
		t.Fatalf("read restriction ledger: %v", err)
	}
	return events
}

func (s *testServer) auditCount(t *testing.T) int64 {
	t.Helper()
	n, err := s.log.Count(context.Background())
	if err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	return n
}

// The analyst pass is the wide geometry. It records decisions and it leases
// nothing — measured at +8.4% innocent money held for +1.9% of the laundering
// value if it did. A block on this route is a recorded opinion, not an action.
func TestTheAnalystRouteRecordsADecisionAndLeasesNothing(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	got := decodeAssess(t, s.post(t, "/api/assess/1", "", ""))

	if got.Action != string(agent.ActionBlock) {
		t.Fatalf("action = %q, want block on a ring that clears every gate "+
			"(adjustments: %v)", got.Action, got.Adjustments)
	}
	if got.Held != 0 {
		t.Errorf("the analyst route held %d accounts; it must lease nothing", got.Held)
	}
	if !strings.Contains(strings.Join(got.HeldBounds, " "), "does not lease") {
		t.Errorf("bounds %v do not say why no hold was placed", got.HeldBounds)
	}
	if got.AuditSeq == 0 || got.AuditHash == "" {
		t.Errorf("decision was not recorded: seq %d hash %q", got.AuditSeq, got.AuditHash)
	}
	if n := s.auditCount(t); n != 1 {
		t.Errorf("audit log holds %d decisions, want 1", n)
	}
	if events := s.events(t); len(events) != 0 {
		t.Errorf("the analyst route wrote %d restriction events", len(events))
	}
}

// The enforcement pass is the narrow geometry and the only one that leases. A
// freeze here must clear the same gates a block clears, and must name the audit
// entry that authorised it.
func TestTheEnforcementRouteFreezesOnlyOnAnApprovedBlock(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	c, score, ok := s.enforcer.at(1)
	if !ok {
		t.Fatal("no enforcement candidate at rank 1")
	}
	got := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))

	if got.Action != string(agent.ActionBlock) {
		t.Fatalf("action = %q, want block (score %.4f, %.2f moved, adjustments %v)",
			got.Action, score, c.Features.TotalAmount, got.Adjustments)
	}
	// The gates, read off the candidate the decision was actually made about.
	policy := s.chain.Policy
	if score < policy.BlockMinScore {
		t.Errorf("blocked at detector score %.4f, below the %.2f gate", score, policy.BlockMinScore)
	}
	if got.Confidence < policy.BlockMinConfidence {
		t.Errorf("blocked at confidence %.2f, below the %.2f gate", got.Confidence, policy.BlockMinConfidence)
	}
	if c.Features.TotalAmount > policy.BlockMaxAmount {
		t.Errorf("blocked a ring moving %.2f, above the %.2f ceiling",
			c.Features.TotalAmount, policy.BlockMaxAmount)
	}

	if got.Held != len(c.Accounts) {
		t.Fatalf("held %d accounts for a %d-account ring", got.Held, len(c.Accounts))
	}

	events := s.events(t)
	if len(events) != len(c.Accounts) {
		t.Fatalf("%d restriction events for %d accounts", len(events), len(c.Accounts))
	}
	for _, e := range events {
		if e.Event != enforce.EventImpose {
			t.Errorf("event %d is %q, want an impose", e.Seq, e.Event)
		}
		if e.Tier != enforce.TierFrozen {
			t.Errorf("account %s leased at tier %q, want frozen", e.Account, e.Tier)
		}
		// Invariant: every restriction names the decision that authorised it,
		// and that decision is in the log.
		if e.DecisionSeq == nil {
			t.Fatalf("account %s is held on no stated authority", e.Account)
		}
		if *e.DecisionSeq != got.AuditSeq {
			t.Errorf("account %s names decision %d, the decision was %d",
				e.Account, *e.DecisionSeq, got.AuditSeq)
		}
		if e.RingID != enforceRingBase+1 {
			t.Errorf("lease names ring %d, want %d", e.RingID, enforceRingBase+1)
		}
		if e.Pass != s.enforcer.limits.Pass {
			t.Errorf("lease names pass %q, want %q", e.Pass, s.enforcer.limits.Pass)
		}
		// The lease lasts the window that justified it, and no longer.
		if got := e.ExpiresAt.Sub(e.ImposedAt); got != s.enforcer.limits.FrozenFor {
			t.Errorf("freeze lasts %v, want %v", got, s.enforcer.limits.FrozenFor)
		}
	}

	// The lease was placed after the decision existed, not before: the sequence
	// it names is a row in the log.
	var seq int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT seq FROM audit_log WHERE seq = $1`, got.AuditSeq).Scan(&seq); err != nil {
		t.Fatalf("the decision the holds name is not in the log: %v", err)
	}
}

// A hold routes a group to a person. It is visible, it expires, and it stops
// nothing — the difference between a watch and a freeze is the whole of what
// "bounded" means here.
func TestAHoldLeasesAWatchAndFreezesNobody(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))

	got := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	if got.Action != string(agent.ActionHold) {
		t.Fatalf("action = %q, want hold_for_review", got.Action)
	}
	if got.Held == 0 {
		t.Fatal("a hold placed no watch at all")
	}
	for _, e := range s.events(t) {
		if e.Tier != enforce.TierWatch {
			t.Errorf("account %s leased at tier %q on a hold, want watch", e.Account, e.Tier)
		}
	}
	held, err := s.holds.ActiveAt(context.Background(), s.events(t)[0].ImposedAt)
	if err != nil {
		t.Fatalf("read active holds: %v", err)
	}
	for _, h := range held {
		if h.Tier == enforce.TierFrozen {
			t.Errorf("account %s is frozen on a hold decision", h.Account)
		}
	}
}

// An allow touches nothing at all.
func TestAnAllowLeasesNothing(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionAllow, 0.9))

	// The floor withholds allow on anything the detector scored highly, so the
	// allow has to be tested on a candidate the detector does not rate.
	rank := lowScoringEnforceRank(t, s)
	got := decodeAssess(t, s.post(t, "/api/enforce/assess/"+strconv.Itoa(rank), "", ""))

	if got.Action != string(agent.ActionAllow) {
		t.Fatalf("action = %q on a %.4f-scored candidate, want allow (adjustments %v)",
			got.Action, scoreAtEnforce(t, s, rank), got.Adjustments)
	}
	if got.Held != 0 {
		t.Errorf("an allow leased %d accounts", got.Held)
	}
	if events := s.events(t); len(events) != 0 {
		t.Errorf("an allow wrote %d restriction events", len(events))
	}
}

// The policy binds in both directions, and the route has to show both halves:
// what the model asked for, and what the system decided instead.
func TestTheRouteReturnsTheBoundedDecisionAndTheProposalItCameFrom(t *testing.T) {
	high := 1 // scores ~0.98 on the synthetic ledger

	tests := []struct {
		name        string
		proposal    agent.Action
		confidence  float64
		rank        int
		wantAction  agent.Action
		wantAdjust  bool
		wantMention string
	}{
		{
			name:     "block on a ring that clears every gate",
			proposal: agent.ActionBlock, confidence: 0.95, rank: high,
			wantAction: agent.ActionBlock, wantAdjust: false,
		},
		{
			name:     "block withheld on the assessor's own confidence",
			proposal: agent.ActionBlock, confidence: 0.40, rank: high,
			wantAction: agent.ActionHold, wantAdjust: true,
			wantMention: "assessor confidence",
		},
		{
			name:     "allow withheld on a well-scored ring",
			proposal: agent.ActionAllow, confidence: 0.99, rank: high,
			wantAction: agent.ActionHold, wantAdjust: true,
			wantMention: "allow withheld",
		},
		{
			name:     "hold stands as proposed",
			proposal: agent.ActionHold, confidence: 0.6, rank: high,
			wantAction: agent.ActionHold, wantAdjust: false,
		},
		{
			name:     "an action outside the permitted set lands on review",
			proposal: agent.Action("freeze_everything"), confidence: 0.99, rank: high,
			wantAction: agent.ActionHold, wantAdjust: false,
			wantMention: "not in the permitted set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDBTestServer(t, chainProposing(tt.proposal, tt.confidence))
			got := decodeAssess(t, s.post(t, "/api/assess/"+strconv.Itoa(tt.rank), "", ""))

			if got.Action != string(tt.wantAction) {
				t.Errorf("action = %q, want %q (adjustments %v)",
					got.Action, tt.wantAction, got.Adjustments)
			}
			if got.Adjusted != tt.wantAdjust {
				t.Errorf("adjusted = %v, want %v (proposed %q, action %q)",
					got.Adjusted, tt.wantAdjust, got.Proposed, got.Action)
			}
			if tt.wantMention != "" &&
				!strings.Contains(strings.Join(got.Adjustments, " "), tt.wantMention) {
				t.Errorf("adjustments %v, want one mentioning %q", got.Adjustments, tt.wantMention)
			}
			if got.ActionClass != actionClass(got.Action) {
				t.Errorf("actionClass %q does not match action %q", got.ActionClass, got.Action)
			}

			// Whatever the policy did, the recorded decision is the one the
			// route reported.
			var action, proposed string
			if err := s.pool.QueryRow(context.Background(),
				`SELECT action, proposed FROM audit_log WHERE seq = $1`,
				got.AuditSeq).Scan(&action, &proposed); err != nil {
				t.Fatalf("read back the decision: %v", err)
			}
			if action != got.Action || proposed != got.Proposed {
				t.Errorf("log holds %q/%q, the route reported %q/%q",
					action, proposed, got.Action, got.Proposed)
			}
		})
	}
}

// A confidence outside [0,1] is a malformed response. It is clamped, and the
// clamped value is what the route reports and what the log keeps — an inflated
// number must not buy authority it did not earn.
func TestConfidenceOutsideTheUnitIntervalIsClampedOnTheWayThrough(t *testing.T) {
	for _, tc := range []struct {
		stated float64
		want   float64
	}{{4.2, 1}, {-3, 0}} {
		s := newDBTestServer(t, chainProposing(agent.ActionBlock, tc.stated))
		got := decodeAssess(t, s.post(t, "/api/assess/1", "", ""))

		if got.Confidence != tc.want {
			t.Errorf("confidence %v reported as %v, want %v", tc.stated, got.Confidence, tc.want)
		}
		if !strings.Contains(strings.Join(got.Adjustments, " "), "out of range") {
			t.Errorf("adjustments %v do not record the clamp", got.Adjustments)
		}
		var recorded float64
		if err := s.pool.QueryRow(context.Background(),
			`SELECT confidence FROM audit_log WHERE seq = $1`, got.AuditSeq).Scan(&recorded); err != nil {
			t.Fatalf("read back the decision: %v", err)
		}
		if float64(float32(tc.want)) != recorded {
			t.Errorf("log kept confidence %v, want the clamped %v", recorded, tc.want)
		}
	}
}

// The chain exists for this: a tier that cannot answer must cost a step down,
// not an error page.
func TestAFailedTierFallsThroughWithoutFailingTheRequest(t *testing.T) {
	calls := 0
	chain := agent.NewChain(agent.DefaultPolicy(),
		failingTier{"gemini-3.7-flash", errors.New("503 model overloaded")},
		countingTier{
			fixedTier: fixedTier{"gemini-3.5-flash-lite",
				agent.Proposal{Action: agent.ActionHold, Confidence: 0.7, Rationale: "second tier"}},
			calls: &calls,
		},
	)
	s := newDBTestServer(t, chain)

	got := decodeAssess(t, s.post(t, "/api/assess/1", "", ""))
	if got.Source != "gemini-3.5-flash-lite" {
		t.Errorf("source = %q, want the tier that answered", got.Source)
	}
	if calls != 1 {
		t.Errorf("second tier consulted %d times, want 1", calls)
	}
	if !strings.Contains(strings.Join(got.Adjustments, " "), "gemini-3.7-flash unavailable") {
		t.Errorf("adjustments %v do not record the degradation", got.Adjustments)
	}

	// The degradation travels with the decision, not only in the logs.
	var adjustments []string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT adjustments FROM audit_log WHERE seq = $1`, got.AuditSeq).Scan(&adjustments); err != nil {
		t.Fatalf("read back the decision: %v", err)
	}
	if !strings.Contains(strings.Join(adjustments, " "), "unavailable") {
		t.Errorf("the log kept %v, which does not say the primary was down", adjustments)
	}
}

// With every model tier down the system still decides, the request still
// succeeds, and the decision is still bounded. This is `serve -offline`, and it
// is step 7 of the demo.
func TestWithEveryModelDownTheRequestStillReturnsABoundedDecision(t *testing.T) {
	chain := agent.NewChain(agent.DefaultPolicy(),
		failingTier{"gemini-3.7-flash", errors.New("503 model overloaded")},
		failingTier{"gemini-3.5-flash-lite", errors.New("context deadline exceeded")},
	)
	s := newDBTestServer(t, chain)

	got := decodeAssess(t, s.post(t, "/api/assess/1", "", ""))
	if got.Source != "deterministic-rule" {
		t.Fatalf("source = %q, want the fallback rule", got.Source)
	}
	if got.Action != string(agent.ActionHold) {
		t.Errorf("action = %q, want hold_for_review", got.Action)
	}
	if got.Rationale == "" {
		t.Error("the fallback produced no explanation")
	}
	if s.auditCount(t) != 1 {
		t.Error("the degraded decision was not recorded")
	}
}

// The last tier never blocks. An autonomous freeze on a degraded path is
// exactly the decision that should wait for a person — and the enforcement
// route is where that would cost someone their money.
func TestTheDeterministicRuleNeverFreezesAnAccount(t *testing.T) {
	s := newDBTestServer(t, offlineChain())

	c, score, _ := s.enforcer.at(1)
	if score < s.chain.Policy.BlockMinScore {
		t.Fatalf("rank 1 scored %.4f; this test needs a candidate that would "+
			"otherwise clear the block gate", score)
	}
	got := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))

	if got.Action == string(agent.ActionBlock) {
		t.Fatalf("the fallback rule blocked a ring at score %.4f", score)
	}
	if got.Proposed == string(agent.ActionBlock) {
		t.Fatalf("the fallback rule proposed a block")
	}
	for _, e := range s.events(t) {
		if e.Tier == enforce.TierFrozen {
			t.Errorf("account %s frozen on the degraded path", e.Account)
		}
	}
	if got.Held != len(c.Accounts) {
		t.Errorf("held %d of %d accounts; a hold still watches", got.Held, len(c.Accounts))
	}
}

// Each route reads its own pass. If the enforcement route ever assessed the
// analyst pass's candidate, the holds page would name accounts that were never
// the ones acted on.
func TestEachRouteAssessesItsOwnPass(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.6))

	analyst, _, _ := s.ringAt(2)
	enforcing, _, _ := s.enforcer.at(2)

	a := decodeAssess(t, s.post(t, "/api/assess/2", "", ""))
	e := decodeAssess(t, s.post(t, "/api/enforce/assess/2", "", ""))

	totals := map[int64]float64{}
	rings := map[int64]int{}
	rows, err := s.pool.Query(context.Background(),
		`SELECT seq, ring_id, (evidence->>'TotalAmount')::float8 FROM audit_log`)
	if err != nil {
		t.Fatalf("read decisions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var ring int
		var total float64
		if err := rows.Scan(&seq, &ring, &total); err != nil {
			t.Fatalf("scan: %v", err)
		}
		totals[seq], rings[seq] = total, ring
	}

	if totals[a.AuditSeq] != analyst.Features.TotalAmount {
		t.Errorf("the analyst decision was made on %.2f, the analyst candidate moves %.2f",
			totals[a.AuditSeq], analyst.Features.TotalAmount)
	}
	if totals[e.AuditSeq] != enforcing.Features.TotalAmount {
		t.Errorf("the enforcement decision was made on %.2f, the enforcement candidate moves %.2f",
			totals[e.AuditSeq], enforcing.Features.TotalAmount)
	}
	if rings[a.AuditSeq] != 2 || rings[e.AuditSeq] != enforceRingBase+2 {
		t.Errorf("ring ids %d and %d, want 2 and %d",
			rings[a.AuditSeq], rings[e.AuditSeq], enforceRingBase+2)
	}
}

// A rank that resolves to nothing must not reach the model, and must not write.
func TestABadRankOnEitherRouteWritesNothing(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	tests := []struct {
		path string
		want int
	}{
		{"/api/assess/0", http.StatusNotFound},
		{"/api/assess/999999", http.StatusNotFound},
		{"/api/assess/-4", http.StatusNotFound},
		{"/api/assess/abc", http.StatusBadRequest},
		{"/api/enforce/assess/0", http.StatusNotFound},
		{"/api/enforce/assess/999999", http.StatusNotFound},
		{"/api/enforce/assess/abc", http.StatusBadRequest},
	}
	for _, tt := range tests {
		res := s.post(t, tt.path, "", "")
		if res.StatusCode != tt.want {
			t.Errorf("POST %s = %d, want %d", tt.path, res.StatusCode, tt.want)
		}
	}
	if n := s.auditCount(t); n != 0 {
		t.Errorf("%d decisions recorded for ranks that do not exist", n)
	}
	if events := s.events(t); len(events) != 0 {
		t.Errorf("%d restrictions placed for ranks that do not exist", len(events))
	}
}

// The console posts these with no body at all. A form body, a JSON body and an
// empty one must all reach the same decision, because the rank is in the path
// and nothing else is read.
func TestAssessIgnoresTheRequestBody(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.6))

	bodies := []struct{ contentType, body string }{
		{"", ""},
		{"application/json", `{"action":"allow","confidence":1}`},
		{"application/x-www-form-urlencoded", "action=allow&rank=99999"},
		{"application/json", "not json at all {{{"},
	}
	for _, b := range bodies {
		got := decodeAssess(t, s.post(t, "/api/assess/1", b.contentType, b.body))
		if got.Action != string(agent.ActionHold) {
			t.Errorf("body %q changed the decision to %q", b.body, got.Action)
		}
	}
}

// scoreAtEnforce and lowScoringEnforceRank find a candidate the detector does
// not rate, which is where an allow can survive the policy floor.
func scoreAtEnforce(t *testing.T, s *testServer, rank int) float64 {
	t.Helper()
	_, sc, ok := s.enforcer.at(rank)
	if !ok {
		t.Fatalf("no enforcement candidate at rank %d", rank)
	}
	return sc
}

func lowScoringEnforceRank(t *testing.T, s *testServer) int {
	t.Helper()
	for rank := len(s.enforcer.order); rank >= 1; rank-- {
		if _, sc, ok := s.enforcer.at(rank); ok && sc < s.chain.Policy.HoldMinScore {
			return rank
		}
	}
	t.Fatal("no enforcement candidate scores below the review threshold")
	return 0
}
