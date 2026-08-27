package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/audit"
	"github.com/vinayaktyagi10/warren/internal/dbtest"
)

// These run against Postgres because the chaining depends on a locked read of
// the previous row inside the same transaction as the insert. A fake would
// paper over exactly the property being claimed.

func decision(ring int, action agent.Action) (agent.Evidence, agent.Assessment) {
	ev := agent.Evidence{RingID: ring, Typology: "CYCLE", Accounts: 8, Txns: 21,
		TotalAmount: 17_700_000, Conservation: 0.949, Score: 0.97}
	return ev, agent.Assessment{
		RingID: ring, Action: action, Proposed: agent.ActionBlock, Confidence: 0.94,
		Source: "test", Rationale: "conservation is high across three intermediaries",
		Adjustments: []string{"block withheld on the value ceiling"},
		DecidedAt:   time.Now().UTC(),
	}
}

func TestAnUntouchedLogVerifies(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	l, err := audit.New(ctx, p)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}

	// An empty log verifies: there is nothing to be wrong about.
	res, err := l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify empty: %v", err)
	}
	if !res.Valid || res.Entries != 0 {
		t.Errorf("empty log: valid=%v entries=%d", res.Valid, res.Entries)
	}

	for i := 1; i <= 5; i++ {
		ev, a := decision(i, agent.ActionHold)
		if _, _, err := l.Record(ctx, ev, a); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	res, err = l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("a log nobody touched failed to verify: seq %d, %s", res.BrokenSeq, res.BrokenReason)
	}
	if res.Entries != 5 {
		t.Errorf("entries = %d, want 5", res.Entries)
	}
	if n, _ := l.Count(ctx); n != 5 {
		t.Errorf("count = %d, want 5", n)
	}
}

// The decision with nothing to adjust is the one that stayed broken longest: a
// nil slice reaches Postgres as NULL and fails the NOT NULL column. It only
// happens when nothing intervened, which is the least-exercised path.
func TestADecisionWithNothingToAdjustRecords(t *testing.T) {
	ctx := context.Background()
	l, err := audit.New(ctx, dbtest.Pool(t))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	ev, a := decision(1, agent.ActionHold)
	a.Proposed = a.Action
	a.Adjustments = nil

	if _, _, err := l.Record(ctx, ev, a); err != nil {
		t.Fatalf("recording a decision with no adjustments: %v", err)
	}
	res, err := l.Verify(ctx)
	if err != nil || !res.Valid {
		t.Fatalf("verify: %v / %+v", err, res)
	}
}

// This is the demo. Editing a row in place, without touching a hash, is what
// someone covering their tracks would do.
func TestEditingADecisionInPlaceIsCaught(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	l, err := audit.New(ctx, p)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	for i := 1; i <= 5; i++ {
		ev, a := decision(i, agent.ActionHold)
		if _, _, err := l.Record(ctx, ev, a); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	if _, err := p.Exec(ctx,
		`UPDATE audit_log SET action = 'allow', rationale = 'nothing to see here' WHERE seq = 3`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Valid {
		t.Fatal("an edited decision verified")
	}
	if res.BrokenSeq != 3 {
		t.Errorf("break reported at seq %d, want 3", res.BrokenSeq)
	}
	if !strings.Contains(res.BrokenReason, "edited after it was written") {
		t.Errorf("reason does not name the edit: %q", res.BrokenReason)
	}
	if res.Entries != 5 {
		t.Errorf("entries = %d; verification must still walk the whole log", res.Entries)
	}
}

// Deleting a row is the other thing someone would try, and it breaks a
// different way: the next row links to a predecessor that is no longer there.
func TestRemovingADecisionIsCaught(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	l, err := audit.New(ctx, p)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	for i := 1; i <= 5; i++ {
		ev, a := decision(i, agent.ActionHold)
		if _, _, err := l.Record(ctx, ev, a); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if _, err := p.Exec(ctx, `DELETE FROM audit_log WHERE seq = 3`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Valid {
		t.Fatal("a log with a row removed verified")
	}
	if res.BrokenSeq != 4 {
		t.Errorf("break reported at seq %d, want 4 — the row that lost its predecessor", res.BrokenSeq)
	}
	if !strings.Contains(res.BrokenReason, "removed, reordered, or inserted") {
		t.Errorf("reason does not name the removal: %q", res.BrokenReason)
	}
}

// Truncating the front of the log must not verify as a shorter but internally
// consistent chain. This is the only thing the genesis constant is for.
func TestRemovingTheEarliestDecisionsIsCaught(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	l, err := audit.New(ctx, p)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	for i := 1; i <= 5; i++ {
		ev, a := decision(i, agent.ActionHold)
		if _, _, err := l.Record(ctx, ev, a); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if _, err := p.Exec(ctx, `DELETE FROM audit_log WHERE seq <= 2`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Valid {
		t.Fatal("a log with its first two entries removed verified as a valid shorter chain")
	}
	if res.BrokenSeq != 3 {
		t.Errorf("break reported at seq %d, want 3", res.BrokenSeq)
	}
}

// Only the first break is reported. Every row after one inherits it, and
// listing them all buries the one that matters.
func TestOnlyTheFirstBreakIsReported(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	l, err := audit.New(ctx, p)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	for i := 1; i <= 5; i++ {
		ev, a := decision(i, agent.ActionHold)
		if _, _, err := l.Record(ctx, ev, a); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if _, err := p.Exec(ctx, `UPDATE audit_log SET confidence = 0.1 WHERE seq IN (2, 4)`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	res, err := l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.BrokenSeq != 2 {
		t.Errorf("break reported at seq %d, want the earliest at 2", res.BrokenSeq)
	}
}

// Concurrent writers must not both chain onto the same predecessor. Without the
// locked read the log forks and verification fails on a log nobody tampered
// with, which would be worse than useless.
func TestConcurrentWritersDoNotForkTheChain(t *testing.T) {
	ctx := context.Background()
	p := dbtest.Pool(t)
	l, err := audit.New(ctx, p)
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}

	const writers = 8
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			ev, a := decision(i, agent.ActionHold)
			_, _, err := l.Record(ctx, ev, a)
			errs <- err
		}(i)
	}
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent record: %v", err)
		}
	}

	res, err := l.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("%d concurrent writers forked the chain at seq %d: %s",
			writers, res.BrokenSeq, res.BrokenReason)
	}
	if res.Entries != writers {
		t.Errorf("entries = %d, want %d", res.Entries, writers)
	}
}
