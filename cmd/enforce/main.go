// Command enforce measures what WARREN's action layer actually achieves: it
// replays the held-out period forward, decides each window's alerts, imposes the
// restrictions those decisions authorise, and counts the value the restrictions
// then sat in front of.
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
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/enforce"
	"github.com/vinayaktyagi10/warren/internal/score"
)

func main() {
	cfg := detect.DefaultConfig()
	budgets := flag.String("budgets", "50,250,1000",
		"alert budgets to replay, comma separated; each is a separate run")
	trainFraction := flag.Float64("train-fraction", 0.5, "share of the active period used to fit the ranker")
	flag.IntVar(&cfg.WindowHours, "window", cfg.WindowHours, "detection window width in hours")
	flag.IntVar(&cfg.StrideHours, "stride", cfg.StrideHours, "detection window stride in hours")
	ceilings := flag.Bool("ceilings", false, "sweep the interception ceiling by window geometry and exit")
	maxAccounts := flag.Int("max-accounts-per-ring", enforce.DefaultLimits().MaxAccountsPerRing,
		"largest ring a single automated decision may freeze")
	frozenHours := flag.Int("frozen-hours", 72, "how long a freeze lasts")
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
	log.Printf("ledger: %d transfers", len(led.Txns))

	all := detect.Detect(led, cfg)
	cut := detect.SplitTime(led, *trainFraction)
	train, test := detect.Split(all, cut)
	model := score.Train(detect.Vectors(train), detect.Labels(led, train), score.DefaultTrainOpts())

	scores := make([]float64, len(test))
	for i, v := range detect.Vectors(test) {
		scores[i] = model.Predict(v)
	}
	log.Printf("held out: %d candidates from %s", len(test), cut.Format("2006-01-02"))

	// Enforcement is replayed over the held-out period only. Acting inside the
	// fitting period would let the ranker's memory of those very groups decide
	// whose money moved, which is not a result, it is a leak.
	heldOut := &detect.Ledger{Accounts: led.Accounts, Txns: afterCut(led.Txns, cut)}
	log.Printf("enforceable ledger: %d transfers after the split", len(heldOut.Txns))

	// What could any windowed detector have stopped? Measured before the actual
	// result, so the actual result is read against a ceiling rather than a hope.
	if *ceilings {
		fmt.Print("\n=== interception ceiling: a perfect oracle detector, by window geometry ===\n\n",
			enforce.FormatCeilings(enforce.CeilingTable(heldOut, cfg, []int{72, 48, 24, 12, 6})))
		return
	}

	ranked := detect.EvaluateRanked(led, test, scores, parseBudgets(*budgets))
	fmt.Printf("\n=== detection at %dh/%dh ===\n\n%s", cfg.WindowHours, cfg.StrideHours, ranked.String())

	lim := enforce.DefaultLimits()
	lim.MaxAccountsPerRing = *maxAccounts
	lim.FrozenFor = time.Duration(*frozenHours) * time.Hour

	policy := agent.DefaultPolicy()
	decide := enforce.ThresholdDecider(policy)

	for _, b := range parseBudgets(*budgets) {
		rc := enforce.ReplayConfig{Budget: b, Limits: lim, Policy: policy}
		res, _ := enforce.Replay(heldOut, test, scores, cfg, rc, decide)
		fmt.Printf("\n=== alert budget %d ===\n\n%s", b, res.String())
	}
}

func afterCut(txns []detect.Txn, cut time.Time) []detect.Txn {
	for i, t := range txns {
		if !t.TS.Before(cut) {
			return txns[i:]
		}
	}
	return nil
}

func parseBudgets(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}
