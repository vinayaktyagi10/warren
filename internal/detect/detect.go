// Package detect finds candidate laundering rings in the transfer graph.
//
// The approach is deliberately not "score every transaction and keep the top
// ones". A laundering ring is a shape, and its individual transfers are
// unremarkable in isolation — that is the whole point of structuring money this
// way. So the detector looks for the shape:
//
//	filter    reduce the ledger to plausible laundering channels
//	window    cut time into overlapping slices
//	connect   find groups of accounts transacting together inside a slice
//	classify  name the shape the group forms
//
// Windowing is load-bearing rather than an optimisation. Some accounts appear in
// dozens of distinct rings — one sits in 32 — and over the full ledger those
// hubs chain unrelated rings into a single blob, the same failure that sank the
// first attempt on IEEE-CIS. Rings complete in 21 to 129 hours, so a window
// measured in days separates rings that merely share an account months apart.
package detect

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/graph"
	"github.com/vinayaktyagi10/warren/internal/latency"
)

// Config controls the detector. Defaults come from measurements on the training
// ledger, recorded in docs/FINDINGS.md, not from intuition.
type Config struct {
	// PaymentFormats restricts the candidate set. Laundering here is 86.6% ACH
	// against 11.75% of ordinary traffic, and never uses Wire or Reinvestment,
	// so this single filter removes 88% of the ledger while keeping 86.6% of
	// the laundering. It is a property of this generator, and a detector for a
	// different institution would have to re-derive it.
	PaymentFormats []string

	// MinAmount drops small transfers. Laundering runs at a 8,667 median
	// against 1,411 for ordinary traffic.
	MinAmount float64

	WindowHours int // width of each time slice
	StrideHours int // how far the window advances; < WindowHours overlaps

	// MaxAccountDegree stops a single account fusing separate rings. Real rings
	// reach 32 accounts, so the cap has to sit well above that to avoid
	// destroying the fan shapes it is meant to preserve.
	MaxAccountDegree int

	MinTxns     int // ignore groups too small to be a ring
	MinAccounts int

	// Upper bounds. The largest labelled ring holds 64 transfers across 45
	// accounts, so a group far past that is not one ring the detector found but
	// several unrelated clusters it failed to keep apart. Flagging it would bury
	// whatever real ring is inside under hundreds of innocent transfers, which
	// is precisely how an alert becomes unusable. The caps sit above the
	// observed maxima so a genuine ring is never discarded for being large.
	MaxTxns     int
	MaxAccounts int
}

func DefaultConfig() Config {
	return Config{
		PaymentFormats:   []string{"ACH"},
		MinAmount:        0,
		WindowHours:      72,
		StrideHours:      24,
		MaxAccountDegree: 60,
		MinTxns:          3,
		MinAccounts:      3,
		MaxTxns:          100,
		MaxAccounts:      60,
	}
}

// Txn is one transfer, with accounts mapped to dense indices for graph work.
type Txn struct {
	ID           int32
	TS           time.Time
	From, To     int32
	Amount       float64
	IsLaundering bool
	PatternID    int32 // 0 when the transfer belongs to no labelled ring
}

// Candidate is a group of accounts the detector believes is acting as one ring.
type Candidate struct {
	TxnIDs    []int32
	Accounts  []int32
	Typology  string
	Senders   int
	Receivers int
	Window    time.Time
	Features  Features
}

// Features describe a group's shape and money movement. Connectivity alone
// cannot separate a ring from ordinary business: most account clusters are
// legitimate, so the discrimination has to come from how the money behaves
// inside the cluster, not from the fact that a cluster exists.
type Features struct {
	Txns     int
	Accounts int

	TotalAmount float64
	MaxAmount   float64
	MeanAmount  float64

	SpanHours float64

	// PassThroughRatio is the share of accounts that both receive and send.
	// Layering moves value along a chain, so laundering groups are rich in
	// intermediaries; a merchant taking payments is not.
	PassThroughRatio float64

	// Conservation measures how closely value entering an intermediary leaves
	// it again. A mule forwards almost everything it receives, so the ratio sits
	// near 1; ordinary accounts keep or top up their balance.
	Conservation float64

	// Density is transfers per account. Tight, repeatedly-transacting groups
	// score above the sparse pairs that dominate ordinary traffic.
	Density float64

	// The temporal features, described in temporal.go. They are computed
	// always and used only when the selected feature set includes them, so the
	// with-and-without comparison costs nothing to run.
	Burstiness   float64
	MaxHourShare float64
	FastForward  float64
}

// Ledger is the working set: filtered transfers plus the account index.
type Ledger struct {
	Txns     []Txn
	Accounts []string // dense index -> "bank|account"
}

