package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/enforce"
)

// The holds page is step 6 of the demo: what the system is sitting on, on whose
// authority, and the button that lets a person take it back.

// record puts one decision in the log and returns its sequence number, so a
// test that is about enforcement still enforces on a real authority.
func (s *testServer) record(t *testing.T, ev agent.Evidence, a agent.Assessment) int64 {
	t.Helper()
	seq, _, err := s.log.Record(context.Background(), ev, a)
	if err != nil {
		t.Fatalf("record decision: %v", err)
	}
	return seq
}

// candidateOf builds a candidate of n accounts moving total, for the bounds
// that no ranked candidate in the synthetic ledger happens to cross.
func candidateOf(n int, total float64) detect.Candidate {
	c := detect.Candidate{Typology: "CYCLE"}
	for i := range n {
		c.Accounts = append(c.Accounts, int32(1000+i))
	}
	c.Features = detect.Features{Accounts: n, Txns: n * 2, TotalAmount: total}
	return c
}

func TestTheHoldsPageRendersWhetherOrNotAnythingIsHeld(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))

	b := body(t, s.get(t, "/holds"))
	if !strings.Contains(b, "</html>") {
		t.Fatal("an empty holds page did not finish rendering")
	}

	decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	b = body(t, s.get(t, "/holds"))
	if !strings.Contains(b, "</html>") {
		t.Fatal("the holds page did not finish rendering with holds in force")
	}
	c, _, _ := s.enforcer.at(1)
	for _, acct := range c.Accounts {
		if label := s.accountLabel(acct); !strings.Contains(b, label) {
			t.Errorf("holds page does not name held account %s", label)
		}
	}
}

// A page showing nothing frozen reads as broken. It is usually the envelope
// refusing to act, and the page has to say so out of the decisions' own
// recorded adjustments rather than from a restatement of the rule.
func TestTheHoldsPageExplainsWhyNothingIsFrozen(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))
	decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))

	b := body(t, s.get(t, "/holds"))
	if !strings.Contains(b, "Nothing is frozen, and that is the envelope working") {
		t.Error("the holds page does not explain a zero freeze count")
	}
}

// Lifting is a first-class audited operation: it appends, it does not erase.
func TestLiftingReleasesTheAccountAndLeavesItsHistoryIntact(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	if e.Held == 0 {
		t.Fatalf("nothing was held (action %q, adjustments %v)", e.Action, e.Adjustments)
	}
	before := s.events(t)
	account := before[0].Account
	imposed := before[0]

	var lifted struct {
		Lifted string `json:"lifted"`
		Seq    int64  `json:"seq"`
		Hash   string `json:"hash"`
	}
	res := s.post(t, "/api/holds/lift",
		"application/x-www-form-urlencoded",
		url.Values{"account": {account}, "reason": {"cleared by the analyst"}}.Encode())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("lift returned %d: %s", res.StatusCode, body(t, res))
	}
	if err := json.NewDecoder(res.Body).Decode(&lifted); err != nil {
		t.Fatalf("decode lift: %v", err)
	}
	if lifted.Lifted != account || lifted.Seq == 0 || lifted.Hash == "" {
		t.Fatalf("lift reported %+v", lifted)
	}

	after := s.events(t)
	if len(after) != len(before)+1 {
		t.Fatalf("lifting produced %d events, want one more than %d", len(after), len(before))
	}
	// The impose it released is still there, byte for byte.
	var found bool
	for _, ev := range after {
		if ev.Seq != imposed.Seq {
			continue
		}
		found = true
		if ev.Hash != imposed.Hash || ev.Tier != imposed.Tier || ev.Event != enforce.EventImpose {
			t.Errorf("the original impose was rewritten: %+v", ev)
		}
	}
	if !found {
		t.Error("lifting deleted the impose it released")
	}

	// A release performed by a person is authorised by that person, not by a
	// decision number.
	lift := after[len(after)-1]
	if lift.Event != enforce.EventLift {
		t.Fatalf("last event is %q, want a lift", lift.Event)
	}
	if lift.DecisionSeq != nil {
		t.Errorf("the lift names decision %d; a person's release names no decision", *lift.DecisionSeq)
	}
	if !strings.Contains(lift.Reason, "cleared by the analyst") {
		t.Errorf("lift reason %q lost the operator's words", lift.Reason)
	}

	held, err := s.holds.ActiveAt(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("read active holds: %v", err)
	}
	for _, h := range held {
		if h.Account == account {
			t.Errorf("account %s is still held after being lifted", account)
		}
	}
	if len(held) != e.Held-1 {
		t.Errorf("%d accounts still held, want %d", len(held), e.Held-1)
	}
	if got := decodeVerify(t, s.post(t, "/api/holds/verify", "", "")); !got.Valid {
		t.Errorf("the restriction chain stopped verifying after a lift: %s", got.Reason)
	}
}

