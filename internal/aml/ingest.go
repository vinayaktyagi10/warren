// Package aml loads the IBM Anti-Money Laundering dataset: a transfer graph
// with an account-to-entity mapping and, crucially, a file that labels which
// transactions form which laundering ring.
package aml

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/csvutil"
)

//go:embed schema.sql
var schemaSQL string

// tsLayout matches the generator's timestamp format, "2022/09/01 00:20".
const tsLayout = "2006/01/02 15:04"

// Transaction file columns, addressed by position rather than by name: the
// header calls both the sending and the receiving column "Account", so a
// name-to-index map would silently collapse them onto one field.
const (
	colTS = iota
	colFromBank
	colFromAccount
	colToBank
	colToAccount
	colAmountReceived
	colReceivingCurrency
	colAmountPaid
	colPaymentCurrency
	colPaymentFormat
	colIsLaundering
	numTxnCols
)

// Account file columns.
const (
	acctBankName = iota
	acctBankID
	acctAccountNumber
	acctEntityID
	acctEntityName
)

// normalizeBank reconciles the two files' bank id formats. The transaction file
// zero-pads ("010", "03208") while the account file does not ("210", "331579"),
// so joining them raw matches nothing at all — every one of the 5,078,345
// transactions fails to find its account, and account-level features come back
// empty without anything raising an error. Both sides are normalised on load so
// the mismatch cannot be reintroduced by a later query.
func normalizeBank(s string) string {
	t := strings.TrimLeft(s, "0")
	if t == "" {
		return "0" // the id was all zeros, not absent
	}
	return t
}

// Load applies the schema and ingests transactions, accounts and ring labels.
func Load(ctx context.Context, pool *pgxpool.Pool, dir, prefix string) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply aml schema: %w", err)
	}
	if err := loadAccounts(ctx, pool, filepath.Join(dir, prefix+"_accounts.csv")); err != nil {
		return err
	}
	if err := loadTransactions(ctx, pool, filepath.Join(dir, prefix+"_Trans.csv")); err != nil {
		return err
	}
	return loadPatterns(ctx, pool, filepath.Join(dir, prefix+"_Patterns.txt"))
}

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

func loadAccounts(ctx context.Context, pool *pgxpool.Pool, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	if _, err := r.Read(); err != nil {
		return fmt.Errorf("accounts header: %w", err)
	}

	// The file lists an account once per (bank, account) pair, but repeats are
	// harmless to guard against and would otherwise abort the load on the
	// primary key.
	seen := make(map[[2]string]bool)
	var rows [][]any
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("accounts: %w", err)
		}
		key := [2]string{normalizeBank(rec[acctBankID]), rec[acctAccountNumber]}
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, []any{
			key[0], key[1],
			csvutil.NullText(rec[acctBankName]),
			csvutil.NullText(rec[acctEntityID]),
			csvutil.NullText(rec[acctEntityName]),
		})
	}

	start := time.Now()
	n, err := pool.CopyFrom(ctx, pgx.Identifier{"aml_accounts"},
		[]string{"bank_id", "account_number", "bank_name", "entity_id", "entity_name"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("copy accounts: %w", err)
	}
	log.Printf("loaded %d accounts in %s", n, time.Since(start).Round(time.Millisecond))
	return nil
}

// ---------------------------------------------------------------------------
// transactions
// ---------------------------------------------------------------------------

// txnSource streams the transaction file into COPY, assigning sequential ids.
type txnSource struct {
	reader *csv.Reader
	rec    []string
	next   int32
	err    error
}

func (s *txnSource) Next() bool {
	rec, err := s.reader.Read()
	if errors.Is(err, io.EOF) {
		return false
	}
	if err != nil {
		s.err = err
		return false
	}
	if len(rec) < numTxnCols {
		s.err = fmt.Errorf("row %d: got %d columns, want %d", s.next+1, len(rec), numTxnCols)
		return false
	}
	s.rec = rec
	s.next++
	return true
}

func (s *txnSource) Err() error { return s.err }

func (s *txnSource) Values() ([]any, error) {
	ts, err := time.Parse(tsLayout, s.rec[colTS])
	if err != nil {
		return nil, fmt.Errorf("row %d timestamp: %w", s.next, err)
	}
	recv, err := csvutil.ParseNumeric(s.rec[colAmountReceived])
	if err != nil {
		return nil, fmt.Errorf("row %d amount received: %w", s.next, err)
	}
	paid, err := csvutil.ParseNumeric(s.rec[colAmountPaid])
	if err != nil {
		return nil, fmt.Errorf("row %d amount paid: %w", s.next, err)
	}
	return []any{
		s.next,
		ts,
		normalizeBank(s.rec[colFromBank]), s.rec[colFromAccount],
		normalizeBank(s.rec[colToBank]), s.rec[colToAccount],
		recv, s.rec[colReceivingCurrency],
		paid, s.rec[colPaymentCurrency],
		s.rec[colPaymentFormat],
		s.rec[colIsLaundering] == "1",
	}, nil
}

