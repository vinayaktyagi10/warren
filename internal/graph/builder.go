// Package graph resolves transactions into client entities and links those
// clients into candidate abuse rings.
//
// The dataset carries no ring labels, only a per-transaction fraud flag, so ring
// membership is reconstructed rather than read. That happens in two stages,
// because they answer different questions:
//
//	stage 1  which transactions are the same account?      -> clients
//	stage 2  which accounts are the same operator?         -> rings
//
// Collapsing the two would be a mistake: one person legitimately holding one
// card across many purchases is not a ring, and treating it as one would bury
// the real signal under ordinary repeat custom.
package graph

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// clientKeySQL derives a stable account identifier for each transaction.
//
// The preferred form exploits D1, "days since this card was first seen": the
// transaction's own day minus D1 recovers the day the account opened, which is a
// constant for the life of that card. Pairing it with card1 and addr1 separates
// accounts far more sharply than card fields alone — 217k groups averaging 2.7
// transactions, against 43k groups whose largest holds 9,900 transactions.
//
// When any of those three fields is missing — 11.3% of transactions — the
// account is simply not identifiable, and each such transaction becomes its own
// client rather than being grouped on whatever fields remain.
//
// That restraint is deliberate. An earlier version fell back to the raw card
// tuple, which looked productive and was not: card1 holds only ~18k distinct
// values across 590k transactions, so with the address missing it pooled
// unrelated people into "accounts" averaging 224 transactions, and those
// pooled pseudo-accounts then linked into one 236-member "ring" carrying
// essentially all of the apparent signal. Enrichment measured against an
// artefact of the key is not enrichment.
//
// The loss is real and worth stating: those 66,794 transactions hold 7,768
// frauds, an 11.6% rate against a 3.5% base. Missing identity data is itself
// predictive, but that belongs to the per-transaction model as a feature, not
// to the ring detector as a fabricated group.
const clientKeySQL = `
SELECT transaction_id,
       CASE WHEN d_deltas[1] IS NOT NULL AND addr1 IS NOT NULL AND card1 IS NOT NULL
            THEN 'uid:' || card1 || ':' || addr1 || ':' ||
                 (transaction_dt/86400 - d_deltas[1]::int)
            ELSE 'txn:' || transaction_id
       END
FROM transactions`

// A LinkRule proposes edges between clients that share some value.
//
// MaxFanout is the first line of defence. A value shared by a handful of
// accounts is evidence of one operator; the same value shared by thousands is
// demographics. In this data 143 device fingerprints cover 44,594 clients —
// generic Windows/Chrome configurations — so without a cap the first rule would
// fuse a third of the dataset into one meaningless component. Every rule
// therefore declares how much sharing it tolerates before it stops believing
// itself.
type LinkRule struct {
	Name      string
	Rationale string
	MaxFanout int
	// SQL must yield (client_id, value) pairs.
	SQL string
}

// DefaultRules links accounts through shared physical infrastructure. Shared
// demographics — an email provider, a card issuer, a country — are deliberately
// absent: they describe populations, not operators.
var DefaultRules = []LinkRule{
	{
		Name:      "device_fingerprint",
		Rationale: "distinct accounts transacting from one device build",
		MaxFanout: 20,
		SQL: `SELECT DISTINCT tc.client_id,
		             i.device_info || '|' || coalesce(i.id_30,'') || '|' ||
		             coalesce(i.id_31,'') || '|' || coalesce(i.id_33,'')
		      FROM transaction_clients tc
		      JOIN identities i USING (transaction_id)
		      WHERE i.device_info IS NOT NULL AND i.id_31 IS NOT NULL`,
	},
	{
		Name:      "shared_card",
		Rationale: "one card number presented under several billing identities",
		MaxFanout: 10,
		SQL: `SELECT DISTINCT tc.client_id, 'card1:' || t.card1
		      FROM transaction_clients tc
		      JOIN transactions t USING (transaction_id)
		      WHERE t.card1 IS NOT NULL`,
	},
}

// Config controls how aggressively accounts are joined.
type Config struct {
	Rules []LinkRule

	// MinCorroboration is how many independent shared values must join two
	// accounts before the link is believed.
	//
	// A fanout cap alone is not enough. Links are transitive, so accounts chain:
	// A shares a device with B, B shares a card with C, C shares a different
	// device with D. At 1 every individual link here looked reasonable — average
	// fanout under 7 — yet 2,219 of them chained 8,930 accounts into a single
	// "ring", with the next largest at 115. Requiring a second, independent
	// shared value breaks those chains, because a chance overlap rarely repeats
	// across two different attributes for the same pair of accounts.
	MinCorroboration int
}

func DefaultConfig() Config {
	return Config{Rules: DefaultRules, MinCorroboration: 2}
}

// RuleStat records what a rule did, including what it refused to do. Rejected
// and unused counts are reported rather than hidden: they are the linking the
// caps and the corroboration threshold threw away, and a rule that contributes
// almost nothing is miscalibrated.
type RuleStat struct {
	Name           string
	GroupsAccepted int // within the fanout cap
	GroupsRejected int // over the fanout cap, discarded as demographic
	GroupsUsed     int // contributed to at least one corroborated link
}

