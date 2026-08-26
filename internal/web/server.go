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
	"github.com/vinayaktyagi10/warren/internal/latency"
	"github.com/vinayaktyagi10/warren/internal/score"
)

//go:embed templates/*.html assets/*
var content embed.FS

// Server holds the prepared pipeline and serves views over it.
type Server struct {
	pool  *pgxpool.Pool
	tmpl  *template.Template
	chain *agent.Chain
	log   *audit.Log

	ledger     *detect.Ledger
	candidates []detect.Candidate // held-out only
	scores     []float64
	order      []int // candidate indices, most suspicious first
	txnByID    map[int32]*detect.Txn
	typologies map[int32]string

	model   *score.Model
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

	mu         sync.RWMutex
	decided    map[int]agent.Assessment
	preparedAt time.Time
}

// New builds the server, running the whole pipeline once.
func New(ctx context.Context, pool *pgxpool.Pool, cfg detect.Config,
	chain *agent.Chain, auditLog *audit.Log, trainFraction float64) (*Server, error) {

	funcs := template.FuncMap{
		"pct":   func(f float64) string { return fmt.Sprintf("%.1f%%", 100*f) },
		"pct3":  func(f float64) string { return fmt.Sprintf("%.2f%%", 100*f) },
		"money": money,
		"num":   humanInt,
		"f2":    func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"f3":    func(f float64) string { return fmt.Sprintf("%.3f", f) },
		"short": func(s string) string {
			if len(s) > 16 {
				return s[:16]
			}
			return s
		},
		"upper":       func(s string) string { return template.HTMLEscapeString(s) },
		"actionClass": actionClass,
		"dur":         latency.Short,
		"add":         func(a, b int) int { return a + b },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		pool: pool, tmpl: tmpl, chain: chain, log: auditLog, cfg: cfg,
		decided: make(map[int]agent.Assessment),
	}
	if err := s.prepare(ctx, trainFraction); err != nil {
		return nil, err
	}
	return s, nil
}

// prepare runs detection, ranking and evaluation.
func (s *Server) prepare(ctx context.Context, trainFraction float64) error {
	start := time.Now()

	led, err := detect.Load(ctx, s.pool, s.cfg)
	if err != nil {
		return fmt.Errorf("load ledger: %w", err)
	}
	led = detect.Trim(led, detect.ActivePeriod(led.Txns))
	s.ledger = led
	log.Printf("ledger: %d transfers", len(led.Txns))

	s.txnByID = make(map[int32]*detect.Txn, len(led.Txns))
	for i := range led.Txns {
		s.txnByID[led.Txns[i].ID] = &led.Txns[i]
	}

	s.typologies, err = loadTypologies(ctx, s.pool)
	if err != nil {
		return err
	}

	detectStart := time.Now()
	all, windows := detect.DetectTimed(led, s.cfg)
	detectWall := time.Since(detectStart)
	cut := detect.SplitTime(led, trainFraction)
	train, test := detect.Split(all, cut)
	s.model = score.Train(detect.Vectors(train), detect.Labels(led, train), score.DefaultTrainOpts())
	s.candidates = test

	s.scores = make([]float64, len(test))
	for i, v := range detect.Vectors(test) {
		s.scores[i] = s.model.Predict(v)
	}
	s.order = make([]int, len(test))
	for i := range s.order {
		s.order[i] = i
	}
	sort.Slice(s.order, func(a, b int) bool { return s.scores[s.order[a]] > s.scores[s.order[b]] })

	s.report = detect.Evaluate(led, all, s.typologies)
	s.ranked = detect.EvaluateRanked(led, test, s.scores,
		[]int{50, 100, 250, 500, 1000, 2500, len(test)})

	s.latency = detect.MeasureLatency(led, all, windows, s.cfg, detectWall, s.model.Predict)
	log.Printf("latency: score p50 %s, arrival to decision p50 %s",
		latency.Short(s.latency.Score.P50), latency.Short(s.latency.Decision.P50))

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

	writeJSON(w, map[string]any{
		"action":      string(a.Action),
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
	return agent.Evidence{
		RingID: rank, Typology: c.Typology,
		Accounts: c.Features.Accounts, Txns: c.Features.Txns,
		TotalAmount: c.Features.TotalAmount, MaxAmount: c.Features.MaxAmount,
		Conservation: c.Features.Conservation, PassThrough: c.Features.PassThroughRatio,
		SpanHours: c.Features.SpanHours, Score: sc, WindowStart: c.Window,
	}, true
}

// SeedAudit decides the first few alerts at startup so the trail is never empty.
//
// An audit page with nothing on it argues against the thing it exists to prove:
// the claim is that every decision leaves a record, and an empty table reads as
// no decisions being recorded rather than as none having been made yet. These
// are real decisions through the real chain, not fixtures — they cost a model
// call each and they can degrade, which is exactly what should be visible.
//
// It seeds only an empty log, so a demo that has already run is never
// overwritten, and it skips the ring the console opens on: assessing that one
// live, in front of an audience, is the point of the front page.
func (s *Server) SeedAudit(ctx context.Context, n int) {
	if n <= 0 {
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

	skip := 0
	if s.heroPattern != 0 {
		skip, _ = s.findCovering(s.heroTxns)
	}

	start := time.Now()
	seeded := 0
	for rank := 1; rank <= len(s.order) && seeded < n; rank++ {
		if rank == skip {
			continue
		}
		ev, ok := s.evidenceFor(rank)
		if !ok {
			continue
		}
		a := s.chain.Assess(ctx, ev)
		if _, _, err := s.log.Record(ctx, ev, a); err != nil {
			log.Printf("seed: record ring %d: %v", rank, err)
			return
		}
		s.mu.Lock()
		s.decided[rank] = a
		s.mu.Unlock()
		seeded++
	}
	log.Printf("seed: %d decisions recorded in %s", seeded, time.Since(start).Round(time.Millisecond))
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
		"Title":   "Measured performance",
		"Nav":     "metrics",
		"Shapes":  shapes,
		"Budgets": s.budgetRows(),
		"Report":  s.report,
		"Weights": s.modelWeights(),
		"Latency": s.latency,
	})
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

func (s *Server) modelWeights() []weightRow {
	rows := make([]weightRow, 0, len(score.RingFeatureNames))
	for i, n := range score.RingFeatureNames {
		w := s.model.Weights[i]
		rows = append(rows, weightRow{n, w, math.Abs(w)})
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