func TestLiftingWithoutAnAccountIsRejected(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))
	decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	before := len(s.events(t))

	for _, b := range []struct{ contentType, body string }{
		{"application/x-www-form-urlencoded", ""},
		{"application/x-www-form-urlencoded", "account="},
		{"application/x-www-form-urlencoded", "reason=no+account+here"},
		{"application/json", `{"account":"0020|R001-0"}`},
	} {
		res := s.post(t, "/api/holds/lift", b.contentType, b.body)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("lift with body %q = %d, want 400", b.body, res.StatusCode)
		}
	}
	if now := len(s.events(t)); now != before {
		t.Errorf("a rejected lift wrote %d events", now-before)
	}
}

// Lifting an account nobody is holding must not damage the ledger or disturb
// the accounts that are held.
func TestLiftingAnAccountThatIsNotHeldIsSafe(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))
	e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))

	res := s.post(t, "/api/holds/lift", "application/x-www-form-urlencoded",
		url.Values{"account": {"0099|not-an-account"}}.Encode())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("lift on an unheld account = %d: %s", res.StatusCode, body(t, res))
	}

	held, err := s.holds.ActiveAt(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("read active holds: %v", err)
	}
	if len(held) != e.Held {
		t.Errorf("%d accounts held, want the %d that were held before", len(held), e.Held)
	}
	if got := decodeVerify(t, s.post(t, "/api/holds/verify", "", "")); !got.Valid {
		t.Errorf("the restriction chain stopped verifying: %s", got.Reason)
	}
}

func TestAnUntouchedRestrictionLedgerVerifies(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))

	empty := decodeVerify(t, s.post(t, "/api/holds/verify", "", ""))
	if !empty.Valid || empty.Events != 0 {
		t.Errorf("an empty ledger verified as %+v", empty)
	}

	e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	got := decodeVerify(t, s.post(t, "/api/holds/verify", "", ""))
	if !got.Valid {
		t.Fatalf("a ledger nobody touched failed to verify: %s", got.Reason)
	}
	if got.Events != e.Held {
		t.Errorf("verified %d events, %d accounts were leased", got.Events, e.Held)
	}
}

// An edited hold is caught by the same walk, through the console's own route.
func TestEditingAHoldIsCaughtByTheConsole(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))
	if e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", "")); e.Held == 0 {
		t.Fatalf("nothing was held (action %q)", e.Action)
	}
	events := s.events(t)
	target := events[len(events)-1]

	if _, err := s.pool.Exec(context.Background(),
		`UPDATE account_restrictions SET expires_at = expires_at + interval '30 days' WHERE seq = $1`,
		target.Seq); err != nil {
		t.Fatalf("edit the hold: %v", err)
	}

	got := decodeVerify(t, s.post(t, "/api/holds/verify", "", ""))
	if got.Valid {
		t.Fatal("a lease whose expiry was extended in place still verifies")
	}
	if got.Seq != target.Seq {
		t.Errorf("break reported at %d, the edit was at %d", got.Seq, target.Seq)
	}
	if !strings.Contains(got.Reason, "altered after it was written") {
		t.Errorf("reason %q does not name the edit", got.Reason)
	}
}

