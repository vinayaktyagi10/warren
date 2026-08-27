// Package dbtest gives a test its own private schema inside the development
// database.
//
// The two hash chains are the part of this system that is hardest to be sure
// about by reading, and until now the only thing that exercised them was a
// person clicking through the console. They cannot be tested without Postgres —
// the chaining depends on a locked read of the previous row inside the same
// transaction as the insert, which is exactly the behaviour an in-memory fake
// would paper over.
//
// So the tests use the real database, and take a schema of their own to do it
// in. search_path resolves the unqualified table names in each package's
// schema.sql into that schema, so a test run creates, fills and drops its own
// copy of every table and cannot touch loaded data or a demo's audit log.
package dbtest

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/db"
)

// Pool returns a pool scoped to a fresh schema, dropped when the test ends.
// Tests skip rather than fail when no database is reachable, so `go test ./...`
// still works on a machine that has not run `docker compose up`.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	schema := fmt.Sprintf("warren_test_%d_%d", time.Now().UnixNano()%1e9, rand.Int31())

	admin, err := pgxpool.New(ctx, db.URL())
	if err != nil {
		t.Skipf("no database configured (%v)", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("database at %s not reachable: %v", db.URL(), err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(db.URL())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	// Set on every connection rather than once, because a pool hands out more
	// than one and a session setting does not follow the pool.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open scoped pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		admin.Close()
	})
	return pool
}
