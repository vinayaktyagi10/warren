package web

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/detect"
)

// The read routes are the pages a judge clicks through. None of them writes
// anything, so none of them needs a database, and all of them are one bad index
// away from a panic in front of an audience.

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestEveryReadRouteRenders(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	for _, path := range []string{"/", "/queue", "/ring/1", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			res := s.get(t, path)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, res.StatusCode)
			}
			if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("content type %q, want html", ct)
			}
			// A template that fails half way through still returns 200 with a
			// truncated page, because ExecuteTemplate writes as it goes. The
			// closing tag is the cheap check that it finished.
			if b := body(t, res); !strings.Contains(b, "</html>") {
				t.Errorf("GET %s: page does not reach </html>; render failed part way", path)
			}
		})
	}
}

// The queue is the first screen of the demo, and the claim it makes is that it
// is ranked. A queue in candidate order rather than score order would look
// identical and be worthless.
func TestQueueIsRankedAndBounded(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	b := body(t, s.get(t, "/queue"))
	first := strings.Index(b, "/ring/1")
	second := strings.Index(b, "/ring/2")
	if first < 0 || second < 0 {
		t.Fatalf("queue does not link the top two alerts")
	}
	if first > second {
		t.Errorf("rank 2 is rendered before rank 1")
	}

	// 50 rows, because 50 is the budget the headline precision is quoted at.
	if n := strings.Count(b, "/ring/"); n > 50 {
		t.Errorf("queue shows %d alerts, more than the 50-alert budget it quotes", n)
	}

	s1, s2 := scoreAt(t, s, 1), scoreAt(t, s, 2)
	if s1 < s2 {
		t.Errorf("rank 1 scored %.4f, below rank 2 at %.4f", s1, s2)
	}
}

func scoreAt(t *testing.T, s *testServer, rank int) float64 {
	t.Helper()
	_, sc, ok := s.ringAt(rank)
	if !ok {
		t.Fatalf("no candidate at rank %d", rank)
	}
	return sc
}

// A rank typed into the address bar is the easiest way to break the console in
// front of someone. Every out-of-range form has to land somewhere deliberate.
func TestRingRankBoundaries(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())
	last := len(s.order)

	tests := []struct {
		path string
		want int
	}{
		{"/ring/1", http.StatusOK},
		{"/ring/0", http.StatusNotFound},
		{"/ring/-1", http.StatusNotFound},
		{"/ring/" + strconv.Itoa(last), http.StatusOK},
		{"/ring/" + strconv.Itoa(last+1), http.StatusNotFound},
		{"/ring/999999", http.StatusNotFound},
		{"/ring/abc", http.StatusBadRequest},
		{"/ring/1.5", http.StatusBadRequest},
		{"/ring/ ", http.StatusBadRequest},
	}
	for _, tt := range tests {
		res := s.get(t, tt.path)
		if res.StatusCode != tt.want {
			t.Errorf("GET %s = %d, want %d", tt.path, res.StatusCode, tt.want)
		}
	}
}

// The hero page is the console's front door, and it is built from a case that
// may not exist in a given ledger. Both branches have to be safe.
func TestHeroFallsBackToTheQueueWhenThereIsNoCase(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())
	if s.heroPattern == 0 {
		t.Fatal("the synthetic ledger was supposed to yield a hero case")
	}

	s.heroPattern, s.heroTxns = 0, nil
	client := s.ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	res, err := client.Get(s.ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusFound {
		t.Fatalf("GET / with no hero case = %d, want 302", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/queue" {
		t.Errorf("redirected to %q, want /queue", loc)
	}
}

// The performance page prints the fitted coefficients by name. If the names and
// the weights ever drift apart every explanation on the page is mislabelled,
// which is worse than having no page.
func TestMetricsNamesEveryFittedCoefficient(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())
	b := body(t, s.get(t, "/metrics"))

	rows := s.modelWeights()
	if len(rows) != len(s.model.Weights) {
		t.Fatalf("%d weight rows for %d weights", len(rows), len(s.model.Weights))
	}
	for _, r := range rows {
		if strings.HasPrefix(r.Name, "feature_") {
			t.Errorf("coefficient %s has no name from the feature set", r.Name)
		}
		if !strings.Contains(b, r.Name) {
			t.Errorf("performance page does not print coefficient %q", r.Name)
		}
	}
	if len(rows) > 1 && rows[0].Abs < rows[1].Abs {
		t.Error("coefficients are not ordered by magnitude")
	}
}

