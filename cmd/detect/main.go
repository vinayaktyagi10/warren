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
	testLabels := detect.Labels(led, test)

	pos := 0
	for _, y := range trainLabels {
		if y {
			pos++
		}
	}
	log.Printf("split at %s: %d train candidates (%d bearing a ring), %d held out (%d bearing a ring)",
		cut.Format("2006-01-02 15:04"), len(train), pos, len(test), countTrue(testLabels))

	model := score.Train(detect.Vectors(train), trainLabels, score.DefaultTrainOpts())
	fmt.Print("\n", model.Explain())

	scores := make([]float64, len(test))
	for i, v := range detect.Vectors(test) {
		scores[i] = model.Predict(v)
	}

	ranked := detect.EvaluateRanked(led, test, scores, []int{50, 100, 250, 500, 1000, 2500, 5000, len(test)})
	fmt.Print("\n=== ranked on held-out data, by alert budget ===\n\n", ranked.String())

	lat := detect.MeasureLatency(led, candidates, windows, cfg, detectWall, model.Predict)
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
