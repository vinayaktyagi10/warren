// Command detect runs ring detection over the AML ledger and reports measured
// precision and recall against the labelled rings.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/forest"
	"github.com/vinayaktyagi10/warren/internal/registry"
	"github.com/vinayaktyagi10/warren/internal/score"
)

func main() {
	cfg := detect.DefaultConfig()
	formats := flag.String("formats", strings.Join(cfg.PaymentFormats, ","),
		"payment formats to consider, comma separated; empty means all")
	flag.Float64Var(&cfg.MinAmount, "min-amount", cfg.MinAmount, "ignore transfers below this amount")
	flag.IntVar(&cfg.WindowHours, "window", cfg.WindowHours, "window width in hours")
	flag.IntVar(&cfg.StrideHours, "stride", cfg.StrideHours, "window stride in hours")
	flag.IntVar(&cfg.MaxAccountDegree, "max-degree", cfg.MaxAccountDegree,
		"exclude accounts busier than this from linking, so hubs cannot fuse rings")
	flag.IntVar(&cfg.MinTxns, "min-txns", cfg.MinTxns, "smallest group worth reporting")
	flag.IntVar(&cfg.MinAccounts, "min-accounts", cfg.MinAccounts, "smallest account count worth reporting")
	flag.IntVar(&cfg.MaxTxns, "max-txns", cfg.MaxTxns, "reject groups larger than this; 0 disables")
	flag.IntVar(&cfg.MaxAccounts, "max-accounts", cfg.MaxAccounts, "reject groups with more accounts than this; 0 disables")
	analyse := flag.Bool("analyse", false, "compare features of ring-bearing candidates against ordinary ones")
	trainFraction := flag.Float64("train-fraction", 0.7, "share of the ledger's time span used to fit the ranker")
	features := flag.String("features", string(detect.DefaultFeatureSet),
		"which feature set the ranker sees: \"base\" is the nine shape-and-money\n"+
			"features, \"temporal\" adds burstiness, hour concentration and forwarding\n"+
			"speed, \"anomaly\" adds an isolation forest score, \"registry\" adds the\n"+
			"share of accounts on a simulated suspect list, \"all\" is everything.\n"+
			"Run two and compare: that is the only way to know a feature is worth it.")
	forestTrees := flag.Int("forest-trees", forest.DefaultOpts().Trees,
		"trees in the isolation forest used by the anomaly feature")
	forestSeed := flag.Int64("forest-seed", forest.DefaultOpts().Seed, "isolation forest seed")
	withRegistry := flag.Bool("registry", false,
		"simulate a shared suspect-account list (I4C-style) and give the ranker the\n"+
			"share of each group's accounts already on it. Implies -features registry.")
	regCoverage := flag.Float64("registry-coverage", registry.DefaultSimOpts().Coverage,
		"share of laundering accounts the simulated list ever names")
	regDelay := flag.Duration("registry-delay", registry.DefaultSimOpts().ReportDelay,
		"mean lag between an account moving money and appearing on the list")
	regFalse := flag.Float64("registry-false-rate", registry.DefaultSimOpts().FalseRate,
		"share of the simulated list that never laundered")
	regSeed := flag.Int64("registry-seed", registry.DefaultSimOpts().Seed, "registry simulation seed")
	shapeBudget := flag.Int("shape-budget", 1000,
		"alert budget at which to break recall out by ring shape")
	trimTail := flag.Bool("trim-tail", true,
		"drop the stretch where the generator stops background traffic and the base rate becomes unrepresentative")
	flag.Parse()

	if *formats == "" {
		cfg.PaymentFormats = nil
	} else {
		cfg.PaymentFormats = strings.Split(*formats, ",")
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	typologies, err := loadTypologies(ctx, pool)
	if err != nil {
		log.Fatalf("typologies: %v", err)
	}

	start := time.Now()
	led, err := detect.Load(ctx, pool, cfg)
	if err != nil {
		log.Fatalf("load: %v", err)
	}
	log.Printf("ledger: %d transfers over %d accounts (loaded in %s)",
		len(led.Txns), len(led.Accounts), time.Since(start).Round(time.Millisecond))

	if *trimTail {
		cut := detect.ActivePeriod(led.Txns)
		before := len(led.Txns)
		led = detect.Trim(led, cut)
		log.Printf("trimmed the generator's quiet tail at %s: %d -> %d transfers",
			cut.Format("2006-01-02"), before, len(led.Txns))
	}

	start = time.Now()
	candidates, windows := detect.DetectTimed(led, cfg)
	detectWall := time.Since(start)
	log.Printf("detected %d candidate rings in %s", len(candidates), detectWall.Round(time.Millisecond))

	set := detect.FeatureSet(*features)
	needsForest := set == detect.FeatureSetAnomaly || set == detect.FeatureSetAll

	var reg *registry.Registry
	if *withRegistry {
		reg = registry.Simulate(led, registry.SimOpts{
			Coverage: *regCoverage, ReportDelay: *regDelay,
			FalseRate: *regFalse, Seed: *regSeed,
		})
		if set != detect.FeatureSetAll {
			set = detect.FeatureSetRegistry
		}
		log.Printf("simulated suspect registry: %d accounts listed, %.0f%% coverage, "+
			"%s mean reporting delay, %.0f%% false reports",
			reg.Size(), 100**regCoverage, *regDelay, 100**regFalse)
	}
	registry.Annotate(candidates, reg)

	if *analyse {
		fmt.Print("\n", detect.AnalyseFeatures(led, candidates))
	}

	rep := detect.Evaluate(led, candidates, typologies)
	fmt.Print("\n=== unranked: every candidate the graph pass raised ===\n\n", rep.String())

	// Fit the ranker on the earlier part of the ledger and judge it on the
	// later part, which the fit never saw.
	cut := detect.SplitTime(led, *trainFraction)
	train, test := detect.Split(candidates, cut)
	trainLabels := detect.Labels(led, train)

	if needsForest {
		// Fitted on the fitting split only. The forest takes no labels, but it
		// still learns the shape of the candidate population, and letting it
		// see the held-out period is letting "ordinary" be defined by the very
		// window it is about to be judged on.
		start := time.Now()
		forest.Annotate(train, candidates, forest.Opts{
			Trees: *forestTrees, SampleSize: forest.DefaultOpts().SampleSize, Seed: *forestSeed})
		train, test = detect.Split(candidates, cut)
		log.Printf("isolation forest: %d trees over %d fitting candidates in %s",
			*forestTrees, len(train), time.Since(start).Round(time.Millisecond))
	}
	testLabels := detect.Labels(led, test)

	pos := 0
	for _, y := range trainLabels {
		if y {
			pos++
		}
	}
	log.Printf("split at %s: %d train candidates (%d bearing a ring), %d held out (%d bearing a ring)",
		cut.Format("2006-01-02 15:04"), len(train), pos, len(test), countTrue(testLabels))

	ranker := detect.TrainRanker(train, trainLabels, set, score.DefaultTrainOpts())
	fmt.Printf("\nfeature set: %s\n%s", ranker.Set, ranker.Explain())

	scores := ranker.ScoreAll(test)

	budgets := []int{50, 100, 250, 500, 1000, 2500, 5000, len(test)}
	ranked := detect.EvaluateRankedByShape(led, test, scores, budgets, typologies)
	fmt.Print("\n=== ranked on held-out data, by alert budget ===\n\n", ranked.String())
	if s := ranked.ShapesAt(*shapeBudget); s != "" {
		fmt.Print("\n", s)
	}

	if reg != nil {
		// The control that decides whether any of this means anything. If
		// ranking by the list alone scores close to the fused model, then the
		// graph is decoration and the registry is doing the work.
		listOnly := make([]float64, len(test))
		for i, c := range test {
			listOnly[i] = c.Features.RegistryShare
		}
		control := detect.EvaluateRankedByShape(led, test, listOnly, budgets, typologies)
		fmt.Print("\n=== control: ranked by the suspect list alone, no graph ===\n\n",
			control.String())

		fmt.Print("\n=== what the graph adds to the list ===\n\n")
		fmt.Printf("%8s %10s %10s %14s %16s\n",
			"alerts", "listed", "reached", "implicated", "per listed acct")
		for _, k := range budgets {
			a := registry.Amplification(led, test, scores, k, reg)
			fmt.Printf("%8d %10d %10d %14d %16.2f\n",
				k, a.Listed, a.Reached, a.Implicated, a.PerListedAccount())
		}
		fmt.Print("\nimplicated counts laundering accounts the list did not already name;\n" +
			"the listed accounts themselves are excluded, or the input would be\n" +
			"counted as output.\n")
	}

	lat := detect.MeasureLatency(led, candidates, windows, cfg, detectWall, ranker)
	fmt.Print("\n=== latency ===\n\n", lat.String())
}

func countTrue(b []bool) int {
	n := 0
	for _, v := range b {
		if v {
			n++
		}
	}
	return n
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
