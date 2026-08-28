// Package web serves the operator console: the ranked alert queue, the evidence
// behind each alert, the decision the system reached, and the audit trail.
//
// The pipeline runs once at startup and is held in memory. Detection over five
// million transfers takes the better part of a minute, and an operator paging
// through alerts should not pay that per click. Assessment is the exception —
// it calls a model, so it happens on demand and its result is cached per ring.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/audit"
	"github.com/vinayaktyagi10/warren/internal/baseline"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/enforce"
	"github.com/vinayaktyagi10/warren/internal/latency"
	"github.com/vinayaktyagi10/warren/internal/score"
)

//go:embed templates/*.html assets/*
var content embed.FS

// The enforcement geometry. Narrow windows decide sooner, and deciding sooner
// is the whole of what enforcement can buy: docs/FINDINGS.md §13 measures the
// ceiling on interception at 10.76% of ring value at 72h windows against 31.75%
// at 24h. Window width is decision latency is the enforcement ceiling.
const (
	enforceWindowHours = 24
	enforceStrideHours = 6
)

// Server holds the prepared pipeline and serves views over it.
type Server struct {
	pool  *pgxpool.Pool
	tmpl  *template.Template
	chain *agent.Chain
	log   *audit.Log

	// holds is the persisted restriction ledger and register is the same state
	// in memory. Both exist because the console answers two different questions:
	// "is this account held right now" is a map lookup, and "what did we do to
	// this account and on whose authority" is a chain walk.
	holds    *enforce.Store
	register *enforce.Register
	limits   enforce.Limits

	// enforcer is the second detection geometry. The console runs two passes
	// for the reason docs/FINDINGS.md §14 measured: a wide window is the better
	// analyst queue and a narrow one is the better enforcement trigger, and
	// making one geometry do both means taking the worse half of each. Only
	// this one leases.
	enforcer *enforcePass

	ledger     *detect.Ledger
	candidates []detect.Candidate // held-out only
	scores     []float64
	order      []int // candidate indices, most suspicious first
	txnByID    map[int32]*detect.Txn
	typologies map[int32]string

	model   *detect.Ranker
	report  *detect.Report
	ranked  *detect.RankedReport
	latency latency.Report
	cfg     detect.Config

	// The per-transaction scorer WARREN is measured against, plus where each
	// held-out transfer ranked under it.
	baseline      *baseline.Model
	txnPercentile map[int32]float64
	comparison    baseline.Comparison
	heroPattern   int32
	heroTxns      []*detect.Txn

	// baseRate is the share of the ledger labelled laundering, held so the
	// performance page can state the lift it achieves over it rather than
	// carrying a number in prose that goes stale the next time the feature set
	// changes. It already did once: the page claimed 100x, which belonged to the
	// base feature set, while the table beside it showed the temporal set's
	// 14.32%.
	baseRate float64

	mu         sync.RWMutex
	decided    map[int]agent.Assessment
	preparedAt time.Time
}

// enforcePass is a detection geometry with its own fitted ranker, held apart
// from the analyst queue because it answers a different question: not "what
// should a person look at today" but "what is safe to act on without one".
type enforcePass struct {
	cfg        detect.Config
	model      *detect.Ranker
	candidates []detect.Candidate
	scores     []float64
	order      []int
	limits     enforce.Limits
}

