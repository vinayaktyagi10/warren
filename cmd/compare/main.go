// Command compare runs WARREN against a per-transaction baseline on identical
// held-out data at an identical alert budget.
//
// This is the load-bearing experiment. WARREN's whole claim is that laundering
// is invisible at the level of a single transfer, and a claim like that is worth
// nothing unless the obvious alternative is built properly and measured.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/vinayaktyagi10/warren/internal/baseline"
	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/score"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	cfg := detect.DefaultConfig()
	topK := flag.Int("top", 50, "alert budget, in WARREN candidates")
	trainFraction := flag.Float64("train-fraction", 0.7, "share of the active period used for fitting")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	led, err := detect.Load(ctx, pool, cfg)
	if err != nil {
		log.Fatalf("load: %v", err)
	}
	led = detect.Trim(led, detect.ActivePeriod(led.Txns))
	cut := detect.SplitTime(led, *trainFraction)
	log.Printf("ledger %d transfers, fitting on everything before %s",
		len(led.Txns), cut.Format("2006-01-02 15:04"))

	// --- WARREN ---
	all := detect.Detect(led, cfg)
	trainC, testC := detect.Split(all, cut)
	ranker := detect.TrainRanker(trainC, detect.Labels(led, trainC), detect.DefaultFeatureSet, score.DefaultTrainOpts())

	scores := ranker.ScoreAll(testC)
	order := detect.RankOrder(scores)

	k := *topK
	if k > len(order) {
		k = len(order)
	}
	warrenFlagged := make(map[int32]bool)
	for _, idx := range order[:k] {
		for _, id := range testC[idx].TxnIDs {
			warrenFlagged[id] = true
		}
	}

	// --- baseline ---
	// Scope is the whole held-out period, not the subset WARREN surfaced.
	// Scoring the baseline only inside WARREN's candidate pool would hand it
	// WARREN's recall for free and ask it merely to re-rank within it, which is
	// not a comparison of the two approaches but of one approach against itself.
	scopeFn := func(t detect.Txn) bool { return !t.TS.Before(cut) }
	inScope := 0
	for _, t := range led.Txns {
		if scopeFn(t) {
			inScope++
		}
	}

	start := time.Now()
	// History from the fitting period only — see the package comment on leakage.
	agg := baseline.BuildAggregates(led, func(t detect.Txn) bool { return t.TS.Before(cut) })
	base := baseline.Train(led, agg, func(t detect.Txn) bool { return t.TS.Before(cut) })
	log.Printf("baseline fitted in %s", time.Since(start).Round(time.Millisecond))

	cmp := baseline.Compare(led, base, scopeFn, warrenFlagged)

	// --- report ---
	fmt.Printf("\nheld-out scope: %d transfers, %d laundering (%.3f%%), %d labelled rings\n",
		inScope, cmp.TotalLaundering,
		100*float64(cmp.TotalLaundering)/float64(inScope), cmp.TotalRings)
	fmt.Printf("both systems flag the same %d transfers\n\n", cmp.FlaggedTxns)

	fmt.Printf("%-28s %14s %14s\n", "", "per-transaction", "WARREN")
	fmt.Printf("%-28s %14d %14d\n", "laundering transfers caught", cmp.BaselineTP, cmp.WarrenTP)
	fmt.Printf("%-28s %13.2f%% %13.2f%%\n", "precision",
		100*float64(cmp.BaselineTP)/float64(cmp.FlaggedTxns),
		100*float64(cmp.WarrenTP)/float64(cmp.FlaggedTxns))
	fmt.Printf("%-28s %13.2f%% %13.2f%%\n", "recall",
		100*float64(cmp.BaselineTP)/float64(cmp.TotalLaundering),
		100*float64(cmp.WarrenTP)/float64(cmp.TotalLaundering))
	fmt.Printf("%-28s %11d/%-3d %11d/%-3d\n", "complete rings recovered",
		cmp.BaselineRings, cmp.TotalRings, cmp.WarrenRings, cmp.TotalRings)

	fmt.Printf("\n%s\n", base.Explain())

	// How invisible are these rings, transfer by transfer?
	blind := baseline.Blindness(led, base, scopeFn)
	var rows []baseline.RingBlindness
	for _, b := range blind {
		rows = append(rows, b)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BestPercentile > rows[j].BestPercentile })

	invisible := 0
	for _, b := range rows {
		if b.FlaggedAt10Pct == 0 {
			invisible++
		}
	}
	fmt.Printf("row-level invisibility across %d held-out rings\n", len(rows))
	fmt.Printf("  %d rings (%.0f%%) have not one transfer in the riskiest 10%%\n",
		invisible, 100*float64(invisible)/float64(len(rows)))

	// Substantial rings only. A ring with one surviving transfer says nothing
	// about structure - it is a fragment, and quoting it would be padding.
	var solid []baseline.RingBlindness
	for _, b := range rows {
		if b.Txns >= 4 && b.FlaggedAt10Pct == 0 {
			solid = append(solid, b)
		}
	}
	sort.Slice(solid, func(i, j int) bool { return solid[i].Txns > solid[j].Txns })

	fmt.Printf("\n  largest rings a per-transaction scorer sees nothing of:\n")
	fmt.Printf("  %-8s %6s %17s %19s\n", "ring", "txns", "best percentile", "median percentile")
	for _, b := range solid[:min(8, len(solid))] {
		fmt.Printf("  %-8d %6d %16.1f%% %18.1f%%\n",
			b.PatternID, b.Txns, 100*b.BestPercentile, 100*b.MedianPercentile)
	}
}