// Result summarises one pass of ring detection.
type Result struct {
	Clients       int
	Rings         int
	PairsProposed int // candidate account pairs sharing at least one value
	PairsLinked   int // pairs that cleared the corroboration threshold
	Stats         []RuleStat
}

// candidate is one accepted (rule, value) group: a set of accounts sharing a
// value that was specific enough to be worth considering.
type candidate struct {
	rule    int
	value   string
	members []int32
}

// pairKey packs an unordered account pair into one comparable integer, so
// evidence can be accumulated per pair without allocating a key per link.
func pairKey(a, b int32) uint64 {
	if a > b {
		a, b = b, a
	}
	return uint64(uint32(a))<<32 | uint64(uint32(b))
}

func unpairKey(k uint64) (int32, int32) {
	return int32(uint32(k >> 32)), int32(uint32(k))
}

// Build resolves clients, links them into rings, and persists both along with
// the evidence for every link.
func Build(ctx context.Context, pool *pgxpool.Pool, cfg Config) (*Result, error) {
	if cfg.MinCorroboration < 1 {
		cfg.MinCorroboration = 1
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply graph schema: %w", err)
	}

	clientIDs, txnClients, err := resolveClients(ctx, pool)
	if err != nil {
		return nil, err
	}
	log.Printf("resolved %d transactions into %d clients", len(txnClients), len(clientIDs))

	// Clients are persisted before linking so the rules can express themselves
	// as ordinary joins in SQL rather than reimplementing the mapping in Go.
	if err := writeClients(ctx, pool, clientIDs, txnClients); err != nil {
		return nil, err
	}

	candidates, stats, err := gatherCandidates(ctx, pool, cfg.Rules)
	if err != nil {
		return nil, err
	}

	// Accumulate, for every pair of accounts, which groups vouch for them.
	pairs := make(map[uint64][]int32)
	for gi, c := range candidates {
		for i := 0; i < len(c.members); i++ {
			for j := i + 1; j < len(c.members); j++ {
				k := pairKey(c.members[i], c.members[j])
				pairs[k] = append(pairs[k], int32(gi))
			}
		}
	}

	// Union in a deterministic order so a rerun on unchanged data reproduces the
	// same rings; an audit trail that renumbers itself is not an audit trail.
	keys := make([]uint64, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	uf := NewUnionFind(len(clientIDs))
	usedGroups := make(map[int32]bool)
	linked := 0
	for _, k := range keys {
		vouchers := pairs[k]
		if len(vouchers) < cfg.MinCorroboration {
			continue
		}
		linked++
		a, b := unpairKey(k)
		uf.Union(a, b)
		for _, gi := range vouchers {
			usedGroups[gi] = true
		}
	}
	for gi := range usedGroups {
		stats[candidates[gi].rule].GroupsUsed++
	}
	for _, s := range stats {
		log.Printf("rule %-20s accepted=%d rejected=%d used=%d",
			s.Name, s.GroupsAccepted, s.GroupsRejected, s.GroupsUsed)
	}
	log.Printf("pairs proposed=%d linked=%d (min corroboration %d)",
		len(pairs), linked, cfg.MinCorroboration)

	ringOf := assignRingIDs(uf)
	if err := writeRings(ctx, pool, uf, ringOf); err != nil {
		return nil, err
	}
	if err := writeEvidence(ctx, pool, uf, ringOf, candidates, usedGroups, cfg.Rules); err != nil {
		return nil, err
	}

	rings := 0
	seen := make(map[int32]bool)
	for _, ring := range ringOf {
		if !seen[ring] {
			seen[ring] = true
			rings++
		}
	}

	return &Result{
		Clients:       len(clientIDs),
		Rings:         rings,
		PairsProposed: len(pairs),
		PairsLinked:   linked,
		Stats:         stats,
	}, nil
}

// gatherCandidates runs every rule and collects the groups that survive its
// fanout cap, along with a count of those that did not.
func gatherCandidates(ctx context.Context, pool *pgxpool.Pool, rules []LinkRule) ([]candidate, []RuleStat, error) {
	var candidates []candidate
	stats := make([]RuleStat, len(rules))

	for ri, rule := range rules {
		stats[ri].Name = rule.Name

		rows, err := pool.Query(ctx, fmt.Sprintf(`
			SELECT value, array_agg(DISTINCT client_id) AS members
			FROM (%s) AS src(client_id, value)
			GROUP BY value
			HAVING count(DISTINCT client_id) BETWEEN 2 AND %d`, rule.SQL, rule.MaxFanout))
		if err != nil {
			return nil, nil, fmt.Errorf("rule %s: %w", rule.Name, err)
		}
		for rows.Next() {
			var value string
			var members []int32
			if err := rows.Scan(&value, &members); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("rule %s: %w", rule.Name, err)
			}
			stats[ri].GroupsAccepted++
			candidates = append(candidates, candidate{rule: ri, value: value, members: members})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, fmt.Errorf("rule %s: %w", rule.Name, err)
		}

		// Count what the cap discarded, so the trade-off stays visible.
		err = pool.QueryRow(ctx, fmt.Sprintf(`
			SELECT count(*) FROM (
			  SELECT value FROM (%s) AS src(client_id, value)
			  GROUP BY value HAVING count(DISTINCT client_id) > %d
			) AS over_cap`, rule.SQL, rule.MaxFanout)).Scan(&stats[ri].GroupsRejected)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %s rejected count: %w", rule.Name, err)
		}
	}
	return candidates, stats, nil
}

