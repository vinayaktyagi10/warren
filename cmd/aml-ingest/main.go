// Command aml-ingest loads one variant of the IBM Anti-Money Laundering
// dataset and reports what arrived, including the ring labels.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/aml"
	"github.com/vinayaktyagi10/warren/internal/db"
)

func main() {
	dir := flag.String("data", "data/aml", "directory holding the AML files")
	prefix := flag.String("set", "HI-Small", "dataset variant, e.g. HI-Small or LI-Small")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := aml.Load(ctx, pool, *dir, *prefix); err != nil {
		log.Fatalf("load: %v", err)
	}
	if err := report(ctx, pool); err != nil {
		log.Fatalf("report: %v", err)
	}
}

func report(ctx context.Context, pool *pgxpool.Pool) error {
	var txns, laundering, inPattern, accounts, entities int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE is_laundering),
		       count(*) FILTER (WHERE pattern_id IS NOT NULL)
		FROM aml_transactions`).Scan(&txns, &laundering, &inPattern); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT entity_id) FROM aml_accounts`).Scan(&accounts, &entities); err != nil {
		return err
	}
	log.Printf("transactions=%d laundering=%d (%.4f%%) in_labelled_ring=%d accounts=%d entities=%d",
		txns, laundering, 100*float64(laundering)/float64(txns), inPattern, accounts, entities)

	rows, err := pool.Query(ctx, `
		SELECT p.typology, count(*) AS rings, sum(p.txn_count) AS txns,
		       min(p.txn_count) AS smallest, max(p.txn_count) AS largest
		FROM aml_patterns p GROUP BY p.typology ORDER BY rings DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	log.Printf("%-16s %6s %7s %9s %8s", "typology", "rings", "txns", "smallest", "largest")
	for rows.Next() {
		var typology string
		var rings, txns, smallest, largest int64
		if err := rows.Scan(&typology, &rings, &txns, &smallest, &largest); err != nil {
			return err
		}
		log.Printf("%-16s %6d %7d %9d %8d", typology, rings, txns, smallest, largest)
	}
	return rows.Err()
}
