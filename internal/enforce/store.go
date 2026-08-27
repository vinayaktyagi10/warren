package enforce

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// GenesisHash starts the restriction chain. It differs from the audit log's so
// that a row cannot be lifted from one chain and replayed into the other.
const GenesisHash = "warren-restrictions-genesis"

const (
	EventImpose = "impose"
	EventLift   = "lift"
)

// Store persists the restriction ledger as a hash-chained event log.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply restriction schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Event is one recorded action on an account.
type Event struct {
	Seq         int64
	Event       string
	RecordedAt  time.Time
	Account     string
	Tier        Tier
	RingID      int
	DecisionSeq *int64
	Pass        string
	Reason      string
	ImposedAt   time.Time
	ExpiresAt   time.Time
	PrevHash    string
	Hash        string
}

// Active reports whether this event leaves the account held at t. Only an
// impose can, and only inside its own window.
func (e Event) Active(t time.Time) bool {
	return e.Event == EventImpose && !t.Before(e.ImposedAt) && t.Before(e.ExpiresAt)
}

// appendLock serialises appenders to the restriction chain. It is a different
// key from the decision log's so the two chains never wait on each other.
const appendLock = 0x7761_7272_656e_0002 // "warren" chain 2

// Record appends one event and returns its sequence number and hash.
//
// The advisory lock, held for the length of the transaction, is what stops two
// writers chaining onto the same predecessor and forking the ledger — the same
// discipline the decision log uses, for the same reason set out there.
func (s *Store) Record(ctx context.Context, e Event) (int64, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(appendLock)); err != nil {
		return 0, "", fmt.Errorf("take append lock: %w", err)
	}

	prevHash := GenesisHash
	err = tx.QueryRow(ctx,
		`SELECT hash FROM account_restrictions ORDER BY seq DESC LIMIT 1`).Scan(&prevHash)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		return 0, "", fmt.Errorf("read previous hash: %w", err)
	}

	var seq int64
	err = tx.QueryRow(ctx, `
		INSERT INTO account_restrictions
			(event, recorded_at, account, tier, ring_id, decision_seq, pass, reason,
			 imposed_at, expires_at, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'')
		RETURNING seq`,
		e.Event, e.RecordedAt, e.Account, string(e.Tier), e.RingID, e.DecisionSeq,
		e.Pass, e.Reason, e.ImposedAt, e.ExpiresAt, prevHash).Scan(&seq)
	if err != nil {
		return 0, "", fmt.Errorf("insert: %w", err)
	}

	e.Seq, e.PrevHash = seq, prevHash
	h := eventHash(e)
	if _, err := tx.Exec(ctx,
		`UPDATE account_restrictions SET hash = $1 WHERE seq = $2`, h, seq); err != nil {
		return 0, "", fmt.Errorf("set hash: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", err
	}
	return seq, h, nil
}

// Impose records a freeze or a watch, naming the decision that authorised it.
func (s *Store) Impose(ctx context.Context, r Restriction, account string, decisionSeq *int64) (int64, string, error) {
	return s.Record(ctx, Event{
		Event: EventImpose, RecordedAt: time.Now().UTC(),
		Account: account, Tier: r.Tier, RingID: r.RingID,
		DecisionSeq: decisionSeq, Pass: r.Pass, Reason: r.Reason,
		ImposedAt: r.Imposed, ExpiresAt: r.Expires,
	})
}

// Lift releases a hold early. It carries no decision_seq: a release performed by
// a person is authorised by that person, and recording a decision number there
// would attribute a human's judgement to the machine.
func (s *Store) Lift(ctx context.Context, account, reason string) (int64, string, error) {
	now := time.Now().UTC()
	return s.Record(ctx, Event{
		Event: EventLift, RecordedAt: now,
		Account: account, Tier: TierFrozen, Reason: reason,
		ImposedAt: now, ExpiresAt: now,
	})
}

