// Package audit records every decision in a hash-chained, append-only log.
//
// A risk system that can hold someone's money has to be able to answer, later
// and to someone unfriendly, three questions: what did you do, why did you do
// it, and has that record been altered since. The first two are content. The
// third is why the rows are chained.
package audit

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/agent"
)

//go:embed schema.sql
var schemaSQL string

// GenesisHash anchors the chain. The first real row commits to this constant,
// so a log that has had its earliest entries removed does not verify as a
// shorter but internally consistent chain.
const GenesisHash = "warren-genesis"

type Log struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, pool *pgxpool.Pool) (*Log, error) {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply audit schema: %w", err)
	}
	return &Log{pool: pool}, nil
}

// appendLock serialises appenders to this chain. The key is arbitrary but must
// stay fixed: it names the lock, not the data.
const appendLock = 0x7761_7272_656e_0001 // "warren" chain 1

// Record appends one decision and returns its sequence number and hash.
//
// An advisory lock held for the length of the transaction is what makes the
// chain a chain. Locking the last row instead is not enough, and the way it
// fails is quiet: a second writer blocked on `SELECT ... ORDER BY seq DESC
// LIMIT 1 FOR UPDATE` re-reads the row it was waiting on, not the newer row the
// first writer has since inserted, so both chain onto the same predecessor. It
// also locks nothing at all on an empty table, which is when the first two
// decisions of every run are written. Either way the log forks and then fails
// to verify, reporting tampering on a log nobody touched — the worst failure
// this component has, because it destroys trust in the one signal it exists to
// give.
func (l *Log) Record(ctx context.Context, ev agent.Evidence, a agent.Assessment) (int64, string, error) {
	evidenceJSON, err := json.Marshal(ev)
	if err != nil {
		return 0, "", fmt.Errorf("encode evidence: %w", err)
	}

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(appendLock)); err != nil {
		return 0, "", fmt.Errorf("take append lock: %w", err)
	}

	prevHash := GenesisHash
	err = tx.QueryRow(ctx,
		`SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&prevHash)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		return 0, "", fmt.Errorf("read previous hash: %w", err)
	}

	// A decision with nothing to adjust has a nil slice, which reaches Postgres
	// as NULL and fails the NOT NULL column. It shows up only on the path where
	// nothing intervened — the deterministic rule with no degradation and no
	// policy override — which is exactly the path least likely to be exercised
	// while a provider is reachable, and so the one that stayed broken longest.
	adjustments := a.Adjustments
	if adjustments == nil {
		adjustments = []string{}
	}

	var seq int64
	err = tx.QueryRow(ctx, `
		INSERT INTO audit_log (decided_at, ring_id, action, proposed, confidence,
		                       source, rationale, adjustments, evidence, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'')
		RETURNING seq`,
		a.DecidedAt, a.RingID, string(a.Action), string(a.Proposed), a.Confidence,
		a.Source, a.Rationale, adjustments, evidenceJSON, prevHash).Scan(&seq)
	if err != nil {
		return 0, "", fmt.Errorf("insert: %w", err)
	}

	// The hash is computed once the sequence number exists, so position in the
	// chain is part of what is committed to and rows cannot be reordered.
	h := entryHash(seq, prevHash, a, evidenceJSON)
	if _, err := tx.Exec(ctx, `UPDATE audit_log SET hash = $1 WHERE seq = $2`, h, seq); err != nil {
		return 0, "", fmt.Errorf("set hash: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", err
	}
	return seq, h, nil
}

// Count reports how many decisions the log holds. Callers use it to tell an
// empty log from one that already has history, which is not the same question
// as whether the log verifies.
func (l *Log) Count(ctx context.Context) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	return n, err
}

// entryHash commits to the decision's content, its position, and its
// predecessor.
func entryHash(seq int64, prevHash string, a agent.Assessment, evidenceJSON []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\n%s\n%s\n%d\n%s\n%s\n%.6f\n%s\n%s\n",
		seq, prevHash, a.DecidedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
		a.RingID, a.Action, a.Proposed, a.Confidence, a.Source, a.Rationale)
	for _, adj := range a.Adjustments {
		fmt.Fprintf(h, "%s\n", adj)
	}
	h.Write(evidenceJSON)
	return hex.EncodeToString(h.Sum(nil))
}

// Entry is one recorded decision.
type Entry struct {
	Seq         int64
	Action      string
	Proposed    string
	RingID      int
	Confidence  float64
	Source      string
	Rationale   string
	Adjustments []string
	Evidence    agent.Evidence
	PrevHash    string
	Hash        string
	DecidedAt   string
}

// VerifyResult is the outcome of walking the chain.
type VerifyResult struct {
	Entries int
	Valid   bool
	// Broken names the first row that fails, and how. Reporting only the first
	// is deliberate: every row after a break inherits it, so listing them all
	// would bury the one that matters.
	BrokenSeq    int64
	BrokenReason string
}

// Verify recomputes every hash and checks each row links to the one before it.
func (l *Log) Verify(ctx context.Context) (*VerifyResult, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT seq, decided_at, ring_id, action, proposed, confidence, source,
		       rationale, adjustments, evidence, prev_hash, hash
		FROM audit_log ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := &VerifyResult{Valid: true}
	expectedPrev := GenesisHash

	for rows.Next() {
		var (
			seq          int64
			ringID       int
			confidence   float64
			action       string
			proposed     string
			source       string
			rationale    string
			adjustments  []string
			evidenceJSON []byte
			prevHash     string
			hash         string
			decidedAt    time.Time
		)
		if err := rows.Scan(&seq, &decidedAt, &ringID, &action, &proposed, &confidence,
			&source, &rationale, &adjustments, &evidenceJSON, &prevHash, &hash); err != nil {
			return nil, err
		}
		res.Entries++
		if !res.Valid {
			continue
		}

		if prevHash != expectedPrev {
			res.Valid = false
			res.BrokenSeq = seq
			res.BrokenReason = fmt.Sprintf(
				"links to %s but the previous entry hashes to %s — a row was removed, reordered, or inserted",
				short(prevHash), short(expectedPrev))
			continue
		}

		var ev agent.Evidence
		if err := json.Unmarshal(evidenceJSON, &ev); err != nil {
			return nil, fmt.Errorf("seq %d: decode evidence: %w", seq, err)
		}
		a := agent.Assessment{
			RingID:      ringID,
			Action:      agent.Action(action),
			Proposed:    agent.Action(proposed),
			Confidence:  confidence,
			Source:      source,
			Rationale:   rationale,
			Adjustments: adjustments,
		}
		a.DecidedAt = decidedAt

		want := entryHash(seq, prevHash, a, canonicalJSON(ev))
		if want != hash {
			res.Valid = false
			res.BrokenSeq = seq
			res.BrokenReason = fmt.Sprintf(
				"content hashes to %s but the row carries %s — this entry was edited after it was written",
				short(want), short(hash))
			continue
		}
		expectedPrev = hash
	}
	return res, rows.Err()
}

// canonicalJSON re-encodes evidence the same way Record did, so verification
// compares like with like rather than against whatever key order the database
// returned.
func canonicalJSON(ev agent.Evidence) []byte {
	b, _ := json.Marshal(ev)
	return b
}

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12] + "…"
}
