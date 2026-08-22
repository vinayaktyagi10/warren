// Command rings resolves transactions into client entities, links those clients
// into candidate abuse rings, and reports whether the rings it found actually
// concentrate fraud.
//
// The concentration report is the point. Any clustering method will produce
// clusters; the question is whether they isolate the loss. If ring members are
// no more fraudulent than the base rate, the graph is decorative and should be
// rejected rather than shipped.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/db"
	"github.com/vinayaktyagi10/warren/internal/graph"
)

func main() {
	minCorroboration := flag.Int("min-corroboration", 2,
		"independent shared values required to link two accounts; 1 reproduces the naive connected-components behaviour")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	cfg := graph.DefaultConfig()
	cfg.MinCorroboration = *minCorroboration

	res, err := graph.Build(ctx, pool, cfg)
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	log.Printf("clients=%d rings=%d", res.Clients, res.Rings)

	if err := concentration(ctx, pool); err != nil {
		log.Fatalf("concentration: %v", err)
	}
	if err := ringSizes(ctx, pool); err != nil {
		log.Fatalf("ring sizes: %v", err)
	}
}

// concentration compares the fraud rate inside detected rings against the rate
// outside them, and reports how much of the total loss the rings account for.
func concentration(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT c.ring_id IS NOT NULL          AS in_ring,
		       count(*)                       AS txns,
		       count(*) FILTER (WHERE t.is_fraud) AS frauds,
		       round(100.0 * count(*) FILTER (WHERE t.is_fraud) / count(*), 3) AS fraud_pct,
		       round(sum(t.transaction_amt) FILTER (WHERE t.is_fraud), 2)      AS fraud_amount
		FROM transactions t
		JOIN transaction_clients tc USING (transaction_id)
		JOIN clients c USING (client_id)
		GROUP BY 1 ORDER BY 1 DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	log.Printf("%-10s %10s %8s %10s %14s", "cohort", "txns", "frauds", "fraud_pct", "fraud_amount")
	for rows.Next() {
		var inRing bool
		var txns, frauds int64
		var pct, amount float64
		if err := rows.Scan(&inRing, &txns, &frauds, &pct, &amount); err != nil {
			return err
		}
		cohort := "outside"
		if inRing {
			cohort = "in ring"
		}
		log.Printf("%-10s %10d %8d %9.3f%% %14.2f", cohort, txns, frauds, pct, amount)
	}
	return rows.Err()
}

// ringSizes shows how ring membership is distributed. A method that produced one
// enormous ring would look successful on totals alone, so the shape is reported
// alongside the concentration.
func ringSizes(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		WITH r AS (
		  SELECT ring_id, count(*) AS clients FROM clients
		  WHERE ring_id IS NOT NULL GROUP BY ring_id
		)
		SELECT CASE WHEN clients = 2 THEN '2'
		            WHEN clients BETWEEN 3 AND 5 THEN '3-5'
		            WHEN clients BETWEEN 6 AND 20 THEN '6-20'
		            ELSE '21+' END AS size_band,
		       count(*) AS rings, sum(clients) AS clients, max(clients) AS largest
		FROM r GROUP BY 1 ORDER BY min(clients)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	log.Printf("%-10s %8s %10s %9s", "ring_size", "rings", "clients", "largest")
	for rows.Next() {
		var band string
		var rings, clients, largest int64
		if err := rows.Scan(&band, &rings, &clients, &largest); err != nil {
			return err
		}
		log.Printf("%-10s %8d %10d %9d", band, rings, clients, largest)
	}
	return rows.Err()
}