// assignRingIDs numbers the multi-account components. Numbering follows each
// ring's lowest account id, which does not depend on the order links were
// applied, so ring 42 refers to the same accounts on every run.
func assignRingIDs(uf *UnionFind) map[int32]int32 {
	components := uf.Components()

	roots := make([]int32, 0, len(components))
	minMember := make(map[int32]int32, len(components))
	for root, members := range components {
		lo := members[0]
		for _, m := range members {
			if m < lo {
				lo = m
			}
		}
		minMember[root] = lo
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool { return minMember[roots[i]] < minMember[roots[j]] })

	ringOf := make(map[int32]int32, len(components))
	for i, root := range roots {
		ringOf[root] = int32(i + 1)
	}
	return ringOf
}

// resolveClients assigns every transaction to a dense client index.
func resolveClients(ctx context.Context, pool *pgxpool.Pool) (map[string]int32, map[int32]int32, error) {
	rows, err := pool.Query(ctx, clientKeySQL)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve clients: %w", err)
	}
	defer rows.Close()

	clientIDs := make(map[string]int32)
	txnClients := make(map[int32]int32)
	for rows.Next() {
		var txnID int32
		var key string
		if err := rows.Scan(&txnID, &key); err != nil {
			return nil, nil, err
		}
		id, ok := clientIDs[key]
		if !ok {
			id = int32(len(clientIDs))
			clientIDs[key] = id
		}
		txnClients[txnID] = id
	}
	return clientIDs, txnClients, rows.Err()
}

func writeClients(ctx context.Context, pool *pgxpool.Pool, clientIDs map[string]int32, txnClients map[int32]int32) error {
	clients := make([][]any, 0, len(clientIDs))
	for key, id := range clientIDs {
		clients = append(clients, []any{id, key})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"clients"},
		[]string{"client_id", "client_key"}, pgx.CopyFromRows(clients)); err != nil {
		return fmt.Errorf("write clients: %w", err)
	}

	links := make([][]any, 0, len(txnClients))
	for txnID, clientID := range txnClients {
		links = append(links, []any{txnID, clientID})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"transaction_clients"},
		[]string{"transaction_id", "client_id"}, pgx.CopyFromRows(links)); err != nil {
		return fmt.Errorf("write transaction_clients: %w", err)
	}
	return nil
}

// writeRings stamps ring membership onto clients. The ids go through a temp
// table and a single set-based UPDATE; issuing a quarter of a million
// individual updates would dominate the runtime of the whole pass.
func writeRings(ctx context.Context, pool *pgxpool.Pool, uf *UnionFind, ringOf map[int32]int32) error {
	// The temp table and the update must share one transaction: ON COMMIT DROP
	// would otherwise discard the table the moment the CREATE auto-committed.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`CREATE TEMP TABLE ring_assign (client_id INTEGER, ring_id INTEGER) ON COMMIT DROP`); err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	assigns := make([][]any, 0, len(ringOf))
	for i := 0; i < uf.Len(); i++ {
		if ring, ok := ringOf[uf.Find(int32(i))]; ok {
			assigns = append(assigns, []any{int32(i), ring})
		}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"ring_assign"},
		[]string{"client_id", "ring_id"}, pgx.CopyFromRows(assigns)); err != nil {
		return fmt.Errorf("write ring assignments: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE clients c SET ring_id = a.ring_id
		FROM ring_assign a WHERE a.client_id = c.client_id`); err != nil {
		return fmt.Errorf("apply ring assignments: %w", err)
	}
	return tx.Commit(ctx)
}

// writeEvidence records the shared values behind each ring, so a flagged ring
// can be justified down to the attribute that produced it.
func writeEvidence(ctx context.Context, pool *pgxpool.Pool, uf *UnionFind, ringOf map[int32]int32,
	candidates []candidate, usedGroups map[int32]bool, rules []LinkRule) error {

	rows := make([][]any, 0, len(usedGroups))
	for gi := range usedGroups {
		c := candidates[gi]
		ring, ok := ringOf[uf.Find(c.members[0])]
		if !ok {
			continue
		}
		rows = append(rows, []any{ring, rules[c.rule].Name, c.value, len(c.members)})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"ring_links"},
		[]string{"ring_id", "rule", "link_value", "client_count"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("write ring links: %w", err)
	}
	return nil
}
