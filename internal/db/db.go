// Package db owns the Postgres connection and the schema WARREN loads data into.
package db

import (
	"context"
	_ "embed"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// DefaultURL is the local development database created by docker-compose.yml.
const DefaultURL = "postgres://warren:warren@localhost:5432/warren"

// URL returns the database URL, preferring DATABASE_URL from the environment so
// the same binaries run against a different database without a rebuild.
func URL() string {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u
	}
	return DefaultURL
}

// Connect opens a pooled connection and verifies the database is reachable,
// so callers fail immediately on a bad URL rather than on first query.
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, URL())
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping %s: %w", URL(), err)
	}
	return pool, nil
}

// ApplySchema creates the tables and indexes, dropping any that already exist.
// Ingest is therefore idempotent: re-running it rebuilds from the CSVs rather
// than appending to a half-loaded table.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}