// parseTemplates builds the console's template set. It is a function rather
// than inline in New so that a test can render the same pages the operator
// sees, through the same helpers, without going near the pipeline.
func parseTemplates() (*template.Template, error) {
	funcs := template.FuncMap{
		"pct":         func(f float64) string { return fmt.Sprintf("%.1f%%", 100*f) },
		"pct3":        func(f float64) string { return fmt.Sprintf("%.2f%%", 100*f) },
		"money":       money,
		"num":         humanInt,
		"f2":          func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"f3":          func(f float64) string { return fmt.Sprintf("%.3f", f) },
		"upper":       func(s string) string { return template.HTMLEscapeString(s) },
		"actionClass": actionClass,
		"dur":         latency.Short,
		"add":         func(a, b int) int { return a + b },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return tmpl, nil
}

// New builds the server, running the whole pipeline once.
func New(ctx context.Context, pool *pgxpool.Pool, cfg detect.Config,
	chain *agent.Chain, auditLog *audit.Log, holds *enforce.Store,
	trainFraction float64) (*Server, error) {

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}

	s := &Server{
		pool: pool, tmpl: tmpl, chain: chain, log: auditLog, cfg: cfg,
		holds: holds, register: &enforce.Register{}, limits: enforce.DefaultLimits(),
		decided: make(map[int]agent.Assessment),
	}
	s.limits.Pass = fmt.Sprintf("%dh/%dh", cfg.WindowHours, cfg.StrideHours)
	if err := s.prepare(ctx, trainFraction); err != nil {
		return nil, err
	}
	return s, nil
}

// prepare reads the ledger and the ring labels, then prepares from them.
//
// The read is split from the preparation because they fail for different
// reasons and are exercised differently: loading is a query against five
// million rows, and everything after it is arithmetic over whatever ledger it
// returned. prepareFrom is what the console's tests drive, over a small ledger
// built in memory, so the handlers under test run on the real detector, the
// real ranker and the real evaluation rather than on stand-ins for them.
func (s *Server) prepare(ctx context.Context, trainFraction float64) error {
	start := time.Now()
	led, err := detect.Load(ctx, s.pool, s.cfg)
	if err != nil {
		return fmt.Errorf("load ledger: %w", err)
	}
	led = detect.Trim(led, detect.ActivePeriod(led.Txns))
	// Logged separately from the preparation below, because the read is most of
	// the wall clock and the two numbers used to be reported as one.
	log.Printf("loaded ledger in %s", time.Since(start).Round(time.Millisecond))

	typologies, err := loadTypologies(ctx, s.pool)
	if err != nil {
		return err
	}
	return s.prepareFrom(led, typologies, trainFraction)
}

// prepareFrom runs detection, ranking and evaluation over a loaded ledger.
func (s *Server) prepareFrom(led *detect.Ledger, typologies map[int32]string,
	trainFraction float64) error {

	start := time.Now()

	s.ledger = led
	log.Printf("ledger: %d transfers", len(led.Txns))

	s.txnByID = make(map[int32]*detect.Txn, len(led.Txns))
	for i := range led.Txns {
		s.txnByID[led.Txns[i].ID] = &led.Txns[i]
	}
	s.typologies = typologies

	laundering := 0
	for i := range led.Txns {
		if led.Txns[i].IsLaundering {
			laundering++
		}
	}
	if len(led.Txns) > 0 {
		s.baseRate = float64(laundering) / float64(len(led.Txns))
	}

	detectStart := time.Now()
	all, windows := detect.DetectTimed(led, s.cfg)
	detectWall := time.Since(detectStart)
	cut := detect.SplitTime(led, trainFraction)
	train, test := detect.Split(all, cut)
	s.model = detect.TrainRanker(train, detect.Labels(led, train),
		detect.DefaultFeatureSet, score.DefaultTrainOpts())
	s.candidates = test
	s.scores = s.model.ScoreAll(test)
	s.order = detect.RankOrder(s.scores)

	s.report = detect.Evaluate(led, all, s.typologies)
	s.ranked = detect.EvaluateRanked(led, test, s.scores,
		[]int{50, 100, 250, 500, 1000, 2500, len(test)})

	s.latency = detect.MeasureLatency(led, all, windows, s.cfg, detectWall, s.model)
	log.Printf("latency: score p50 %s, arrival to decision p50 %s",
		latency.Short(s.latency.Score.P50), latency.Short(s.latency.Decision.P50))

	if err := s.prepareEnforcer(led, cut); err != nil {
		return err
	}

	s.prepareBaseline(led, cut)
	s.heroPattern, s.heroTxns = s.pickHero()
	if s.heroPattern == 0 {
		log.Printf("no ring met the hero criteria; the console opens on the queue")
	} else {
		log.Printf("hero case: ring %d, %d transfers", s.heroPattern, len(s.heroTxns))
	}

	s.preparedAt = time.Now()
	log.Printf("prepared %d candidates in %s", len(all), time.Since(start).Round(time.Millisecond))
	return nil
}