// Load reads the candidate transfers into memory. The filtered ACH set is
// ~600k rows, which is small enough to hold and repeatedly window over without
// paying a database round trip per slice.
func Load(ctx context.Context, pool *pgxpool.Pool, cfg Config) (*Ledger, error) {
	formats := cfg.PaymentFormats
	if len(formats) == 0 {
		formats = nil // nil disables the filter in the query below
	}

	rows, err := pool.Query(ctx, `
		SELECT txn_id, ts,
		       from_bank || '|' || from_account,
		       to_bank   || '|' || to_account,
		       amount_paid, is_laundering, coalesce(pattern_id, 0)
		FROM aml_transactions
		WHERE ($1::text[] IS NULL OR payment_format = ANY($1))
		  AND amount_paid >= $2
		ORDER BY ts`, formats, cfg.MinAmount)
	if err != nil {
		return nil, fmt.Errorf("load ledger: %w", err)
	}
	defer rows.Close()

	led := &Ledger{}
	index := make(map[string]int32)
	intern := func(s string) int32 {
		if id, ok := index[s]; ok {
			return id
		}
		id := int32(len(led.Accounts))
		index[s] = id
		led.Accounts = append(led.Accounts, s)
		return id
	}

	for rows.Next() {
		var t Txn
		var from, to string
		if err := rows.Scan(&t.ID, &t.TS, &from, &to, &t.Amount, &t.IsLaundering, &t.PatternID); err != nil {
			return nil, err
		}
		t.From, t.To = intern(from), intern(to)
		led.Txns = append(led.Txns, t)
	}
	return led, rows.Err()
}

// ActivePeriod finds where the ledger stops behaving like a working ledger.
//
// This generator emits background traffic for the first ten days and then stops,
// while continuing to play out laundering patterns already in flight. The tail
// is therefore 58% to 73% laundering against a 0.1% base rate. Any detector
// scores brilliantly there — near-perfect recall at high precision — and the
// number would mean nothing, because it was measured on a stretch of ledger
// where most transfers really are laundering.
//
// The cut is found rather than hardcoded: the first day whose volume falls below
// a tenth of the median daily volume, which locates the collapse without
// assuming this particular dataset's calendar.
func ActivePeriod(txns []Txn) time.Time {
	if len(txns) == 0 {
		return time.Time{}
	}
	byDay := make(map[time.Time]int)
	for _, t := range txns {
		byDay[t.TS.Truncate(24*time.Hour)]++
	}
	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	counts := make([]int, 0, len(byDay))
	for _, d := range days {
		counts = append(counts, byDay[d])
	}
	sorted := append([]int(nil), counts...)
	sort.Ints(sorted)
	median := sorted[len(sorted)/2]

	for i, d := range days {
		if counts[i]*10 < median {
			return d
		}
	}
	return txns[len(txns)-1].TS.Add(time.Second)
}

// Trim drops transfers at or after cut.
func Trim(led *Ledger, cut time.Time) *Ledger {
	out := &Ledger{Accounts: led.Accounts}
	for _, t := range led.Txns {
		if t.TS.Before(cut) {
			out.Txns = append(out.Txns, t)
		}
	}
	return out
}

// Detect walks the ledger in overlapping windows and returns the groups found.
func Detect(led *Ledger, cfg Config) []Candidate {
	c, _ := DetectTimed(led, cfg)
	return c
}