// The graph on the ring page is the picture the demo talks over. A candidate
// whose transfers cannot be resolved must draw nothing rather than panic.
func TestRingViewDrawsOnlyTheTransfersItHas(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	v, ok := s.buildRingView(1, true)
	if !ok {
		t.Fatal("no ring at rank 1")
	}
	c, _, _ := s.ringAt(1)
	if len(v.Edges) != len(c.TxnIDs) {
		t.Errorf("%d edges drawn for %d transfers", len(v.Edges), len(c.TxnIDs))
	}
	if len(v.Nodes) != c.Features.Accounts {
		t.Errorf("%d nodes drawn for %d accounts", len(v.Nodes), c.Features.Accounts)
	}

	nodes, edges := s.layout(detect.Candidate{TxnIDs: []int32{-1, -2}})
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("unknown transfers drew %d nodes and %d edges, want nothing",
			len(nodes), len(edges))
	}
	if nodes, edges := s.layout(detect.Candidate{}); len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("a candidate with no accounts drew %d nodes and %d edges",
			len(nodes), len(edges))
	}
}

// The ring page shows the decision once one exists, including what the model
// asked for beside what the system did. That pairing is the pitch.
func TestRingViewCarriesTheDecisionAndTheProposalItOverrode(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	s.mu.Lock()
	s.decided[1] = agent.Assessment{
		RingID: 1, Action: agent.ActionHold, Proposed: agent.ActionBlock,
		Confidence: 0.91, Source: "test-tier",
		Adjustments: []string{"ring moves 17700000.00, above the 10000000.00 autonomous block ceiling"},
	}
	s.mu.Unlock()

	v, _ := s.buildRingView(1, false)
	if v.Assessment == nil {
		t.Fatal("the ring view lost the recorded decision")
	}
	if !v.Assessment.Adjusted() {
		t.Error("an assessment whose action differs from its proposal is not marked adjusted")
	}

	b := body(t, s.get(t, "/ring/1"))
	for _, want := range []string{"hold_for_review", "block", "autonomous block ceiling"} {
		if !strings.Contains(b, want) {
			t.Errorf("ring page does not show %q", want)
		}
	}
}

// Truth labels are shown in the console because this is a demonstration against
// a labelled dataset — and they must never reach the assessor.
func TestEvidenceCarriesNoLabelAndNoFreeText(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	ev, ok := s.evidenceFor(1)
	if !ok {
		t.Fatal("no evidence for rank 1")
	}
	c, sc, _ := s.ringAt(1)
	if ev.Score != sc || ev.TotalAmount != c.Features.TotalAmount {
		t.Errorf("evidence does not match the candidate it describes")
	}
	if ev.RingID != 1 {
		t.Errorf("ring id = %d, want the analyst rank", ev.RingID)
	}
	// Every field on Evidence is a number, a typology or a timestamp. The test
	// that matters is that the account labels — the only operator-visible
	// strings in the ledger — are nowhere in it.
	for _, label := range s.ledger.Accounts[:5] {
		if strings.Contains(ev.Typology, label) {
			t.Errorf("evidence carries the account label %q", label)
		}
	}
}

// The two passes both rank from 1, so the audit log would say "ring 3" about two
// different groups of accounts without the offset — and the holds page joins on
// exactly that number.
func TestTheTwoPassesDoNotShareRingIdentifiers(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	analyst, _ := s.evidenceFor(3)
	c, sc, ok := s.enforcer.at(3)
	if !ok {
		t.Fatal("no enforcement candidate at rank 3")
	}
	enforcing := s.evidenceOf(enforceRingBase+3, c, sc)

	if analyst.RingID == enforcing.RingID {
		t.Fatalf("both passes recorded ring id %d", analyst.RingID)
	}
	if enforcing.RingID != enforceRingBase+3 {
		t.Errorf("enforcement ring id = %d, want %d", enforcing.RingID, enforceRingBase+3)
	}
}