// Events reads the whole ledger in order.
func (s *Store) Events(ctx context.Context) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, event, recorded_at, account, tier, ring_id, decision_seq,
		       pass, reason, imposed_at, expires_at, prev_hash, hash
		FROM account_restrictions ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var tier string
		if err := rows.Scan(&e.Seq, &e.Event, &e.RecordedAt, &e.Account, &tier,
			&e.RingID, &e.DecisionSeq, &e.Pass, &e.Reason,
			&e.ImposedAt, &e.ExpiresAt, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		e.Tier = Tier(tier)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Held is what an account's current state is: the fold of its events.
type Held struct {
	Event
	Expired bool
}

// ActiveAt folds the ledger into the set of accounts held at t.
//
// A lift cancels every impose on that account recorded before it, which is why
// events are replayed in order rather than the latest row being read. Where an
// account is imposed on twice the longer lease wins, matching the in-memory
// register exactly — the two must agree or the console and the replay would tell
// different stories about the same account.
func (s *Store) ActiveAt(ctx context.Context, t time.Time) ([]Held, error) {
	events, err := s.Events(ctx)
	if err != nil {
		return nil, err
	}
	current := make(map[string]Event)
	for _, e := range events {
		switch e.Event {
		case EventLift:
			delete(current, e.Account)
		case EventImpose:
			prev, ok := current[e.Account]
			switch {
			case !ok:
				current[e.Account] = e
			case prev.Tier.stops() && !e.Tier.stops():
				// a watch cannot weaken or extend a freeze
			case !prev.Tier.stops() && e.Tier.stops():
				current[e.Account] = e
			case e.ExpiresAt.After(prev.ExpiresAt):
				current[e.Account] = e
			}
		}
	}

	var out []Held
	for _, e := range current {
		out = append(out, Held{Event: e, Expired: !e.Active(t)})
	}
	return out, nil
}

// VerifyResult is the outcome of walking the restriction chain.
type VerifyResult struct {
	Events       int
	Valid        bool
	BrokenSeq    int64
	BrokenReason string
}

// Verify walks the chain and reports the first row that fails.
func (s *Store) Verify(ctx context.Context) (*VerifyResult, error) {
	events, err := s.Events(ctx)
	if err != nil {
		return nil, err
	}
	res := &VerifyResult{Events: len(events), Valid: true}
	prev := GenesisHash
	for _, e := range events {
		if e.PrevHash != prev {
			res.Valid = false
			res.BrokenSeq = e.Seq
			res.BrokenReason = fmt.Sprintf(
				"entry %d chains onto %s, but entry %d hashes to %s", e.Seq, short(e.PrevHash), e.Seq-1, short(prev))
			return res, nil
		}
		if want := eventHash(e); want != e.Hash {
			res.Valid = false
			res.BrokenSeq = e.Seq
			res.BrokenReason = fmt.Sprintf(
				"entry %d was altered after it was written: its content hashes to %s, not the recorded %s",
				e.Seq, short(want), short(e.Hash))
			return res, nil
		}
		prev = e.Hash
	}
	return res, nil
}

// stamp renders an instant for the digest at the precision the storage can
// actually hold.
//
// This was RFC3339Nano, and it meant the restriction chain had never once
// verified. Postgres keeps timestamptz to the microsecond, so the nanosecond
// digits written into the hash are gone by the time the row is read back and
// every entry recomputes to something else. The chain reported tampering on a
// ledger nobody had touched — a verifier that always cries wolf is worse than
// none, because the one real break is indistinguishable from the noise. The
// decision log had it right; this is the same format, for the same reason.
//
// The zeroes in the layout are load-bearing: RFC3339Nano drops trailing zeroes,
// so two instants a database round trip apart could render differently even at
// matching precision.
func stamp(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}

// eventHash commits to the action's content, its position, and its predecessor.
func eventHash(e Event) string {
	h := sha256.New()
	ds := int64(-1)
	if e.DecisionSeq != nil {
		ds = *e.DecisionSeq
	}
	fmt.Fprintf(h, "%d\n%s\n%s\n%s\n%s\n%d\n%d\n%s\n%s\n%s\n%s\n",
		e.Seq, e.PrevHash, e.Event, e.Account, e.Tier, e.RingID, ds, e.Pass, e.Reason,
		stamp(e.ImposedAt), stamp(e.ExpiresAt))
	return hex.EncodeToString(h.Sum(nil))
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
