package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/vinayaktyagi10/warren/internal/agent"
)

// Step 5 of the demo: verify, tamper, verify again, and see the break located
// and named. These tests protect that sequence at the route level; the chain
// semantics themselves are pinned in internal/audit and internal/enforce
// against the same real Postgres.

type verifyResponse struct {
	Valid   bool   `json:"valid"`
	Entries int    `json:"entries"`
	Events  int    `json:"events"`
	Seq     int64  `json:"seq"`
	Reason  string `json:"reason"`
}

func decodeVerify(t *testing.T, res *http.Response) verifyResponse {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("verify returned %d: %s", res.StatusCode, body(t, res))
	}
	var out verifyResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	return out
}

// decide records n analyst decisions, returning their sequence numbers.
func (s *testServer) decide(t *testing.T, n int) []int64 {
	t.Helper()
	var seqs []int64
	for rank := 1; rank <= n; rank++ {
		got := decodeAssess(t, s.post(t, "/api/assess/"+strconv.Itoa(rank), "", ""))
		seqs = append(seqs, got.AuditSeq)
	}
	return seqs
}

func TestAnUntouchedDecisionLogVerifiesThroughTheConsole(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))
	s.decide(t, 3)

	got := decodeVerify(t, s.post(t, "/api/audit/verify", "", ""))
	if !got.Valid {
		t.Fatalf("a log nobody touched failed to verify: %s", got.Reason)
	}
	if got.Entries != 3 {
		t.Errorf("verified %d entries, want 3", got.Entries)
	}
	if got.Seq != 0 || got.Reason != "" {
		t.Errorf("a valid chain named a break at %d: %s", got.Seq, got.Reason)
	}
}