// One decision may not freeze an unbounded number of people, and past the cap
// the system declines to act at all rather than freezing an arbitrary subset.
func TestAnOverLargeRingIsDeclinedRatherThanPartlyFrozen(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	limit := s.enforcer.limits.MaxAccountsPerRing
	c := candidateOf(limit+1, 1_000_000)
	ev := s.evidenceOf(enforceRingBase+900, c, 0.99)
	a := s.chain.Assess(context.Background(), ev)
	if a.Action != agent.ActionBlock {
		t.Fatalf("policy returned %q; this test needs an approved block", a.Action)
	}
	seq := s.record(t, ev, a)

	held, bounds := s.enforceDecision(context.Background(), c, a, seq, s.enforcer.limits, true)
	if held != 0 {
		t.Errorf("froze %d of %d accounts; the cap is %d and the rule is all or nothing",
			held, len(c.Accounts), limit)
	}
	if !strings.Contains(strings.Join(bounds, " "), "declined to freeze") {
		t.Errorf("bounds %v do not record the declined action", bounds)
	}
	if events := s.events(t); len(events) != 0 {
		t.Errorf("%d leases written for a ring past the cap", len(events))
	}

	// One account under the cap, the same decision acts.
	ok := candidateOf(limit, 1_000_000)
	evOK := s.evidenceOf(enforceRingBase+901, ok, 0.99)
	aOK := s.chain.Assess(context.Background(), evOK)
	seqOK := s.record(t, evOK, aOK)
	if n, _ := s.enforceDecision(context.Background(), ok, aOK, seqOK, s.enforcer.limits, true); n != limit {
		t.Errorf("froze %d accounts at the cap of %d", n, limit)
	}
}

// Above the ceiling a person decides, however certain the machine is — and the
// consequence is that nothing is frozen.
func TestARingAboveTheValueCeilingCannotBeFrozenAutonomously(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.99))

	ceiling := s.chain.Policy.BlockMaxAmount
	c := candidateOf(4, ceiling*5)
	ev := s.evidenceOf(enforceRingBase+902, c, 0.99)
	a := s.chain.Assess(context.Background(), ev)

	if a.Action != agent.ActionHold {
		t.Fatalf("action = %q on a ring moving %.0f against a %.0f ceiling",
			a.Action, c.Features.TotalAmount, ceiling)
	}
	if a.Proposed != agent.ActionBlock {
		t.Errorf("the model's proposal was not preserved: %q", a.Proposed)
	}
	if !strings.Contains(strings.Join(a.Adjustments, " "), "autonomous block ceiling") {
		t.Errorf("adjustments %v do not name the ceiling", a.Adjustments)
	}

	seq := s.record(t, ev, a)
	held, _ := s.enforceDecision(context.Background(), c, a, seq, s.enforcer.limits, true)
	if held != len(c.Accounts) {
		t.Errorf("held %d accounts, want a watch over all %d", held, len(c.Accounts))
	}
	for _, e := range s.events(t) {
		if e.Tier == enforce.TierFrozen {
			t.Fatalf("account %s frozen above the value ceiling", e.Account)
		}
	}
}

// An account caught in two rings must not become easier to use because the
// second one was judged less harshly.
func TestAWatchCannotWeakenAFreezeThroughTheConsole(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	frozen := candidateOf(3, 1_000_000)
	evF := s.evidenceOf(enforceRingBase+910, frozen, 0.99)
	aF := s.chain.Assess(context.Background(), evF)
	if aF.Action != agent.ActionBlock {
		t.Fatalf("policy returned %q; this test needs an approved block", aF.Action)
	}
	seqF := s.record(t, evF, aF)
	if n, _ := s.enforceDecision(context.Background(), frozen, aF, seqF, s.enforcer.limits, true); n != 3 {
		t.Fatalf("froze %d of 3 accounts", n)
	}

	// The same accounts, judged less harshly by a second decision.
	watched := candidateOf(3, 1_000_000)
	evW := s.evidenceOf(enforceRingBase+911, watched, 0.99)
	aW := agent.Assessment{
		RingID: evW.RingID, Action: agent.ActionHold, Proposed: agent.ActionHold,
		Confidence: 0.6, Source: "test-tier", Rationale: "routed to a person",
		DecidedAt: time.Now().UTC(),
	}
	seqW := s.record(t, evW, aW)
	if n, _ := s.enforceDecision(context.Background(), watched, aW, seqW, s.enforcer.limits, true); n != 3 {
		t.Fatalf("watched %d of 3 accounts", n)
	}

	now := time.Now().UTC()
	held, err := s.holds.ActiveAt(context.Background(), now)
	if err != nil {
		t.Fatalf("read active holds: %v", err)
	}
	if len(held) != 3 {
		t.Fatalf("%d accounts held, want the 3 that were frozen", len(held))
	}
	for _, h := range held {
		if h.Tier != enforce.TierFrozen {
			t.Errorf("account %s reads as %q; a watch weakened a live freeze", h.Account, h.Tier)
		}
	}
	// The in-memory register the transfer check reads must say the same thing.
	for _, acct := range frozen.Accounts {
		if _, stopped := s.register.Stopped(acct, now); !stopped {
			t.Errorf("account %d is no longer stopped after a watch was imposed over it", acct)
		}
	}
}

