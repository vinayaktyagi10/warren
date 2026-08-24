// Command serve runs the WARREN operator console.
//
// The pipeline runs once at startup — detection over five million transfers is
// not something to repeat per page load — and the console reads from it. Only
// assessment happens live, because only assessment calls a model.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/audit"
	"github.com/vinayaktyagi10/warren/internal/config"
	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/detect"
	"github.com/vinayaktyagi10/warren/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	model := flag.String("model", agent.DefaultGeminiModel, "primary assessor model")
	fallbackModel := flag.String("fallback-model", "gemini-3.5-flash-lite",
		"second model tried when the primary is unavailable; empty disables the tier")
	offline := flag.Bool("offline", false,
		"run with no model at all, deciding on the deterministic fallback")
	trainFraction := flag.Float64("train-fraction", 0.7, "share of the active period used to fit the ranker")
	seed := flag.Int("seed-decisions", 4,
		"decide this many top alerts at startup when the audit log is empty; 0 disables")
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

	auditLog, err := audit.New(ctx, pool)
	if err != nil {
		log.Fatalf("audit: %v", err)
	}

	chain := buildChain(ctx, *model, *fallbackModel, *offline)

	srv, err := web.New(ctx, pool, detect.DefaultConfig(), chain, auditLog, *trainFraction)
	if err != nil {
		log.Fatalf("prepare: %v", err)
	}
	srv.SeedAudit(ctx, *seed)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous, because an assessment request waits on a model call and may
		// walk the whole fallback chain before answering.
		WriteTimeout: 3 * time.Minute,
	}

	log.Printf("console on http://localhost%s", *addr)
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func buildChain(ctx context.Context, model, fallbackModel string, offline bool) *agent.Chain {
	policy := agent.DefaultPolicy()
	if offline {
		log.Printf("offline: deciding on the deterministic fallback only")
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