// The tamper button rewrites a decision in place and leaves every hash alone —
// what someone covering their tracks would do. Verification has to catch it and
// say where.
func TestTamperingIsCaughtAndTheFirstBreakIsNamed(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))
	seqs := s.decide(t, 4)

	var tampered struct {
		Tampered bool  `json:"tampered"`
		Seq      int64 `json:"seq"`
	}
	res := s.post(t, "/api/audit/tamper/"+strconv.FormatInt(seqs[1], 10), "", "")
	if err := json.NewDecoder(res.Body).Decode(&tampered); err != nil {
		t.Fatalf("decode tamper: %v", err)
	}
	if !tampered.Tampered || tampered.Seq != seqs[1] {
		t.Fatalf("tamper reported %+v, want entry %d rewritten", tampered, seqs[1])
	}

	// The row really was rewritten, and the hash really was left alone.
	var action string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT action FROM audit_log WHERE seq = $1`, seqs[1]).Scan(&action); err != nil {
		t.Fatalf("read the rewritten row: %v", err)
	}
	if action != string(agent.ActionAllow) {
		t.Errorf("entry %d now reads %q, want the rewritten allow", seqs[1], action)
	}

	got := decodeVerify(t, s.post(t, "/api/audit/verify", "", ""))
	if got.Valid {
		t.Fatal("the edited log still verifies")
	}
	if got.Seq != seqs[1] {
		t.Errorf("break reported at entry %d, the edit was at %d", got.Seq, seqs[1])
	}
	if !strings.Contains(got.Reason, "edited after it was written") {
		t.Errorf("reason %q does not name the edit", got.Reason)
	}
	if got.Entries != 4 {
		t.Errorf("verified %d entries, want all 4 walked", got.Entries)
	}
}

// Every entry after a break inherits it, so only the first is worth reporting.
func TestOnlyTheEarliestBreakIsReportedThroughTheConsole(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))
	seqs := s.decide(t, 4)

	s.post(t, "/api/audit/tamper/"+strconv.FormatInt(seqs[3], 10), "", "")
	s.post(t, "/api/audit/tamper/"+strconv.FormatInt(seqs[1], 10), "", "")

	got := decodeVerify(t, s.post(t, "/api/audit/verify", "", ""))
	if got.Valid {
		t.Fatal("a log with two edited entries verified")
	}
	if got.Seq != seqs[1] {
		t.Errorf("reported the break at %d, want the earliest at %d", got.Seq, seqs[1])
	}
}

func TestTamperingAnEntryThatDoesNotExistChangesNothing(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))
	s.decide(t, 2)

	var out struct {
		Tampered bool `json:"tampered"`
	}
	res := s.post(t, "/api/audit/tamper/9999", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("tamper on a missing entry returned %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode tamper: %v", err)
	}
	if out.Tampered {
		t.Error("reported having rewritten an entry that does not exist")
	}
	if got := decodeVerify(t, s.post(t, "/api/audit/verify", "", "")); !got.Valid {
		t.Errorf("the log stopped verifying: %s", got.Reason)
	}
}

func TestAMalformedSequenceNumberIsRejected(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionHold, 0.7))
	s.decide(t, 1)

	for _, path := range []string{"/api/audit/tamper/abc", "/api/audit/tamper/1.5", "/api/audit/tamper/ "} {
		if res := s.post(t, path, "", ""); res.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", path, res.StatusCode)
		}
	}
	if got := decodeVerify(t, s.post(t, "/api/audit/verify", "", "")); !got.Valid {
		t.Errorf("a rejected tamper damaged the chain: %s", got.Reason)
	}
}

// The audit page is what the room is looking at when the tamper is run.
func TestTheAuditPageShowsWhatWasDecidedAndWhatWasProposed(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionAllow, 0.99))
	seqs := s.decide(t, 2)

	b := body(t, s.get(t, "/audit"))
	for _, seq := range seqs {
		if !strings.Contains(b, "#"+strconv.FormatInt(seq, 10)) {
			t.Errorf("audit page does not show entry %d", seq)
		}
	}
	// The proposal is preserved beside the decision, so the disagreement is
	// visible. On a well-scored ring, allow is withheld and both must show.
	for _, want := range []string{"allow", "hold_for_review", "allow withheld"} {
		if !strings.Contains(b, want) {
			t.Errorf("audit page does not show %q", want)
		}
	}
}

// The two chains are separate ledgers with separate genesis values. Breaking one
// must not make the other report a break — that is the whole point of the join
// the holds page makes between them.
func TestTheTwoChainsAreIndependent(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	if e.Held == 0 {
		t.Fatalf("nothing was held, so there is no restriction chain to test "+
			"(action %q, adjustments %v)", e.Action, e.Adjustments)
	}

	s.post(t, "/api/audit/tamper/"+strconv.FormatInt(e.AuditSeq, 10), "", "")

	if got := decodeVerify(t, s.post(t, "/api/audit/verify", "", "")); got.Valid {
		t.Error("the decision chain still verifies after an edit")
	}
	holds := decodeVerify(t, s.post(t, "/api/holds/verify", "", ""))
	if !holds.Valid {
		t.Errorf("editing a decision broke the restriction chain too: %s", holds.Reason)
	}
	if holds.Events != e.Held {
		t.Errorf("restriction chain reports %d events, %d accounts were held", holds.Events, e.Held)
	}
}

// The consequence is the point of the demo: a broken decision leaves the money
// it authorised held on an authority that no longer checks out.
func TestABrokenDecisionOrphansTheHoldsItAuthorised(t *testing.T) {
	s := newDBTestServer(t, chainProposing(agent.ActionBlock, 0.95))

	e := decodeAssess(t, s.post(t, "/api/enforce/assess/1", "", ""))
	if e.Held == 0 {
		t.Fatalf("nothing was held (action %q, adjustments %v)", e.Action, e.Adjustments)
	}

	if b := body(t, s.get(t, "/holds")); strings.Contains(b, "no longer verifies") {
		t.Fatal("holds were reported as orphaned before anything was tampered with")
	}

	s.post(t, "/api/audit/tamper/"+strconv.FormatInt(e.AuditSeq, 10), "", "")

	b := body(t, s.get(t, "/holds"))
	if !strings.Contains(b, "no longer verifies") {
		t.Error("the holds page does not report holds left on a broken authority")
	}
	if !strings.Contains(b, strconv.Itoa(e.Held)+" holds rest on an authority") {
		t.Errorf("the holds page does not count the %d affected holds", e.Held)
	}
}