// Nothing is permanent. A lease that has run out stops counting as a hold, and
// the page says so rather than dropping the row.
func TestALapsedLeaseIsShownAsLapsedAndCountsAsNeitherFrozenNorWatching(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	past := time.Now().UTC().Add(-48 * time.Hour)
	seq := s.record(t,
		agent.Evidence{RingID: enforceRingBase + 920, Score: 0.99, TotalAmount: 1_000_000},
		agent.Assessment{RingID: enforceRingBase + 920, Action: agent.ActionBlock,
			Proposed: agent.ActionBlock, Confidence: 0.95, Source: "test-tier",
			Rationale: "lapsed lease", DecidedAt: past})

	r := enforce.Restriction{
		Account: 1000, Tier: enforce.TierFrozen, RingID: enforceRingBase + 920,
		Pass: s.enforcer.limits.Pass, Reason: "expired lease",
		Imposed: past, Expires: past.Add(24 * time.Hour),
	}
	if _, _, err := s.holds.Impose(context.Background(), r, "0020|LAPSED-1", &seq); err != nil {
		t.Fatalf("impose: %v", err)
	}

	held, err := s.holds.ActiveAt(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("read active holds: %v", err)
	}
	if len(held) != 1 || !held[0].Expired {
		t.Fatalf("the lapsed lease reads as %+v, want one expired hold", held)
	}

	b := body(t, s.get(t, "/holds"))
	if !strings.Contains(b, "0020|LAPSED-1") {
		t.Error("the holds page dropped the lapsed lease instead of showing it")
	}
	if !strings.Contains(b, "lapsed") {
		t.Error("the holds page does not mark the lease as lapsed")
	}
	if !strings.Contains(b, "Nothing is frozen") {
		t.Error("a lapsed freeze is still being counted as frozen")
	}
}

// The enforcement queue is the part of the page that shows what the block gate
// reads on each row, so a page with no freezes explains itself on the same line.
func TestTheEnforcementQueueShowsWhatTheBlockGateReads(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))

	rows := s.enforcementQueue(context.Background(), 5)
	if len(rows) == 0 {
		t.Fatal("the enforcement queue is empty")
	}
	policy := s.chain.Policy
	for _, row := range rows {
		c, sc, ok := s.enforcer.at(row.Rank)
		if !ok {
			t.Fatalf("queue row %d resolves to no candidate", row.Rank)
		}
		if row.Score != sc || row.Total != c.Features.TotalAmount {
			t.Errorf("row %d shows score %.4f/%.2f, the candidate is %.4f/%.2f",
				row.Rank, row.Score, row.Total, sc, c.Features.TotalAmount)
		}
		want := sc >= policy.BlockMinScore && c.Features.TotalAmount <= policy.BlockMaxAmount
		if row.Blocks != want {
			t.Errorf("row %d says blocks=%v, the gates say %v", row.Rank, row.Blocks, want)
		}
	}

	// Once a row has been decided, the queue carries the decision beside it.
	e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	rows = s.enforcementQueue(context.Background(), 5)
	if rows[0].Decision != e.Action || rows[0].Proposed != e.Proposed {
		t.Errorf("queue row 1 shows %q/%q, the decision was %q/%q",
			rows[0].Decision, rows[0].Proposed, e.Action, e.Proposed)
	}
	if rows[0].Held != e.Held {
		t.Errorf("queue row 1 counts %d holds, %d were placed", rows[0].Held, e.Held)
	}
}