// prepareEnforcer runs the narrow geometry and fits its own ranker.
//
// A separate ranker rather than the analyst pass's, because the two see
// different candidate populations: a 24-hour window produces smaller, tighter
// groups than a 72-hour one, and a model fitted on one is calibrated for the
// other's distribution. Sharing it would put the enforcing pass's score — the
// number the block gate reads — on a scale nothing measured.
func (s *Server) prepareEnforcer(led *detect.Ledger, cut time.Time) error {
	cfg := s.cfg
	cfg.WindowHours, cfg.StrideHours = enforceWindowHours, enforceStrideHours

	start := time.Now()
	all := detect.Detect(led, cfg)
	train, test := detect.Split(all, cut)
	if len(train) == 0 || len(test) == 0 {
		return fmt.Errorf("enforcement pass: %d fitting and %d held-out candidates", len(train), len(test))
	}

	e := &enforcePass{cfg: cfg, candidates: test}
	e.model = detect.TrainRanker(train, detect.Labels(led, train),
		detect.DefaultFeatureSet, score.DefaultTrainOpts())
	e.scores = e.model.ScoreAll(test)
	e.order = detect.RankOrder(e.scores)

	e.limits = enforce.DefaultLimits()
	e.limits.Pass = fmt.Sprintf("%dh/%dh", cfg.WindowHours, cfg.StrideHours)
	// The lease lasts as long as the window that justified it: a pass that acted
	// on 24 hours of evidence buys a 24-hour hold, no longer.
	e.limits.FrozenFor = time.Duration(cfg.WindowHours) * time.Hour

	s.enforcer = e
	log.Printf("enforcement pass %s: %d candidates, %d held out, fitted in %s",
		e.limits.Pass, len(all), len(test), time.Since(start).Round(time.Millisecond))
	return nil
}

// ringInPass resolves a rank within the enforcement queue.
func (e *enforcePass) at(rank int) (detect.Candidate, float64, bool) {
	if rank < 1 || rank > len(e.order) {
		return detect.Candidate{}, 0, false
	}
	idx := e.order[rank-1]
	return e.candidates[idx], e.scores[idx], true
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.FileServerFS(content))
	mux.HandleFunc("GET /{$}", s.handleHero)
	mux.HandleFunc("GET /queue", s.handleDashboard)
	mux.HandleFunc("GET /ring/{rank}", s.handleRing)
	mux.HandleFunc("POST /api/assess/{rank}", s.handleAssess)
	mux.HandleFunc("GET /audit", s.handleAudit)
	mux.HandleFunc("POST /api/audit/verify", s.handleVerify)
	mux.HandleFunc("POST /api/audit/tamper/{seq}", s.handleTamper)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /holds", s.handleHolds)
	mux.HandleFunc("POST /api/enforce/assess/{rank}", s.handleEnforceAssess)
	mux.HandleFunc("POST /api/holds/lift", s.handleLift)
	mux.HandleFunc("POST /api/holds/verify", s.handleVerifyHolds)
	return mux
}

// ---------------------------------------------------------------------------
// view models
// ---------------------------------------------------------------------------

type ringView struct {
	Rank         int
	ShowLabels   bool
	Score        float64
	Typology     string
	Accounts     int
	Txns         int
	Total        float64
	Max          float64
	Conservation float64
	PassThrough  float64
	SpanHours    float64
	Window       time.Time

	// Truth is shown in the console because this is a demonstration against a
	// labelled dataset. It is never given to the assessor.
	Laundering int
	IsRing     bool

	Assessment *agent.Assessment
	Nodes      []nodeView
	Edges      []edgeView
}

