// Command assess runs the full pipeline: detect rings, rank them, and put the
// highest-scoring ones through the bounded assessment chain.
//
// This is the end-to-end path — ledger to explained, gated, recorded decision.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/audit"
	"github.com/vinayaktyagi10/warren/internal/config"
	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/score"
)

func main() {
	cfg := detect.DefaultConfig()
	topK := flag.Int("top", 5, "how many of the highest-scoring rings to assess")
	model := flag.String("model", agent.DefaultGeminiModel, "primary assessor model")
	fallbackModel := flag.String("fallback-model", "gemini-3.5-flash-lite",
		"second model tried when the primary is unavailable; empty disables the tier")
	offline := flag.Bool("offline", false,
		"skip the model entirely and decide on the deterministic fallback, to demonstrate degraded operation")
	trainFraction := flag.Float64("train-fraction", 0.7, "share of the active period used to fit the ranker")
	envFile := flag.String("env", ".env", "file to read GEMINI_API_KEY from")
	flag.Parse()

	if err := config.LoadDotEnv(*envFile); err != nil {
		log.Fatalf("read %s: %v", *envFile, err)
	}

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

	candidates := detect.Detect(led, cfg)
	train, test := detect.Split(candidates, detect.SplitTime(led, *trainFraction))
	model2 := detect.TrainRanker(train, detect.Labels(led, train), detect.DefaultFeatureSet, score.DefaultTrainOpts())
	log.Printf("detected %d candidates, ranking %d held-out", len(candidates), len(test))

	scores := model2.ScoreAll(test)
	order := make([]int, len(test))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })

	auditLog, err := audit.New(ctx, pool)
	if err != nil {
		log.Fatalf("audit: %v", err)
	}

	chain := buildChain(ctx, *model, *fallbackModel, *offline)
	log.Printf("assessor chain: %s", strings.Join(tierNames(chain), " -> "))

	// Ground truth is shown alongside each decision. The assessor never sees it;
	// it is printed so a reader can judge the decision rather than take its word.
	truth := ringTruth(led)

	n := *topK
	if n > len(order) {
		n = len(order)
	}
	fmt.Printf("\n")
	for rank, idx := range order[:n] {
		c := test[idx]
		ev := evidenceFrom(rank+1, c, scores[idx])

		start := time.Now()
		a := chain.Assess(ctx, ev)
		took := time.Since(start)

		seq, hash, err := auditLog.Record(ctx, ev, a)
		if err != nil {
			log.Fatalf("record decision: %v", err)
		}
		report(rank+1, c, ev, a, took, truth, seq, hash)
	}
}

// buildChain assembles the degradation ladder. The reasoning model answers when
// it can; a smaller, less contended model covers the 503s the popular one
// returns under load; the deterministic rule covers everything else.
func buildChain(ctx context.Context, model, fallbackModel string, offline bool) *agent.Chain {
	policy := agent.DefaultPolicy()
	if offline {
		return agent.NewChain(policy)
	}

	key := os.Getenv("GEMINI_API_KEY")
	var tiers []agent.Assessor
	for _, m := range []string{model, fallbackModel} {
		if m == "" {
			continue
		}
		a, err := agent.NewGeminiAssessor(ctx, key, m)
		if err != nil {
			log.Printf("assessor %s unavailable at startup: %v", m, err)
			continue
		}
		tiers = append(tiers, a)
	}
	return agent.NewChain(policy, tiers...)
}

func tierNames(c *agent.Chain) []string {
	names := make([]string, 0, len(c.Tiers))
	for _, t := range c.Tiers {
		names = append(names, t.Name())
	}
	return names
}

func evidenceFrom(id int, c detect.Candidate, s float64) agent.Evidence {
	return agent.Evidence{
		RingID:       id,
		Typology:     c.Typology,
		Accounts:     c.Features.Accounts,
		Txns:         c.Features.Txns,
		TotalAmount:  c.Features.TotalAmount,
		MaxAmount:    c.Features.MaxAmount,
		Conservation: c.Features.Conservation,
		PassThrough:  c.Features.PassThroughRatio,
		SpanHours:    c.Features.SpanHours,
		Score:        s,
		WindowStart:  c.Window,
	}
}

// ringTruth maps transfer ids to whether they are labelled laundering.
func ringTruth(led *detect.Ledger) map[int32]bool {
	out := make(map[int32]bool, len(led.Txns))
	for _, t := range led.Txns {
		out[t.ID] = t.IsLaundering
	}
	return out
}

func report(rank int, c detect.Candidate, ev agent.Evidence, a agent.Assessment,
	took time.Duration, truth map[int32]bool, seq int64, hash string) {
	laundering := 0
	for _, id := range c.TxnIDs {
		if truth[id] {
			laundering++
		}
	}

	fmt.Printf("─── ring %d ───────────────────────────────────────────────\n", rank)
	fmt.Printf("shape %s · %d accounts · %d transfers · %.2f moved · %.1fh\n",
		ev.Typology, ev.Accounts, ev.Txns, ev.TotalAmount, ev.SpanHours)
	fmt.Printf("conservation %.3f · pass-through %.3f · score %.3f\n",
		ev.Conservation, ev.PassThrough, ev.Score)

	fmt.Printf("\nDECISION  %s", strings.ToUpper(string(a.Action)))
	if a.Adjusted() {
		fmt.Printf("   (assessor proposed %s)", strings.ToUpper(string(a.Proposed)))
	}
	fmt.Printf("\nconfidence %.2f · via %s · %s\n", a.Confidence, a.Source, took.Round(time.Millisecond))

	if a.Rationale != "" {
		fmt.Printf("\n%s\n", wrap(a.Rationale, 72))
	}
	if len(a.Adjustments) > 0 {
		fmt.Printf("\npolicy:\n")
		for _, adj := range a.Adjustments {
			fmt.Printf("  · %s\n", wrap(adj, 68))
		}
	}

	verdict := "no labelled laundering in this group"
	if laundering > 0 {
		verdict = fmt.Sprintf("%d of %d transfers are labelled laundering", laundering, len(c.TxnIDs))
	}
	fmt.Printf("\naudit #%d  %s\n", seq, hash[:16])
	fmt.Printf("ground truth (withheld from the assessor): %s\n\n", verdict)
}

func wrap(s string, width int) string {
	var b strings.Builder
	col := 0
	for _, word := range strings.Fields(s) {
		if col > 0 && col+len(word)+1 > width {
			b.WriteString("\n")
			col = 0
		} else if col > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(word)
		col += len(word)
	}
	return b.String()
}
