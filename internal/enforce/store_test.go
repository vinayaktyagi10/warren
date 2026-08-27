package enforce_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/dbtest"
	"github.com/vinayaktyagi10/warren/internal/enforce"
)

func newStore(t *testing.T) (*enforce.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := enforce.NewStore(ctx, dbtest.Pool(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, ctx
}

func lease(account string, tier enforce.Tier, ring int, hours time.Duration) enforce.Restriction {
	now := time.Now().UTC()
	return enforce.Restriction{
		Tier: tier, RingID: ring, Pass: "24h/6h", Reason: "test",
		Imposed: now, Expires: now.Add(hours),
	}
}

// The chain has to survive its own round trip through Postgres before it can
// claim to detect anything: a ledger that fails to verify the moment it is read
// back reports tampering on every run and the signal is worthless.
func TestALedgerNobodyTouchedVerifies(t *testing.T) {
	s, ctx := newStore(t)

	res, err := s.Verify(ctx)
	if err != nil || !res.Valid || res.Events != 0 {
		t.Fatalf("empty ledger: %+v / %v", res, err)
	}

	seq := int64(7)
	for i := 0; i < 5; i++ {
		if _, _, err := s.Impose(ctx, lease("bank|acct", enforce.TierFrozen, i, 72*time.Hour),
			"bank|acct", &seq); err != nil {
			t.Fatalf("impose: %v", err)
		}
	}
	if _, _, err := s.Lift(ctx, "bank|acct", "analyst released it"); err != nil {
		t.Fatalf("lift: %v", err)
	}

	res, err = s.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("a ledger nobody touched failed to verify at seq %d: %s", res.BrokenSeq, res.BrokenReason)
	}
	if res.Events != 6 {
		t.Errorf("events = %d, want 6", res.Events)
	}
}

func TestEditingAHoldInPlaceIsCaught(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	s, err := enforce.NewStore(ctx, p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seq := int64(1)
	for i := 0; i < 4; i++ {
		if _, _, err := s.Impose(ctx, lease("bank|a", enforce.TierFrozen, i, 72*time.Hour),
			"bank|a", &seq); err != nil {
			t.Fatalf("impose: %v", err)
		}
	}

	// Stretching a hold's expiry is the edit with a motive behind it.
	if _, err := p.Exec(ctx,
		`UPDATE account_restrictions SET expires_at = expires_at + interval '30 days' WHERE seq = 2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := s.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Valid {
		t.Fatal("a lengthened hold verified")
	}
	if res.BrokenSeq != 2 {
		t.Errorf("break at seq %d, want 2", res.BrokenSeq)
	}
	if !strings.Contains(res.BrokenReason, "altered after it was written") {
		t.Errorf("reason: %q", res.BrokenReason)
	}
}

// The two chains use different genesis constants so a row cannot be lifted from
// one and replayed into the other.
func TestTheTwoChainsDoNotShareAGenesis(t *testing.T) {
	if enforce.GenesisHash == "warren-genesis" {
		t.Error("the restriction chain starts from the decision log's genesis")
	}
}

// Current state is the fold of the events, in order — not the latest row.
func TestActiveStateIsTheFoldOfTheEvents(t *testing.T) {
	s, ctx := newStore(t)
	seq := int64(1)
	now := time.Now().UTC()

	mustImpose := func(acct string, tier enforce.Tier, d time.Duration) {
		t.Helper()
		r := enforce.Restriction{Tier: tier, RingID: 1, Pass: "24h/6h", Reason: "test",
			Imposed: now, Expires: now.Add(d)}
		if _, _, err := s.Impose(ctx, r, acct, &seq); err != nil {
			t.Fatalf("impose %s: %v", acct, err)
		}
	}

	mustImpose("bank|held", enforce.TierFrozen, 72*time.Hour)
	mustImpose("bank|lifted", enforce.TierFrozen, 72*time.Hour)
	mustImpose("bank|expired", enforce.TierWatch, time.Hour)
	if _, _, err := s.Lift(ctx, "bank|lifted", "analyst released it"); err != nil {
		t.Fatalf("lift: %v", err)
	}

	held, err := s.ActiveAt(ctx, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("ActiveAt: %v", err)
	}
	state := map[string]bool{}
	for _, h := range held {
		state[h.Account] = !h.Expired
	}
	if !state["bank|held"] {
		t.Error("a live freeze is not reported as held")
	}
	if _, present := state["bank|lifted"]; present {
		t.Error("a lifted account is still in the current state")
	}
	if state["bank|expired"] {
		t.Error("a watch past its expiry is reported as live")
	}
}

// A watch must never weaken a freeze, whichever order the two arrive in — the
// window passes overlap, so the same account is genuinely re-leased.
func TestAWatchCannotWeakenAFreeze(t *testing.T) {
	s, ctx := newStore(t)
	seq := int64(1)
	now := time.Now().UTC()

	freeze := enforce.Restriction{Tier: enforce.TierFrozen, RingID: 1, Reason: "block",
		Imposed: now, Expires: now.Add(72 * time.Hour)}
	watch := enforce.Restriction{Tier: enforce.TierWatch, RingID: 2, Reason: "hold",
		Imposed: now, Expires: now.Add(14 * 24 * time.Hour)}

	if _, _, err := s.Impose(ctx, freeze, "bank|a", &seq); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Impose(ctx, watch, "bank|a", &seq); err != nil {
		t.Fatal(err)
	}

	held, err := s.ActiveAt(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 {
		t.Fatalf("got %d held accounts, want 1", len(held))
	}
	if held[0].Tier != enforce.TierFrozen {
		t.Errorf("tier = %s; the longer but weaker watch replaced the freeze", held[0].Tier)
	}
}

// A lift is authorised by the person performing it, so it carries no decision
// sequence. Attributing it to a decision would credit a human's judgement to
// the machine.
func TestALiftNamesNoDecision(t *testing.T) {
	s, ctx := newStore(t)
	seq := int64(9)
	if _, _, err := s.Impose(ctx, lease("bank|a", enforce.TierFrozen, 1, time.Hour), "bank|a", &seq); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Lift(ctx, "bank|a", "released by an analyst"); err != nil {
		t.Fatal(err)
	}

	events, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].DecisionSeq == nil || *events[0].DecisionSeq != 9 {
		t.Errorf("the impose does not name its decision: %v", events[0].DecisionSeq)
	}
	if events[1].DecisionSeq != nil {
		t.Errorf("the lift names decision %d", *events[1].DecisionSeq)
	}
}

// A lift is appended, never a deletion, so "frozen for a while and then
// released" survives in the record.
func TestLiftingIsRecordedRatherThanErasing(t *testing.T) {
	s, ctx := newStore(t)
	seq := int64(1)
	if _, _, err := s.Impose(ctx, lease("bank|a", enforce.TierFrozen, 1, 72*time.Hour), "bank|a", &seq); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Lift(ctx, "bank|a", "released by an analyst"); err != nil {
		t.Fatal(err)
	}

	events, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("ledger holds %d events, want the impose and the lift", len(events))
	}
	if events[0].Event != enforce.EventImpose || events[1].Event != enforce.EventLift {
		t.Errorf("events = %s, %s", events[0].Event, events[1].Event)
	}
	if !strings.Contains(events[1].Reason, "analyst") {
		t.Errorf("the lift did not keep its reason: %q", events[1].Reason)
	}
}

func TestConcurrentWritersDoNotForkTheLedger(t *testing.T) {
	s, ctx := newStore(t)
	seq := int64(1)

	const writers = 8
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			_, _, err := s.Impose(ctx, lease("bank|a", enforce.TierFrozen, i, 72*time.Hour), "bank|a", &seq)
			errs <- err
		}(i)
	}
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent impose: %v", err)
		}
	}

	res, err := s.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("%d concurrent writers forked the ledger at seq %d: %s",
			writers, res.BrokenSeq, res.BrokenReason)
	}
}