type nodeView struct {
	ID    string
	Label string
	X, Y  float64
	In    float64
	Out   float64
	Kind  string // source, sink, intermediary
	Hub   bool   // drawn larger at the centre of a fan
}

type edgeView struct {
	X1, Y1, X2, Y2 float64
	Amount         float64
	Laundering     bool
	MidX, MidY     float64

	// SelfLoop marks a transfer from an account back to itself. Drawn as a
	// straight line it has zero length and vanishes, which reads as missing data
	// rather than as the round trip it is.
	SelfLoop bool
	LoopX    float64
	LoopY    float64
}

func (s *Server) ringAt(rank int) (detect.Candidate, float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rank < 1 || rank > len(s.order) {
		return detect.Candidate{}, 0, false
	}
	idx := s.order[rank-1]
	return s.candidates[idx], s.scores[idx], true
}

func (s *Server) buildRingView(rank int, withGraph bool) (*ringView, bool) {
	c, sc, ok := s.ringAt(rank)
	if !ok {
		return nil, false
	}

	v := &ringView{
		Rank: rank, Score: sc, Typology: c.Typology,
		Accounts: c.Features.Accounts, Txns: c.Features.Txns,
		Total: c.Features.TotalAmount, Max: c.Features.MaxAmount,
		Conservation: c.Features.Conservation, PassThrough: c.Features.PassThroughRatio,
		SpanHours: c.Features.SpanHours, Window: c.Window,
	}
	for _, id := range c.TxnIDs {
		if t := s.txnByID[id]; t != nil && t.IsLaundering {
			v.Laundering++
		}
	}
	v.IsRing = v.Laundering > 0

	s.mu.RLock()
	if a, ok := s.decided[rank]; ok {
		v.Assessment = &a
	}
	s.mu.RUnlock()

	if withGraph {
		v.Nodes, v.Edges = s.layout(c)
		// Past roughly two dozen nodes the labels collide into an unreadable
		// band around the circle and actively obscure the edges, which are the
		// part that carries the meaning.
		v.ShowLabels = len(v.Nodes) <= 24
	}
	return v, true
}

