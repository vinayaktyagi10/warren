// Command audit reads and verifies the decision log.
//
// Verification answers the question an auditor actually asks, which is not
// "what did the system decide" but "is this record of what it decided still
// what it originally said".
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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/audit"
	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/enforce"
)

func main() {
	verify := flag.Bool("verify", false, "recompute the hash chain and report any break")
	tamper := flag.Int64("tamper", 0,
		"DEMO ONLY: rewrite the rationale of this entry in place, the way someone covering their tracks would")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	logbook, err := audit.New(ctx, pool)
	if err != nil {
		log.Fatalf("audit: %v", err)
	}

	if *tamper > 0 {
		// Edits the content only, leaving every hash untouched — exactly what an
		// attacker with database access would do, and exactly what the chain is
		// built to expose.
		tag, err := pool.Exec(ctx, `
			UPDATE audit_log
			SET rationale = 'Reviewed and cleared. No further action required.',
			    action = 'allow'
			WHERE seq = $1`, *tamper)
		if err != nil {
			log.Fatalf("tamper: %v", err)
		}
		if tag.RowsAffected() == 0 {
			log.Fatalf("no entry with seq %d", *tamper)
		}
		fmt.Printf("entry %d was rewritten in place: action set to 'allow', rationale replaced.\n", *tamper)
		fmt.Printf("no hash was touched. Run with -verify.\n")
		return
	}

	if *verify {
		res, err := logbook.Verify(ctx)
		if err != nil {
			log.Fatalf("verify: %v", err)
		}
		fmt.Printf("audit log: %d entries\n\n", res.Entries)
		if res.Valid {
			fmt.Printf("chain intact — every entry hashes to its recorded value and links\n")
			fmt.Printf("to the one before it.\n")
			return
		}
		fmt.Printf("CHAIN BROKEN at entry %d\n\n  %s\n\n", res.BrokenSeq, res.BrokenReason)
		fmt.Printf("the decision recorded at entry %d is not the decision that was made.\n", res.BrokenSeq)
		reportOrphanedHolds(ctx, pool, res.BrokenSeq)
		os.Exit(1)
	}

	if err := list(ctx, pool); err != nil {
		log.Fatalf("list: %v", err)
	}
}

// reportOrphanedHolds names the accounts whose money is being held on the
// authority of a decision that no longer verifies.
//
// This is what the two chains exist to make sayable. A broken hash on its own is
// an abstraction — it says a record changed. Joined to the restriction ledger it
// says something an operator has to act on today: these people's transfers are
// being stopped, and the reason given for stopping them is no longer the reason
// that was recorded. Either the log is restored or the holds come off.
func reportOrphanedHolds(ctx context.Context, pool *pgxpool.Pool, brokenSeq int64) {
	store, err := enforce.NewStore(ctx, pool)
	if err != nil {
		return
	}
	held, err := store.ActiveAt(ctx, time.Now().UTC())
	if err != nil {
		return
	}
	var orphaned []enforce.Held
	for _, h := range held {
		if h.Tier == enforce.TierFrozen && !h.Expired &&
			h.DecisionSeq != nil && *h.DecisionSeq >= brokenSeq {
			orphaned = append(orphaned, h)
		}
	}
	if len(orphaned) == 0 {
		return
	}
	sort.Slice(orphaned, func(i, j int) bool { return orphaned[i].Account < orphaned[j].Account })

	fmt.Printf("\n%d accounts are currently frozen on the authority of entry %d or later:\n\n",
		len(orphaned), brokenSeq)
	for i, h := range orphaned {
		if i == 5 {
			fmt.Printf("  ... and %d more\n", len(orphaned)-5)
			break
		}
		fmt.Printf("  %-24s ring %-5d expires %s  (decision %d)\n",
			h.Account, h.RingID, h.ExpiresAt.Format("2006-01-02 15:04"), *h.DecisionSeq)
	}
	fmt.Printf("\nthese holds no longer have a verifiable authority behind them.\n")
}

// list prints the log newest first, which is the order someone investigating a
// decision reads it in.
func list(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT seq, decided_at, ring_id, action, proposed, confidence, source,
		       rationale, adjustments, hash
		FROM audit_log ORDER BY seq DESC LIMIT 20`)
	if err != nil {
		return err
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var (
			seq         int64
			decidedAt   time.Time
			ringID      int
			action      string
			proposed    string
			confidence  float64
			source      string
			rationale   string
			adjustments []string
			hash        string
		)
		if err := rows.Scan(&seq, &decidedAt, &ringID, &action, &proposed,
			&confidence, &source, &rationale, &adjustments, &hash); err != nil {
			return err
		}
		found = true

		fmt.Printf("#%d  %s  ring %d\n", seq, decidedAt.Format("2006-01-02 15:04:05"), ringID)
		fmt.Printf("    %s", strings.ToUpper(action))
		if action != proposed {
			fmt.Printf("  (assessor proposed %s)", strings.ToUpper(proposed))
		}
		fmt.Printf("  confidence %.2f  via %s\n", confidence, source)
		if rationale != "" {
			fmt.Printf("    %s\n", truncate(rationale, 100))
		}
		for _, adj := range adjustments {
			fmt.Printf("    policy: %s\n", truncate(adj, 100))
		}
		fmt.Printf("    %s\n\n", hash[:32])
	}
	if !found {
		fmt.Printf("the log is empty. Run cmd/assess to make some decisions.\n")
	}
	return rows.Err()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