// The analyst pass leases nothing even when its decision would otherwise
// authorise a freeze. This is the invariant the geometry split exists to keep.
func TestTheAnalystLimitsNeverLease(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	c := candidateOf(4, 1_000_000)
	ev := s.evidenceOf(930, c, 0.99)
	a := s.chain.Assess(context.Background(), ev)
	if a.Action != agent.ActionBlock {
		t.Fatalf("policy returned %q; this test needs an approved block", a.Action)
	}
	seq := s.record(t, ev, a)

	held, bounds := s.enforceDecision(context.Background(), c, a, seq, s.limits, false)
	if held != 0 {
		t.Errorf("the analyst pass leased %d accounts", held)
	}
	joined := strings.Join(bounds, " ")
	if !strings.Contains(joined, s.limits.Pass) || !strings.Contains(joined, s.enforcer.limits.Pass) {
		t.Errorf("bounds %v do not name which pass leases and which does not", bounds)
	}
	if events := s.events(t); len(events) != 0 {
		t.Errorf("%d leases written by the analyst pass", len(events))
	}
}

// The seeded decisions are what a judge sees before touching anything: the
// analyst pass records and holds nothing, the enforcement pass is what leases.
func TestSeedingRecordsBothPassesAndLeasesOnlyFromTheNarrowOne(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))
	ctx := context.Background()

	s.SeedAudit(ctx, 2, 2)

	if n := s.auditCount(t); n != 4 {
		t.Fatalf("seeded %d decisions, want 4", n)
	}
	rings := map[int]bool{}
	rows, err := s.pool.Query(ctx, `SELECT ring_id FROM audit_log ORDER BY seq`)
	if err != nil {
		t.Fatalf("read decisions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ring int
		if err := rows.Scan(&ring); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rings[ring] = true
	}
	analyst, enforcing := 0, 0
	for ring := range rings {
		if ring >= enforceRingBase {
			enforcing++
		} else {
			analyst++
		}
	}
	if analyst != 2 || enforcing != 2 {
		t.Errorf("seeded %d analyst and %d enforcement decisions, want 2 and 2", analyst, enforcing)
	}

	for _, e := range s.events(t) {
		if e.RingID < enforceRingBase {
			t.Errorf("seeding leased on analyst ring %d", e.RingID)
		}
		if e.DecisionSeq == nil {
			t.Errorf("seeded lease on %s names no decision", e.Account)
		}
	}

	// Seeding again must not double the log.
	s.SeedAudit(ctx, 2, 2)
	if n := s.auditCount(t); n != 4 {
		t.Errorf("re-seeding an already-seeded log left %d decisions", n)
	}
}

// The console renders account labels from the ledger, and an index outside it
// must not panic the page.
func TestAccountLabelsSurviveAnIndexOutsideTheLedger(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	if got := s.accountLabel(0); got != s.ledger.Accounts[0] {
		t.Errorf("account 0 labelled %q, want %q", got, s.ledger.Accounts[0])
	}
	if got := s.accountLabel(int32(len(s.ledger.Accounts))); !strings.HasPrefix(got, "account-") {
		t.Errorf("an index past the ledger labelled %q", got)
	}
}

// The enforcement queue used to render the answering tier through a helper that
// truncated it to 16 characters — which is exactly the length at which
// "gemini:gemini-3.7-flash" and "gemini:gemini-3.5-flash-lite" become the same
// string. The one thing the fallback demonstration has to show on that page is
// which tier answered, so a column that cannot tell the tiers apart is worse
// than an absent one.
func TestTheEnforcementQueueNamesTheTierThatAnswered(t *testing.T) {
	const primary, secondary = "gemini:gemini-3.7-flash", "gemini:gemini-3.5-flash-lite"

	chain := agent.NewChain(agent.DefaultPolicy(),
		failingTier{name: primary, err: errors.New("503 unavailable")},
		fixedTier{name: secondary, prop: agent.Proposal{
			Action: agent.ActionHold, Confidence: 0.7, Rationale: "conservation is high",
		}},
	)
	s := newDBTestServer(t, chain)

	got := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	if got.Source != secondary {
		t.Fatalf("answered by %q, want the second tier %q", got.Source, secondary)
	}

	b := body(t, s.get(t, "/holds"))
	if !strings.Contains(b, secondary) {
		t.Errorf("the holds page does not name the tier that answered (%q)", secondary)
	}
	// The failure mode was a shared prefix, so naming the primary would be just
	// as wrong as truncating: the page must say which one actually answered.
	if strings.Contains(b, ">"+primary+"<") {
		t.Errorf("the holds page names %q, which failed and did not decide", primary)
	}
}