// layout arranges the ring's accounts on a circle and draws the transfers
// between them. A circle is used rather than a force-directed layout because it
// is deterministic: the same ring looks the same every time it is opened, which
// matters when someone is comparing what they saw before with what they see now.
func (s *Server) layout(c detect.Candidate) ([]nodeView, []edgeView) {
	const cx, cy, r = 400.0, 300.0, 230.0

	inflow := map[int32]float64{}
	outflow := map[int32]float64{}
	present := map[int32]bool{}
	var txns []*detect.Txn
	for _, id := range c.TxnIDs {
		t := s.txnByID[id]
		if t == nil {
			continue
		}
		txns = append(txns, t)
		outflow[t.From] += t.Amount
		inflow[t.To] += t.Amount
		present[t.From] = true
		present[t.To] = true
	}

	accounts := make([]int32, 0, len(present))
	for a := range present {
		accounts = append(accounts, a)
	}

	// A fan has a hub, and a hub drawn on the rim of a circle hides the very
	// shape that makes it a fan. When one account touches most of the transfers,
	// it goes in the middle and everything else arranges around it, which is how
	// the structure is recognised by eye.
	var hub int32 = -1
	if len(txns) >= 4 {
		touches := make(map[int32]int, len(present))
		for _, t := range txns {
			touches[t.From]++
			if t.To != t.From {
				touches[t.To]++
			}
		}
		best, bestN := int32(-1), 0
		for a, n := range touches {
			if n > bestN {
				best, bestN = a, n
			}
		}
		if bestN*2 >= len(txns) {
			hub = best
		}
	}
	// Order by net flow so sources and sinks sit apart on the circle and the
	// direction of movement is legible at a glance.
	sort.Slice(accounts, func(i, j int) bool {
		ni := outflow[accounts[i]] - inflow[accounts[i]]
		nj := outflow[accounts[j]] - inflow[accounts[j]]
		if ni != nj {
			return ni > nj
		}
		return accounts[i] < accounts[j]
	})

	// Place the rim accounts, keeping the hub aside for the centre.
	rim := accounts
	if hub >= 0 {
		rim = rim[:0]
		for _, a := range accounts {
			if a != hub {
				rim = append(rim, a)
			}
		}
	}

	pos := make(map[int32][2]float64, len(accounts))
	for i, a := range rim {
		angle := 2 * math.Pi * float64(i) / float64(len(rim))
		pos[a] = [2]float64{
			cx + r*math.Cos(angle-math.Pi/2),
			cy + r*math.Sin(angle-math.Pi/2),
		}
	}
	if hub >= 0 {
		pos[hub] = [2]float64{cx, cy}
	}

	nodes := make([]nodeView, 0, len(accounts))
	for _, a := range accounts {
		x, y := pos[a][0], pos[a][1]

		kind := "intermediary"
		switch {
		case inflow[a] == 0:
			kind = "source"
		case outflow[a] == 0:
			kind = "sink"
		}
		label := s.ledger.Accounts[a]
		if len(label) > 9 {
			label = label[len(label)-9:]
		}
		nodes = append(nodes, nodeView{
			ID: fmt.Sprint(a), Label: label, X: x, Y: y,
			In: inflow[a], Out: outflow[a], Kind: kind, Hub: a == hub,
		})
	}

	edges := make([]edgeView, 0, len(txns))
	for _, t := range txns {
		p1, p2 := pos[t.From], pos[t.To]
		e := edgeView{
			X1: p1[0], Y1: p1[1], X2: p2[0], Y2: p2[1],
			MidX: (p1[0] + p2[0]) / 2, MidY: (p1[1] + p2[1]) / 2,
			Amount: t.Amount, Laundering: t.IsLaundering,
		}
		if t.From == t.To {
			// Park a small circle just outside the node, away from the centre.
			e.SelfLoop = true
			dx, dy := p1[0]-cx, p1[1]-cy
			norm := math.Hypot(dx, dy)
			if norm == 0 {
				norm = 1
			}
			e.LoopX = p1[0] + 15*dx/norm
			e.LoopY = p1[1] + 15*dy/norm
		}
		edges = append(edges, e)
	}
	return nodes, edges
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	// 50 rows, because 50 is the alert budget the strip above headlines and the
	// one the measured precision figure is quoted at. A queue showing a different
	// number of alerts than the number being claimed invites the obvious question.
	limit := 50
	rings := make([]*ringView, 0, limit)
	for rank := 1; rank <= limit && rank <= len(s.order); rank++ {
		if v, ok := s.buildRingView(rank, false); ok {
			rings = append(rings, v)
		}
	}

	s.mu.RLock()
	decided := len(s.decided)
	s.mu.RUnlock()

	// The 50-alert row is the headline: it is the budget a real team could work.
	var headline detect.RankedRow
	for _, row := range s.ranked.Rows {
		if row.TopK == 50 {
			headline = row
			break
		}
	}

	s.render(w, "dashboard.html", map[string]any{
		"Title":    "Alert queue",
		"Nav":      "queue",
		"Rings":    rings,
		"Total":    len(s.order),
		"Ledger":   len(s.ledger.Txns),
		"Decided":  decided,
		"Headline": headline,
		"Budgets":  s.budgetPoints(50, 250, 1000),
		"Report":   s.report,
		"Chain":    tierNames(s.chain),
	})
}

func (s *Server) handleRing(w http.ResponseWriter, r *http.Request) {
	rank, err := strconv.Atoi(r.PathValue("rank"))
	if err != nil {
		http.Error(w, "bad rank", http.StatusBadRequest)
		return
	}
	v, ok := s.buildRingView(rank, true)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "ring.html", map[string]any{
		"Title":  fmt.Sprintf("Ring #%d", rank),
		"Nav":    "queue",
		"Ring":   v,
		"Policy": s.chain.Policy,
		"Chain":  tierNames(s.chain),
		"Prev":   rank - 1,
		"Next":   rank + 1,
		"Last":   len(s.order),
	})
}