func loadTransactions(ctx context.Context, pool *pgxpool.Pool, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.ReuseRecord = true
	r.FieldsPerRecord = numTxnCols
	if _, err := r.Read(); err != nil {
		return fmt.Errorf("transactions header: %w", err)
	}

	log.Printf("loading transactions from %s", path)
	start := time.Now()
	src := &txnSource{reader: r}
	n, err := pool.CopyFrom(ctx, pgx.Identifier{"aml_transactions"},
		[]string{
			"txn_id", "ts", "from_bank", "from_account", "to_bank", "to_account",
			"amount_received", "receiving_currency", "amount_paid",
			"payment_currency", "payment_format", "is_laundering",
		}, src)
	if err != nil {
		return fmt.Errorf("copy transactions after %d rows: %w", src.next, err)
	}
	log.Printf("loaded %d transactions in %s", n, time.Since(start).Round(time.Millisecond))
	return nil
}

// ---------------------------------------------------------------------------
// patterns
// ---------------------------------------------------------------------------

// The patterns file is not CSV. It is blocks of transaction lines fenced by
// markers naming the laundering shape:
//
//	BEGIN LAUNDERING ATTEMPT - FAN-OUT:  Max 16-degree Fan-Out
//	2022/09/01 00:06,021174,800737690,012,80011F990,2848.96,Euro,...,1
//	END LAUNDERING ATTEMPT - FAN-OUT
const (
	beginMarker = "BEGIN LAUNDERING ATTEMPT - "
	endMarker   = "END LAUNDERING ATTEMPT"
)

func loadPatterns(ctx context.Context, pool *pgxpool.Pool, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var patterns [][]any
	var members [][]any
	patternID := 0
	inBlock := false
	count := 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue

		case strings.HasPrefix(line, beginMarker):
			// "FAN-OUT:  Max 16-degree Fan-Out" -> typology, description.
			rest := strings.TrimPrefix(line, beginMarker)
			typology, description, _ := strings.Cut(rest, ":")
			patternID++
			inBlock = true
			count = 0
			patterns = append(patterns, []any{
				patternID,
				strings.TrimSpace(typology),
				strings.TrimSpace(description),
				0, // txn_count, filled in once the block closes
			})

		case strings.HasPrefix(line, endMarker):
			if inBlock {
				patterns[len(patterns)-1][3] = count
			}
			inBlock = false

		case inBlock:
			rec := strings.Split(line, ",")
			if len(rec) < numTxnCols {
				return fmt.Errorf("pattern %d: got %d columns, want %d", patternID, len(rec), numTxnCols)
			}
			ts, err := time.Parse(tsLayout, rec[colTS])
			if err != nil {
				return fmt.Errorf("pattern %d timestamp: %w", patternID, err)
			}
			paid, err := csvutil.ParseNumeric(rec[colAmountPaid])
			if err != nil {
				return fmt.Errorf("pattern %d amount: %w", patternID, err)
			}
			count++
			members = append(members, []any{
				patternID, ts,
				normalizeBank(rec[colFromBank]), rec[colFromAccount],
				normalizeBank(rec[colToBank]), rec[colToAccount],
				paid, rec[colPaymentCurrency],
				nil, // matched_txn_id, resolved below
			})
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan patterns: %w", err)
	}

	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"aml_patterns"},
		[]string{"pattern_id", "typology", "description", "txn_count"},
		pgx.CopyFromRows(patterns)); err != nil {
		return fmt.Errorf("copy patterns: %w", err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"aml_pattern_txns"},
		[]string{
			"pattern_id", "ts", "from_bank", "from_account", "to_bank",
			"to_account", "amount_paid", "payment_currency", "matched_txn_id",
		}, pgx.CopyFromRows(members)); err != nil {
		return fmt.Errorf("copy pattern members: %w", err)
	}
	log.Printf("loaded %d labelled rings covering %d transactions", len(patterns), len(members))

	return linkPatterns(ctx, pool)
}

// linkPatterns resolves each quoted pattern line back to a transaction row.
// The patterns file repeats the transaction rather than referencing it, so the
// join is on the full tuple; the match rate is reported because a silent
// failure here would leave the ground truth attached to nothing.
func linkPatterns(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		UPDATE aml_pattern_txns p SET matched_txn_id = t.txn_id
		FROM aml_transactions t
		WHERE t.ts = p.ts
		  AND t.from_bank = p.from_bank AND t.from_account = p.from_account
		  AND t.to_bank   = p.to_bank   AND t.to_account   = p.to_account
		  AND t.amount_paid = p.amount_paid
		  AND t.payment_currency = p.payment_currency`); err != nil {
		return fmt.Errorf("match pattern transactions: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE aml_transactions t SET pattern_id = p.pattern_id
		FROM aml_pattern_txns p WHERE p.matched_txn_id = t.txn_id`); err != nil {
		return fmt.Errorf("stamp pattern ids: %w", err)
	}

	var total, matched int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(matched_txn_id) FROM aml_pattern_txns`).Scan(&total, &matched); err != nil {
		return err
	}
	log.Printf("pattern transactions matched to ledger: %d/%d (%.2f%%)",
		matched, total, 100*float64(matched)/float64(total))
	if matched < total {
		log.Printf("warning: %d pattern rows did not match a transaction", total-matched)
	}
	return nil
}