// DetectTimed is Detect with the per-window cost recorded alongside.
//
// The timing is per window rather than for the pass as a whole because a window
// closure is the unit of work a deployed detector would actually do: the batch
// run here processes years at once, but in service it would wake every stride
// and handle one window. A total divided by the number of windows would hide
// that a busy window costs an order of magnitude more than a quiet one.
func DetectTimed(led *Ledger, cfg Config) ([]Candidate, []latency.Window) {
	if len(led.Txns) == 0 {
		return nil, nil
	}
	window := time.Duration(cfg.WindowHours) * time.Hour
	stride := time.Duration(cfg.StrideHours) * time.Hour

	start := led.Txns[0].TS
	end := led.Txns[len(led.Txns)-1].TS

	// A group found in one window usually reappears in the next, since windows
	// overlap. Keyed by its transaction set, the duplicate is dropped and the
	// earliest sighting kept.
	seen := make(map[string]bool)
	var out []Candidate
	var timings []latency.Window

	for ws := start; !ws.After(end); ws = ws.Add(stride) {
		we := ws.Add(window)
		lo := sort.Search(len(led.Txns), func(i int) bool { return !led.Txns[i].TS.Before(ws) })
		hi := sort.Search(len(led.Txns), func(i int) bool { return !led.Txns[i].TS.Before(we) })

		// A window is recorded whether or not it clears MinTxns. Skipping the
		// quiet ones would report the cost of only the expensive half of the
		// work, and would leave the transfers inside them looking uncovered when
		// in fact they were seen and dismissed.
		t0 := time.Now()
		var found []Candidate
		if hi-lo >= cfg.MinTxns {
			found = detectWindow(led.Txns[lo:hi], cfg, ws)
		}
		timings = append(timings, latency.Window{Start: ws, Txns: hi - lo, Process: time.Since(t0)})

		for _, c := range found {
			key := candidateKey(c.TxnIDs)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out, timings
}

// detectWindow groups the transfers inside one time slice.
func detectWindow(txns []Txn, cfg Config, windowStart time.Time) []Candidate {
	// Degree is counted first so over-connected accounts can be excluded from
	// linking before any group forms around them.
	degree := make(map[int32]int)
	for _, t := range txns {
		degree[t.From]++
		degree[t.To]++
	}

	// Dense local indices keep the union-find array small: a window touches a
	// fraction of the half million accounts in the ledger.
	local := make(map[int32]int32)
	localOf := func(a int32) int32 {
		if id, ok := local[a]; ok {
			return id
		}
		id := int32(len(local))
		local[a] = id
		return id
	}
	for _, t := range txns {
		if degree[t.From] > cfg.MaxAccountDegree || degree[t.To] > cfg.MaxAccountDegree {
			continue
		}
		localOf(t.From)
		localOf(t.To)
	}
	if len(local) == 0 {
		return nil
	}

	uf := graph.NewUnionFind(len(local))
	for _, t := range txns {
		if degree[t.From] > cfg.MaxAccountDegree || degree[t.To] > cfg.MaxAccountDegree {
			continue
		}
		uf.Union(local[t.From], local[t.To])
	}

	// Collect each component's transfers.
	members := make(map[int32][]Txn)
	for _, t := range txns {
		if degree[t.From] > cfg.MaxAccountDegree || degree[t.To] > cfg.MaxAccountDegree {
			continue
		}
		root := uf.Find(local[t.From])
		members[root] = append(members[root], t)
	}

	var out []Candidate
	for _, group := range members {
		c, ok := summarise(group, cfg, windowStart)
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// summarise turns a component into a candidate, rejecting groups too small to
// be worth a human's attention.
func summarise(group []Txn, cfg Config, windowStart time.Time) (Candidate, bool) {
	senders := make(map[int32]bool)
	receivers := make(map[int32]bool)
	accounts := make(map[int32]bool)
	ids := make([]int32, 0, len(group))

	for _, t := range group {
		senders[t.From] = true
		receivers[t.To] = true
		accounts[t.From] = true
		accounts[t.To] = true
		ids = append(ids, t.ID)
	}
	if len(group) < cfg.MinTxns || len(accounts) < cfg.MinAccounts {
		return Candidate{}, false
	}
	if cfg.MaxTxns > 0 && len(group) > cfg.MaxTxns {
		return Candidate{}, false
	}
	if cfg.MaxAccounts > 0 && len(accounts) > cfg.MaxAccounts {
		return Candidate{}, false
	}

	acctList := make([]int32, 0, len(accounts))
	for a := range accounts {
		acctList = append(acctList, a)
	}
	sort.Slice(acctList, func(i, j int) bool { return acctList[i] < acctList[j] })
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	return Candidate{
		TxnIDs:    ids,
		Accounts:  acctList,
		Typology:  classify(senders, receivers, accounts),
		Senders:   len(senders),
		Receivers: len(receivers),
		Window:    windowStart,
		Features:  features(group, senders, receivers, accounts),
	}, true
}

// FeatureSet selects which features the ranker sees.
//
// Two sets exist so a new feature can be measured against its own absence
// rather than added and assumed to help. Every number in docs/FINDINGS.md that
// compares them was produced by running the same pipeline twice with this flag
// as the only difference.
type FeatureSet string

const (
	// FeatureSetBase is the nine shape-and-money features the published
	// results were measured on. It is frozen: changing it would mean every
	// before-and-after comparison measured a rewrite instead of a feature.
	FeatureSetBase FeatureSet = "base"

	// FeatureSetTemporal adds what the timestamps say about arrangement in
	// time. It extends the base set rather than reordering it, so a
	// coefficient means the same thing under either.
	FeatureSetTemporal FeatureSet = "temporal"
)

// DefaultFeatureSet is what the console and the shipped defaults use.
const DefaultFeatureSet = FeatureSetTemporal

var featureNames = map[FeatureSet][]string{
	FeatureSetBase: {
		"log_total_amount", "log_max_amount", "log_mean_amount",
		"conservation", "pass_through", "log_txns", "log_accounts",
		"density", "span_hours",
	},
}

func init() {
	featureNames[FeatureSetTemporal] = append(
		append([]string(nil), featureNames[FeatureSetBase]...),
		"burstiness", "max_hour_share", "fast_forward")
}

// FeatureNamesFor labels the coefficients of a model fitted on the given set.
// An unrecognised set falls back to the default rather than returning a short
// list, because a length mismatch would mislabel every coefficient printed.
func FeatureNamesFor(set FeatureSet) []string {
	if n, ok := featureNames[set]; ok {
		return n
	}
	return featureNames[DefaultFeatureSet]
}

// VectorFor renders the features for the ranking model, in the order declared
// by FeatureNamesFor. Amounts and counts are log-scaled: they span nine orders
// of magnitude, and untransformed they would let a handful of enormous
// transfers dictate the fit. The temporal features are already ratios and are
// left alone.
func (f Features) VectorFor(set FeatureSet) []float64 {
	base := []float64{
		math.Log1p(f.TotalAmount),
		math.Log1p(f.MaxAmount),
		math.Log1p(f.MeanAmount),
		f.Conservation,
		f.PassThroughRatio,
		math.Log1p(float64(f.Txns)),
		math.Log1p(float64(f.Accounts)),
		f.Density,
		f.SpanHours,
	}
	if set == FeatureSetBase {
		return base
	}
	return append(base, f.Burstiness, f.MaxHourShare, f.FastForward)
}

// Vector renders the default feature set.
func (f Features) Vector() []float64 { return f.VectorFor(DefaultFeatureSet) }

// features summarises how money moves through a group.
func features(group []Txn, senders, receivers, accounts map[int32]bool) Features {
	f := Features{Txns: len(group), Accounts: len(accounts)}

	inflow := make(map[int32]float64)
	outflow := make(map[int32]float64)
	first, last := group[0].TS, group[0].TS

	for _, t := range group {
		f.TotalAmount += t.Amount
		if t.Amount > f.MaxAmount {
			f.MaxAmount = t.Amount
		}
		outflow[t.From] += t.Amount
		inflow[t.To] += t.Amount
		if t.TS.Before(first) {
			first = t.TS
		}
		if t.TS.After(last) {
			last = t.TS
		}
	}
	f.MeanAmount = f.TotalAmount / float64(len(group))
	f.SpanHours = last.Sub(first).Hours()
	f.Density = float64(len(group)) / float64(len(accounts))

	// Intermediaries are accounts on both sides of the group.
	passThrough := 0
	var conservationSum float64
	for a := range accounts {
		in, out := inflow[a], outflow[a]
		if in > 0 && out > 0 {
			passThrough++
			ratio := out / in
			if ratio > 1 {
				ratio = 1 / ratio // symmetric: forwarding 90% or 110% are equally close
			}
			conservationSum += ratio
		}
	}
	f.PassThroughRatio = float64(passThrough) / float64(len(accounts))
	if passThrough > 0 {
		f.Conservation = conservationSum / float64(passThrough)
	}

	f.Burstiness = burstiness(group)
	f.MaxHourShare = maxHourShare(group)
	f.FastForward = fastForward(group)
	return f
}

// classify names the shape from how sending and receiving roles overlap.
//
// The signatures come from the labelled rings: a fan-out has exactly one sender
// feeding roughly seven receivers; a cycle has every account both sending and
// receiving, so accounts, senders and receivers all match; a bipartite group
// splits cleanly into a sending side and a receiving side with nobody in both.
// Shapes that share a signature — scatter-gather against gather-scatter against
// stack — are reported as MIXED rather than guessed at, since naming them
// requires flow direction analysis this pass does not do.
func classify(senders, receivers, accounts map[int32]bool) string {
	both := 0
	for a := range senders {
		if receivers[a] {
			both++
		}
	}
	switch {
	case len(senders) == 1:
		return "FAN-OUT"
	case len(receivers) == 1:
		return "FAN-IN"
	case both == len(accounts):
		return "CYCLE"
	case both == 0:
		return "BIPARTITE"
	default:
		return "MIXED"
	}
}

func candidateKey(ids []int32) string {
	b := make([]byte, 0, len(ids)*4)
	for _, id := range ids {
		b = append(b, byte(id), byte(id>>8), byte(id>>16), byte(id>>24))
	}
	return string(b)
}