// handleAssess runs the chain for one ring and records the decision. This is
// the only path that calls a model, and the only one that writes.
func (s *Server) handleAssess(w http.ResponseWriter, r *http.Request) {
	rank, err := strconv.Atoi(r.PathValue("rank"))
	if err != nil {
		http.Error(w, "bad rank", http.StatusBadRequest)
		return
	}
	ev, ok := s.evidenceFor(rank)
	if !ok {
		http.NotFound(w, r)
		return
	}

	start := time.Now()
	a := s.chain.Assess(r.Context(), ev)
	seq, hash, err := s.log.Record(r.Context(), ev, a)
	if err != nil {
		http.Error(w, "record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.decided[rank] = a
	s.mu.Unlock()

	c, _, _ := s.ringAt(rank)
	held, bounds := s.enforceDecision(r.Context(), c, a, seq, s.limits, false)

	writeJSON(w, map[string]any{
		"action":      string(a.Action),
		"held":        held,
		"heldBounds":  bounds,
		"proposed":    string(a.Proposed),
		"adjusted":    a.Adjusted(),
		"confidence":  a.Confidence,
		"rationale":   a.Rationale,
		"source":      a.Source,
		"adjustments": a.Adjustments,
		"actionClass": actionClass(string(a.Action)),
		"auditSeq":    seq,
		"auditHash":   hash,
		"took":        time.Since(start).Round(time.Millisecond).String(),
	})
}

// evidenceFor assembles the bundle the assessor is given for one ranked group.
//
// Only measured quantities go in. There is deliberately no name, memo or
// reference field: if free text from the ledger reached the model, a party under
// investigation would have a channel to address the system judging them.
func (s *Server) evidenceFor(rank int) (agent.Evidence, bool) {
	c, sc, ok := s.ringAt(rank)
	if !ok {
		return agent.Evidence{}, false
	}
	return s.evidenceOf(rank, c, sc), true
}

// enforceRingBase keeps the two passes' ring identifiers apart in the audit log.
//
// Both passes rank from 1, so without an offset "ring 3" in the log would mean
// two different groups of accounts depending on which pass wrote it — and the
// holds page joins on exactly that number. An ambiguous identifier in a record
// whose entire purpose is to be unambiguous later is not a cosmetic problem.
const enforceRingBase = 100000

// evidenceOf renders what the assessor is told about one candidate.
//
// Every field is a measured quantity from the detector. There is deliberately
// no name, reference or memo, so a party under investigation has no channel to
// address the model judging them.
func (s *Server) evidenceOf(ringID int, c detect.Candidate, sc float64) agent.Evidence {
	return agent.Evidence{
		RingID: ringID, Typology: c.Typology,
		Accounts: c.Features.Accounts, Txns: c.Features.Txns,
		TotalAmount: c.Features.TotalAmount, MaxAmount: c.Features.MaxAmount,
		Conservation: c.Features.Conservation, PassThrough: c.Features.PassThroughRatio,
		SpanHours: c.Features.SpanHours, Score: sc, WindowStart: c.Window,
	}
}

// SeedAudit decides the first few alerts at startup so the trail is never empty.
//
// It seeds from both passes, and the split is the architecture rather than a
// convenience. The analyst pass produces decisions and no holds — that is what
// the wide geometry is for, and a console where every recorded decision also
// seized money would misrepresent the system. The enforcement pass produces the
// holds. Someone reading the audit page and the holds page side by side should
// be able to see that they are not the same list, and why.
func (s *Server) SeedAudit(ctx context.Context, analyst, enforcing int) {
	if analyst <= 0 && enforcing <= 0 {
		return
	}
	have, err := s.log.Count(ctx)
	if err != nil {
		log.Printf("seed: cannot read audit log: %v", err)
		return
	}
	if have > 0 {
		log.Printf("seed: audit log already holds %d decisions, leaving it alone", have)
		return
	}

	// The hero case is left undecided so the live assessment in the demo has
	// something to decide.
	skip := 0
	if s.heroPattern != 0 {
		skip, _ = s.findCovering(s.heroTxns)
	}

	start := time.Now()
	seeded, held := 0, 0

	for rank := 1; rank <= len(s.order) && seeded < analyst; rank++ {
		if rank == skip {
			continue
		}
		ev, ok := s.evidenceFor(rank)
		if !ok {
			continue
		}
		a := s.chain.Assess(ctx, ev)
		seq, _, err := s.log.Record(ctx, ev, a)
		if err != nil {
			log.Printf("seed: record analyst ring %d: %v", rank, err)
			return
		}
		s.mu.Lock()
		s.decided[rank] = a
		s.mu.Unlock()

		c, _, _ := s.ringAt(rank)
		s.enforceDecision(ctx, c, a, seq, s.limits, false)
		seeded++
	}

	for rank := 1; rank <= len(s.enforcer.order) && rank <= enforcing; rank++ {
		c, sc, ok := s.enforcer.at(rank)
		if !ok {
			continue
		}
		ev := s.evidenceOf(enforceRingBase+rank, c, sc)
		a := s.chain.Assess(ctx, ev)
		seq, _, err := s.log.Record(ctx, ev, a)
		if err != nil {
			log.Printf("seed: record enforcement ring %d: %v", rank, err)
			return
		}
		// A seeded decision enforces exactly as a live one does. Recording the
		// decision but skipping its consequence would make the holds page a
		// different kind of thing from the audit page — a display rather than a
		// record of what the system actually did.
		n, _ := s.enforceDecision(ctx, c, a, seq, s.enforcer.limits, true)
		if n > 0 {
			log.Printf("seed: enforcement rank %d held %d accounts on decision %d", rank, n, seq)
		}
		held += n
		seeded++
	}

	log.Printf("seed: %d decisions recorded (%d accounts held) in %s",
		seeded, held, time.Since(start).Round(time.Millisecond))
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.auditEntries(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "audit.html", map[string]any{
		"Title":   "Audit trail",
		"Nav":     "audit",
		"Entries": entries,
	})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	res, err := s.log.Verify(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"valid":   res.Valid,
		"entries": res.Entries,
		"seq":     res.BrokenSeq,
		"reason":  res.BrokenReason,
	})
}

// handleTamper exists to be used in front of an audience: it edits a recorded
// decision in place without touching a hash, which is what someone covering
// their tracks would do, so that verification can be seen catching it.
func (s *Server) handleTamper(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.Error(w, "bad seq", http.StatusBadRequest)
		return
	}
	tag, err := s.pool.Exec(r.Context(), `
		UPDATE audit_log
		SET action = 'allow',
		    rationale = 'Reviewed and cleared. No further action required.'
		WHERE seq = $1`, seq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"tampered": tag.RowsAffected() > 0, "seq": seq})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	type shapeRow struct {
		Typology string
		Found    int
		Labelled int
		Rate     float64
	}
	var shapes []shapeRow
	for _, st := range s.report.PerTypology {
		rate := 0.0
		if st.Labelled > 0 {
			rate = float64(st.Found) / float64(st.Labelled)
		}
		shapes = append(shapes, shapeRow{st.Typology, st.Found, st.Labelled, rate})
	}
	sort.Slice(shapes, func(i, j int) bool { return shapes[i].Rate > shapes[j].Rate })

	s.render(w, "metrics.html", map[string]any{
		"Title":    "Measured performance",
		"Nav":      "metrics",
		"Shapes":   shapes,
		"Budgets":  s.budgetRows(),
		"Report":   s.report,
		"Weights":  s.modelWeights(),
		"Latency":  s.latency,
		"BaseRate": s.baseRate,
		"Lift":     s.liftAt(50),
	})
}