// The enforcement pass is a different geometry with its own fitted ranker. If
// the two ever collapsed into one, the block gate would be reading a score
// calibrated for a different candidate population.
func TestTheEnforcementPassIsItsOwnGeometryAndItsOwnModel(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	if s.enforcer.cfg.WindowHours != enforceWindowHours || s.enforcer.cfg.StrideHours != enforceStrideHours {
		t.Errorf("enforcement geometry is %dh/%dh, want %dh/%dh",
			s.enforcer.cfg.WindowHours, s.enforcer.cfg.StrideHours,
			enforceWindowHours, enforceStrideHours)
	}
	if s.enforcer.model == s.model {
		t.Error("the enforcement pass shares the analyst pass's fitted ranker")
	}
	if s.enforcer.limits.FrozenFor.Hours() != float64(enforceWindowHours) {
		t.Errorf("a freeze lasts %v, want the %dh window that justified it",
			s.enforcer.limits.FrozenFor, enforceWindowHours)
	}
	if s.enforcer.limits.Pass == s.limits.Pass {
		t.Errorf("both passes name themselves %q", s.limits.Pass)
	}

	_, _, ok := s.enforcer.at(0)
	if ok {
		t.Error("rank 0 resolved in the enforcement queue")
	}
	if _, _, ok := s.enforcer.at(len(s.enforcer.order) + 1); ok {
		t.Error("a rank past the end resolved in the enforcement queue")
	}
}

// The performance page used to carry two numbers in prose that the page's own
// tables contradicted: "nine features" beside twelve fitted coefficients, and
// "roughly 100×" beside a precision that is 140× the base rate. Both were true
// of the base feature set and went stale when the temporal set landed. They are
// derived now, and this pins that they stay derived.
func TestThePerformancePageQuotesItsOwnNumbers(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())
	b := body(t, s.get(t, "/metrics"))

	if want := strconv.Itoa(len(s.model.Weights)); !strings.Contains(b, "with "+want+"\n") &&
		!strings.Contains(b, "with "+want+" ") {
		t.Errorf("the page does not state its own feature count of %s", want)
	}

	// The lift is precision at the 50-alert budget over the ledger's own
	// laundering rate, and both halves have to come from this run.
	if s.baseRate <= 0 {
		t.Fatal("no base rate was measured")
	}
	want := fmt.Sprintf("%.0f×", s.liftAt(50))
	if !strings.Contains(b, want) {
		t.Errorf("the page does not quote the lift it measured (%s)", want)
	}
	if strings.Contains(b, "100×") && want != "100×" {
		t.Errorf("the page still carries the stale 100× figure; this run measured %s", want)
	}
	// The multiple and the rate it is a multiple of must describe the same
	// population. Quoting a lift over the raw ledger beside a rate measured after
	// the channel filter was the arithmetic error behind the stale figure.
	if !strings.Contains(b, fmt.Sprintf("%.2f%%", 100*s.baseRate)) {
		t.Errorf("the page does not state the base rate the lift is measured against (%.2f%%)",
			100*s.baseRate)
	}
}

// The console opens on a chosen case, and which case it is has to survive a
// restart: the demo runbook names it, and a page that opens on a different ring
// each time cannot be walked through. pickHero ranged over a map and then sorted
// by transfer count alone, so any tie at the top was broken by Go's map seed —
// the same defect as the candidate ordering in FINDINGS §11, in a different
// place. Ranging a map is only safe when the result is a total order.
func TestTheHeroCaseIsTheSameOneEveryRun(t *testing.T) {
	s := newTestServer(t, nil, offlineChain())

	first, txns := s.pickHero()
	if first == 0 {
		t.Fatal("no hero case was chosen")
	}
	for i := range 25 {
		got, gotTxns := s.pickHero()
		if got != first {
			t.Fatalf("pick %d chose ring %d, the first pick chose %d", i+2, got, first)
		}
		if len(gotTxns) != len(txns) {
			t.Errorf("pick %d returned %d transfers, the first returned %d",
				i+2, len(gotTxns), len(txns))
		}
	}
}
