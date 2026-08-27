// Command enforce measures what WARREN's action layer actually achieves: it
// replays the held-out period forward, decides each window closure's alerts,
// imposes the restrictions those decisions authorise, and counts the value the
// restrictions then sat in front of.
//
// It runs one or several detection geometries. Given more than one it reports
// each alone and then all of them together against a single shared register,
// which is the dual-geometry argument: a wide window sees a ring's shape, a
// narrow one still has something left to freeze, and the two are not the same
// job. Run apart their numbers cannot be added, because they lease the same
// accounts; run together the later lease extends the earlier one.
//
// The point of running it is that "the system takes action" is a claim, and an
// unmeasured claim about stopping people's money is the kind this project keeps
// throwing away.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/enforce"
	"github.com/vinayaktyagi10/warren/internal/score"
)

func main() {
	base := detect.DefaultConfig()
	geometries := flag.String("geometries", "72/24,24/6",
		"detection passes as window/stride in hours, comma separated; the first is the investigation pass")
	budget := flag.Int("budget", 250, "alerts each pass may raise over the replay period")
	trainFraction := flag.Float64("train-fraction", 0.5, "share of the active period used to fit each ranker")
	maxAccounts := flag.Int("max-accounts-per-ring", enforce.DefaultLimits().MaxAccountsPerRing,
		"largest ring a single automated decision may freeze")
	enforcing := flag.String("enforce", "",
		"which geometries may lease into the register, comma separated; empty means all of them")
	ceilings := flag.Bool("ceilings", false, "sweep the interception ceiling by window geometry and exit")
	flag.Parse()

	geoms, err := parseGeometries(*geometries)
	if err != nil {
		log.Fatalf("geometries: %v", err)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	led, err := detect.Load(ctx, pool, base)
	if err != nil {
		log.Fatalf("load: %v", err)
	}
	led = detect.Trim(led, detect.ActivePeriod(led.Txns))
	cut := detect.SplitTime(led, *trainFraction)
	heldOut := &detect.Ledger{Accounts: led.Accounts, Txns: afterCut(led.Txns, cut)}
	log.Printf("ledger %d transfers, split %s, %d held out",
		len(led.Txns), cut.Format("2006-01-02"), len(heldOut.Txns))

	if *ceilings {
		fmt.Print("\n=== interception ceiling: a perfect oracle detector, by window geometry ===\n\n",
			enforce.FormatCeilings(enforce.CeilingTable(heldOut, base, []int{72, 48, 24, 12, 6})))
		return
	}

	policy := agent.DefaultPolicy()
	decide := enforce.ThresholdDecider(policy)

	sources := make([]enforce.Source, 0, len(geoms))
	for _, g := range geoms {
		src := buildSource(led, base, g, cut, *budget, *maxAccounts)
		src.Enforces = *enforcing == "" || slices.Contains(strings.Split(*enforcing, ","), g.String())
		sources = append(sources, src)
	}

	// Each pass alone, so the combined figure has something to be read against.
	type row struct {
		name       string
		ringsFound int
		totalRings int
		recall     float64
		res        enforce.ReplayResult
	}
	var rows []row
	for _, src := range sources {
		solo := src
		solo.Enforces = true
		res, _ := enforce.ReplayMulti(heldOut, []enforce.Source{solo}, decide)
		fmt.Printf("\n=== %s alone ===\n\n%s", src.Name, res.String())

		rr := detect.EvaluateRanked(led, src.Candidates, src.Scores, []int{*budget})
		r := row{name: src.Name, res: res}
		if len(rr.Rows) > 0 {
			r.ringsFound, r.totalRings = rr.Rows[0].RingsFound, rr.Rows[0].TotalRings
			if den := rr.Rows[0].TxnTP + rr.Rows[0].TxnFN; den > 0 {
				r.recall = float64(rr.Rows[0].TxnTP) / float64(den)
			}
		}
		rows = append(rows, r)
	}

	if len(sources) == 1 {
		return
	}

	combined, _ := enforce.ReplayMulti(heldOut, sources, decide)
	fmt.Printf("\n=== all passes together, one register ===\n\n%s", combined.String())

	// The argument in one table. A single geometry has to choose between seeing
	// the ring and still having something to freeze; running both means the
	// investigation queue keeps the wide pass's recall while enforcement keeps
	// the narrow pass's runway.
	fmt.Printf("\n=== what each configuration buys, at %d alerts per pass ===\n\n", *budget)
	fmt.Printf("%-22s %14s %10s %12s %16s %10s\n",
		"configuration", "rings found", "recall", "rings hit", "value stopped", "% ceiling")
	for _, r := range rows {
		fmt.Printf("%-22s %6d/%-7d %9.1f%% %12d %16.0f %9.2f%%\n",
			r.name+" alone", r.ringsFound, r.totalRings, 100*r.recall,
			len(r.res.RingsHit), r.res.ValueLaunderingStopped, 100*r.res.OfCeiling())
	}
	best := rows[0]
	for _, r := range rows {
		if r.recall > best.recall {
			best = r
		}
	}
	label := "both together"
	if *enforcing != "" {
		label = "both, " + *enforcing + " leasing"
	}
	fmt.Printf("%-22s %6d/%-7d %9.1f%% %12d %16.0f %9.2f%%\n",
		label, best.ringsFound, best.totalRings, 100*best.recall,
		len(combined.RingsHit), combined.ValueLaunderingStopped, 100*combined.OfCeiling())
	leasing := "every pass"
	if *enforcing != "" {
		leasing = *enforcing
	}
	fmt.Printf("\nthe combined row takes detection from the widest pass, which is what feeds the\n"+
		"analyst queue, and enforcement from the register leased into by: %s\n", leasing)
}

// buildSource runs one geometry end to end: detect, split, fit a ranker on the
// fitting period only, and score the held-out candidates.
//
// Each pass gets its own ranker rather than sharing one. The geometries produce
// different candidate populations — a 24-hour window raises smaller, tighter
// groups than a 72-hour one — so a single fit would be a compromise between two
// distributions and would describe neither. The features are identical; only the
// coefficients differ, which keeps both models as explainable as the original.
func buildSource(led *detect.Ledger, base detect.Config, g geometry,
	cut time.Time, budget, maxAccounts int) enforce.Source {

	cfg := base
	cfg.WindowHours, cfg.StrideHours = g.window, g.stride

	all := detect.Detect(led, cfg)
	train, test := detect.Split(all, cut)
	model := detect.TrainRanker(train, detect.Labels(led, train), detect.DefaultFeatureSet, score.DefaultTrainOpts())

	scores := model.ScoreAll(test)
	log.Printf("%s: %d candidates, %d held out", g, len(all), len(test))

	// The lease lasts as long as the window that justified it. This is the whole
	// escalation rule: a pass that acted on 24 hours of evidence buys a 24-hour
	// hold, and if a 72-hour pass later agrees about the same accounts its longer
	// lease replaces the short one. Nobody has to release the short one when the
	// confirmation never comes — it lapses.
	lim := enforce.DefaultLimits()
	lim.MaxAccountsPerRing = maxAccounts
	lim.FrozenFor = time.Duration(g.window) * time.Hour
	lim.Pass = g.String()

	return enforce.Source{
		Name: g.String(), Cfg: cfg,
		Candidates: test, Scores: scores,
		Budget: budget, Limits: lim,
	}
}

type geometry struct{ window, stride int }

func (g geometry) String() string { return fmt.Sprintf("%dh/%dh", g.window, g.stride) }

func parseGeometries(s string) ([]geometry, error) {
	var out []geometry
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		w, st, ok := strings.Cut(part, "/")
		if !ok {
			return nil, fmt.Errorf("%q is not window/stride", part)
		}
		wi, err := strconv.Atoi(strings.TrimSpace(w))
		if err != nil {
			return nil, fmt.Errorf("%q: %w", part, err)
		}
		si, err := strconv.Atoi(strings.TrimSpace(st))
		if err != nil {
			return nil, fmt.Errorf("%q: %w", part, err)
		}
		if wi <= 0 || si <= 0 || si > wi {
			return nil, fmt.Errorf("%q: need 0 < stride <= window", part)
		}
		out = append(out, geometry{wi, si})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no geometries given")
	}
	return out, nil
}

func afterCut(txns []detect.Txn, cut time.Time) []detect.Txn {
	for i, t := range txns {
		if !t.TS.Before(cut) {
			return txns[i:]
		}
	}
	return nil
}