// liftAt reports how many times the precision at a given alert budget exceeds
// the ledger's own laundering rate. Stating it as a computed ratio rather than
// prose is the point: the multiple is the headline claim, and it moves whenever
// the feature set does.
func (s *Server) liftAt(budget int) float64 {
	if s.baseRate <= 0 {
		return 0
	}
	for _, row := range s.ranked.Rows {
		if row.TopK == budget {
			return row.PrecisionAt / s.baseRate
		}
	}
	return 0
}

// budgetRow is one point on the alert-budget curve: what a team gets, and what
// it costs them, if they agree to work exactly this many alerts.
type budgetRow struct {
	detect.RankedRow
	Precision float64
	Recall    float64
}

func (s *Server) budgetRows() []budgetRow {
	rows := make([]budgetRow, 0, len(s.ranked.Rows))
	for _, row := range s.ranked.Rows {
		p, rc := 0.0, 0.0
		if row.TxnTP+row.TxnFP > 0 {
			p = float64(row.TxnTP) / float64(row.TxnTP+row.TxnFP)
		}
		if row.TxnTP+row.TxnFN > 0 {
			rc = float64(row.TxnTP) / float64(row.TxnTP+row.TxnFN)
		}
		rows = append(rows, budgetRow{row, p, rc})
	}
	return rows
}

