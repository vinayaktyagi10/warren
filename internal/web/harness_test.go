package web

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/audit"
	"github.com/vinayaktyagi10/warren/internal/dbtest"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/enforce"
)

// The console's tests run the real pipeline over a ledger built here rather
// than loaded from Postgres. Everything downstream of the load — detection,
// the fit, the evaluation, the policy clamp, the enforcement limits — is the
// production code, so a test that passes says something about what the demo
// does. Only the two things a test cannot supply, the five-million-row read and
// the label table, sit on the other side of prepareFrom.

var synthEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// synthLedger builds a small ledger with labelled rings in it.
//
// The shape is chosen to be the one the detector is built for and not a
// caricature of it: rings are tight cycles of four accounts forwarding nearly
// everything they receive inside a few hours, and the background is ordinary
// traffic among accounts that happen to transact together. The rings are made
// of accounts that appear nowhere else, so a ring's candidate is the ring —
// which is what makes the amounts and account counts in these tests
// predictable, not a claim that real rings are that clean.
//
// Deterministic by construction: a fixed seed, and identifiers assigned in
// timestamp order.
func synthLedger() (*detect.Ledger, map[int32]string) {
	rng := rand.New(rand.NewSource(20260828))

	led := &detect.Ledger{}
	index := map[string]int32{}
	intern := func(name string) int32 {
		if id, ok := index[name]; ok {
			return id
		}
		id := int32(len(led.Accounts))
		index[name] = id
		led.Accounts = append(led.Accounts, name)
		return id
	}

	background := make([]int32, 0, 60)
	for i := range 60 {
		background = append(background, intern(fmt.Sprintf("0010|B%03d", i)))
	}

	var txns []detect.Txn
	add := func(ts time.Time, from, to int32, amount float64, pattern int32) {
		txns = append(txns, detect.Txn{
			TS: ts, From: from, To: to, Amount: amount,
			IsLaundering: pattern != 0, PatternID: pattern,
		})
	}

	const days = 14
	typologies := map[int32]string{}
	shapes := []string{"CYCLE", "FAN-OUT", "SCATTER-GATHER", "STACK"}
	ring := int32(0)

	for day := range days {
		dayStart := synthEpoch.Add(time.Duration(day) * 24 * time.Hour)

		// Ordinary traffic: small groups that transact together and keep most of
		// what they receive. These are the candidates the ranker has to reject.
		for range 30 {
			a := background[rng.Intn(len(background))]
			b := background[rng.Intn(len(background))]
			if a == b {
				continue
			}
			at := dayStart.Add(time.Duration(rng.Intn(24*60)) * time.Minute)
			add(at, a, b, 500+rng.Float64()*4000, 0)
		}

		// Two labelled rings a day: a four-account cycle, six transfers, inside
		// a few hours, forwarding nearly the whole sum onward each hop.
		for range 2 {
			ring++
			typologies[ring] = shapes[int(ring)%len(shapes)]

			accounts := make([]int32, 4)
			for i := range accounts {
				accounts[i] = intern(fmt.Sprintf("0020|R%03d-%d", ring, i))
			}
			amount := 120_000 + float64(ring%7)*20_000
			at := dayStart.Add(time.Duration(2+rng.Intn(10)) * time.Hour)
			for hop := range 6 {
				from := accounts[hop%4]
				to := accounts[(hop+1)%4]
				at = at.Add(time.Duration(20+rng.Intn(40)) * time.Minute)
				add(at, from, to, amount*(1-0.02*float64(hop)), ring)
			}
		}
	}

	sort.SliceStable(txns, func(i, j int) bool { return txns[i].TS.Before(txns[j].TS) })
	for i := range txns {
		txns[i].ID = int32(i + 1)
	}
	led.Txns = txns
	return led, typologies
}

// testServer is a prepared console over the synthetic ledger.
type testServer struct {
	*Server
	pool *pgxpool.Pool
	ts   *httptest.Server
}

// newTestServer prepares a console. A nil pool is allowed: the read-only views
// need no database, and the routes that write skip when one is not reachable.
func newTestServer(t *testing.T, pool *pgxpool.Pool, chain *agent.Chain) *testServer {
	t.Helper()

	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	cfg := detect.DefaultConfig()
	cfg.PaymentFormats = nil // the synthetic ledger is already the filtered one

	s := &Server{
		pool: pool, tmpl: tmpl, chain: chain, cfg: cfg,
		register: &enforce.Register{}, limits: enforce.DefaultLimits(),
		decided: make(map[int]agent.Assessment),
	}
	s.limits.Pass = fmt.Sprintf("%dh/%dh", cfg.WindowHours, cfg.StrideHours)

	if pool != nil {
		ctx := context.Background()
		if s.log, err = audit.New(ctx, pool); err != nil {
			t.Fatalf("audit schema: %v", err)
		}
		if s.holds, err = enforce.NewStore(ctx, pool); err != nil {
			t.Fatalf("restriction schema: %v", err)
		}
	}

	led, typologies := synthLedger()
	if err := s.prepareFrom(led, typologies, 0.6); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	srv := &testServer{Server: s, pool: pool}
	srv.ts = httptest.NewServer(s.Routes())
	t.Cleanup(srv.ts.Close)
	return srv
}

// newDBTestServer is newTestServer with a private schema behind it, skipping
// when no database is reachable.
func newDBTestServer(t *testing.T, chain *agent.Chain) *testServer {
	t.Helper()
	return newTestServer(t, dbtest.Pool(t), chain)
}

func (s *testServer) get(t *testing.T, path string) *http.Response {
	t.Helper()
	res, err := s.ts.Client().Get(s.ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func (s *testServer) post(t *testing.T, path, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build POST %s: %v", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := s.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// ---------------------------------------------------------------------------
// assessors
// ---------------------------------------------------------------------------

// The chain and the policy under test are the production ones. Only the tiers
// are supplied here, because a tier is the one part of the system whose answer
// comes from outside it: what has to be pinned down is what the policy does
// with an answer, not what a model would have said.

type fixedTier struct {
	name string
	prop agent.Proposal
}

func (f fixedTier) Name() string { return f.name }
func (f fixedTier) Assess(context.Context, agent.Evidence) (agent.Proposal, error) {
	return f.prop, nil
}

type failingTier struct {
	name string
	err  error
}

func (f failingTier) Name() string { return f.name }
func (f failingTier) Assess(context.Context, agent.Evidence) (agent.Proposal, error) {
	return agent.Proposal{}, f.err
}

// countingTier records how many times it was consulted, so a test can tell a
// tier that answered from one that was never reached.
type countingTier struct {
	fixedTier
	calls *int
}

func (c countingTier) Assess(ctx context.Context, ev agent.Evidence) (agent.Proposal, error) {
	*c.calls++
	return c.fixedTier.Assess(ctx, ev)
}

func proposing(action agent.Action, confidence float64) agent.Assessor {
	return fixedTier{
		name: "test-tier",
		prop: agent.Proposal{
			Action: action, Confidence: confidence,
			Rationale: "conservation is high and the group forwards what it receives",
		},
	}
}

// chainProposing is the production chain with one tier that always answers.
func chainProposing(action agent.Action, confidence float64) *agent.Chain {
	return agent.NewChain(agent.DefaultPolicy(), proposing(action, confidence))
}

// offlineChain is what `serve -offline` builds: no model tier at all.
func offlineChain() *agent.Chain { return agent.NewChain(agent.DefaultPolicy()) }