// budgetPoints picks a few budgets to show inline, in the order asked for.
func (s *Server) budgetPoints(want ...int) []budgetRow {
	byK := make(map[int]budgetRow, len(s.ranked.Rows))
	for _, r := range s.budgetRows() {
		byK[r.TopK] = r
	}
	out := make([]budgetRow, 0, len(want))
	for _, k := range want {
		if r, ok := byK[k]; ok {
			out = append(out, r)
		}
	}
	return out
}

type weightRow struct {
	Name   string
	Weight float64
	Abs    float64
}

// modelWeights reads the names off the fitted model rather than a list held
// here, so a feature added to the vector appears in the table by itself.
func (s *Server) modelWeights() []weightRow {
	rows := make([]weightRow, 0, len(s.model.Weights))
	for i, w := range s.model.Weights {
		name := fmt.Sprintf("feature_%d", i)
		if i < len(s.model.Names) {
			name = s.model.Names[i]
		}
		rows = append(rows, weightRow{name, w, math.Abs(w)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Abs > rows[j].Abs })
	return rows
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type auditEntry struct {
	Seq         int64
	DecidedAt   time.Time
	RingID      int
	Action      string
	Proposed    string
	Adjusted    bool
	Confidence  float64
	Source      string
	Rationale   string
	Adjustments []string
	Hash        string
	ActionClass string
}

func (s *Server) auditEntries(ctx context.Context) ([]auditEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, decided_at, ring_id, action, proposed, confidence,
		       source, rationale, adjustments, hash
		FROM audit_log ORDER BY seq DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []auditEntry
	for rows.Next() {
		var e auditEntry
		if err := rows.Scan(&e.Seq, &e.DecidedAt, &e.RingID, &e.Action, &e.Proposed,
			&e.Confidence, &e.Source, &e.Rationale, &e.Adjustments, &e.Hash); err != nil {
			return nil, err
		}
		e.Adjusted = e.Action != e.Proposed
		e.ActionClass = actionClass(e.Action)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func actionClass(action string) string {
	switch action {
	case "allow":
		return "allow"
	case "block":
		return "block"
	default:
		return "hold"
	}
}

func tierNames(c *agent.Chain) []string {
	names := make([]string, 0, len(c.Tiers))
	for _, t := range c.Tiers {
		names = append(names, t.Name())
	}
	return names
}

func loadTypologies(ctx context.Context, pool *pgxpool.Pool) (map[int32]string, error) {
	rows, err := pool.Query(ctx, `SELECT pattern_id, typology FROM aml_patterns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int32]string)
	for rows.Next() {
		var id int32
		var ty string
		if err := rows.Scan(&id, &ty); err != nil {
			return nil, err
		}
		out[id] = ty
	}
	return out, rows.Err()
}

// money renders large sums readably. A ring moving 862,544,926.85 is easier to
// judge as 862.5M than as a wall of digits.
func money(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fbn", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func humanInt(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
