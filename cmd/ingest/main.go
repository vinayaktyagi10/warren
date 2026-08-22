// Command ingest loads the IEEE-CIS Fraud Detection CSVs into Postgres.
//
// Rows are streamed through the Postgres COPY protocol rather than inserted one
// at a time: at 590k rows a per-row INSERT means 590k round trips and 590k
// plan cycles, and materialising the whole file first would cost well over a
// gigabyte of memory. The CopyFromSource implementations below read the CSV
// lazily, so memory stays flat regardless of file size.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vinayaktyagi10/warren/internal/db"
)

func main() {
	dataDir := flag.String("data", "data", "directory holding the IEEE-CIS CSVs")
	flag.Parse()

	ctx := context.Background()
	pool, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	log.Printf("applying schema")
	if err := db.ApplySchema(ctx, pool); err != nil {
		log.Fatalf("schema: %v", err)
	}

	if err := loadTransactions(ctx, pool, filepath.Join(*dataDir, "train_transaction.csv")); err != nil {
		log.Fatalf("transactions: %v", err)
	}
	if err := loadIdentities(ctx, pool, filepath.Join(*dataDir, "train_identity.csv")); err != nil {
		log.Fatalf("identities: %v", err)
	}
	if err := report(ctx, pool); err != nil {
		log.Fatalf("report: %v", err)
	}
}

// ---------------------------------------------------------------------------
// column layout
// ---------------------------------------------------------------------------

// Named transaction columns, in the order the transactions table expects them.
var txnNamedCols = []string{
	"TransactionID", "isFraud", "TransactionDT", "TransactionAmt", "ProductCD",
	"card1", "card2", "card3", "card4", "card5", "card6",
	"addr1", "addr2", "dist1", "dist2",
	"P_emaildomain", "R_emaildomain",
}

// identityNumericIdx records which id_NN columns hold numbers rather than
// labels. The split is interleaved rather than a clean prefix, and was derived
// by scanning every value in train_identity.csv, not assumed.
var identityNumericIdx = map[int]bool{
	1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true,
	9: true, 10: true, 11: true, 13: true, 14: true, 17: true, 18: true,
	19: true, 20: true, 21: true, 22: true, 24: true, 25: true, 26: true,
	32: true,
}

func identityColNames() []string {
	names := make([]string, 0, 38)
	for i := 1; i <= 38; i++ {
		names = append(names, fmt.Sprintf("id_%02d", i))
	}
	return names
}

// ---------------------------------------------------------------------------
// csv plumbing
// ---------------------------------------------------------------------------

// csvCursor wraps a CSV file with a header lookup so columns are addressed by
// name. Positional indexing would silently mis-load if the file layout shifted.
type csvCursor struct {
	file   *os.File
	reader *csv.Reader
	index  map[string]int
	rec    []string
}

func openCSV(path string) (*csvCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(f)
	r.ReuseRecord = true // safe: values are copied out before the next Read
	header, err := r.Read()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read header: %w", err)
	}
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[name] = i
	}
	return &csvCursor{file: f, reader: r, index: index}, nil
}

func (c *csvCursor) next() (bool, error) {
	rec, err := c.reader.Read()
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c.rec = rec
	return true, nil
}

// col returns the raw value for a column name, or "" when the column is absent.
func (c *csvCursor) col(name string) string {
	i, ok := c.index[name]
	if !ok || i >= len(c.rec) {
		return ""
	}
	return c.rec[i]
}

func (c *csvCursor) close() error { return c.file.Close() }

// ---------------------------------------------------------------------------
// value parsing
//
// An empty field means "missing" and must reach Postgres as NULL rather than
// as a zero, which the model would read as a real observation.
// ---------------------------------------------------------------------------

func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullFloat32(s string) (*float32, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return nil, err
	}
	f := float32(v)
	return &f, nil
}

// nullInt16 parses codes that the CSV writes in float form ("404.0" for 404).
func nullInt16(s string) (*int16, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	i := int16(v)
	return &i, nil
}

func nullInt32(s string) (*int32, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	i := int32(v)
	return &i, nil
}

// parseNumeric converts a decimal string to an exact NUMERIC. Going via float64
// would let 75.887 land as 75.88699999999999; the fractional part of the amount
// is a real fraud signal here, so it is carried as mantissa plus exponent.
func parseNumeric(s string) (pgtype.Numeric, error) {
	if s == "" {
		return pgtype.Numeric{}, errors.New("empty amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	intPart, fracPart, _ := strings.Cut(s, ".")
	digits := intPart + fracPart
	mant, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return pgtype.Numeric{}, fmt.Errorf("bad numeric %q", s)
	}
	if neg {
		mant.Neg(mant)
	}
	return pgtype.Numeric{Int: mant, Exp: int32(-len(fracPart)), Valid: true}, nil
}

// floatArray reads a contiguous block of numeric columns (C1..C14 style) into a
// slice that preserves NULL elements.
func floatArray(c *csvCursor, prefix string, n int) ([]*float32, error) {
	out := make([]*float32, n)
	for i := 1; i <= n; i++ {
		v, err := nullFloat32(c.col(fmt.Sprintf("%s%d", prefix, i)))
		if err != nil {
			return nil, fmt.Errorf("%s%d: %w", prefix, i, err)
		}
		out[i-1] = v
	}
	return out, nil
}

func textArray(c *csvCursor, prefix string, n int) []*string {
	out := make([]*string, n)
	for i := 1; i <= n; i++ {
		out[i-1] = nullText(c.col(fmt.Sprintf("%s%d", prefix, i)))
	}
	return out
}

// ---------------------------------------------------------------------------
// transactions
// ---------------------------------------------------------------------------

type txnSource struct {
	cur   *csvCursor
	err   error
	count int64
}

func (s *txnSource) Next() bool {
	ok, err := s.cur.next()
	if err != nil {
		s.err = err
		return false
	}
	if ok {
		s.count++
	}
	return ok
}

func (s *txnSource) Err() error { return s.err }

func (s *txnSource) Values() ([]any, error) {
	c := s.cur

	txnID, err := nullInt32(c.col("TransactionID"))
	if err != nil {
		return nil, fmt.Errorf("TransactionID: %w", err)
	}
	dt, err := nullInt32(c.col("TransactionDT"))
	if err != nil {
		return nil, fmt.Errorf("TransactionDT: %w", err)
	}
	amt, err := parseNumeric(c.col("TransactionAmt"))
	if err != nil {
		return nil, fmt.Errorf("TransactionAmt: %w", err)
	}
	card1, err := nullInt32(c.col("card1"))
	if err != nil {
		return nil, fmt.Errorf("card1: %w", err)
	}

	smallInts := make([]*int16, 0, 5)
	for _, name := range []string{"card2", "card3", "card5", "addr1", "addr2"} {
		v, err := nullInt16(c.col(name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		smallInts = append(smallInts, v)
	}

	dist1, err := nullFloat32(c.col("dist1"))
	if err != nil {
		return nil, fmt.Errorf("dist1: %w", err)
	}
	dist2, err := nullFloat32(c.col("dist2"))
	if err != nil {
		return nil, fmt.Errorf("dist2: %w", err)
	}

	cCounts, err := floatArray(c, "C", 14)
	if err != nil {
		return nil, err
	}
	dDeltas, err := floatArray(c, "D", 15)
	if err != nil {
		return nil, err
	}
	vFeatures, err := floatArray(c, "V", 339)
	if err != nil {
		return nil, err
	}

	return []any{
		txnID,
		c.col("isFraud") == "1",
		dt,
		amt,
		nullText(c.col("ProductCD")),
		card1,
		smallInts[0], // card2
		smallInts[1], // card3
		nullText(c.col("card4")),
		smallInts[2], // card5
		nullText(c.col("card6")),
		smallInts[3], // addr1
		smallInts[4], // addr2
		dist1,
		dist2,
		nullText(c.col("P_emaildomain")),
		nullText(c.col("R_emaildomain")),
		cCounts,
		dDeltas,
		textArray(c, "M", 9),
		vFeatures,
	}, nil
}

func loadTransactions(ctx context.Context, pool *pgxpool.Pool, path string) error {
	cur, err := openCSV(path)
	if err != nil {
		return err
	}
	defer cur.close()

	cols := []string{
		"transaction_id", "is_fraud", "transaction_dt", "transaction_amt",
		"product_cd", "card1", "card2", "card3", "card4", "card5", "card6",
		"addr1", "addr2", "dist1", "dist2", "p_emaildomain", "r_emaildomain",
		"c_counts", "d_deltas", "m_flags", "v_features",
	}

	log.Printf("loading transactions from %s", path)
	start := time.Now()
	src := &txnSource{cur: cur}
	n, err := pool.CopyFrom(ctx, pgx.Identifier{"transactions"}, cols, src)
	if err != nil {
		return fmt.Errorf("copy after %d rows: %w", src.count, err)
	}
	log.Printf("loaded %d transactions in %s", n, time.Since(start).Round(time.Millisecond))
	return nil
}

// ---------------------------------------------------------------------------
// identities
// ---------------------------------------------------------------------------

type identSource struct {
	cur *csvCursor
	err error
}

func (s *identSource) Next() bool {
	ok, err := s.cur.next()
	if err != nil {
		s.err = err
		return false
	}
	return ok
}

func (s *identSource) Err() error { return s.err }

func (s *identSource) Values() ([]any, error) {
	c := s.cur

	txnID, err := nullInt32(c.col("TransactionID"))
	if err != nil {
		return nil, fmt.Errorf("TransactionID: %w", err)
	}

	vals := make([]any, 0, 41)
	vals = append(vals, txnID)
	for i := 1; i <= 38; i++ {
		name := fmt.Sprintf("id_%02d", i)
		raw := c.col(name)
		if identityNumericIdx[i] {
			v, err := nullFloat32(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			vals = append(vals, v)
		} else {
			vals = append(vals, nullText(raw))
		}
	}
	vals = append(vals, nullText(c.col("DeviceType")), nullText(c.col("DeviceInfo")))
	return vals, nil
}

func loadIdentities(ctx context.Context, pool *pgxpool.Pool, path string) error {
	cur, err := openCSV(path)
	if err != nil {
		return err
	}
	defer cur.close()

	cols := append([]string{"transaction_id"}, identityColNames()...)
	cols = append(cols, "device_type", "device_info")

	log.Printf("loading identities from %s", path)
	start := time.Now()
	n, err := pool.CopyFrom(ctx, pgx.Identifier{"identities"}, cols, &identSource{cur: cur})
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	log.Printf("loaded %d identities in %s", n, time.Since(start).Round(time.Millisecond))
	return nil
}

// ---------------------------------------------------------------------------
// verification
// ---------------------------------------------------------------------------

// report prints the figures that prove the load is faithful to the source. The
// published IEEE-CIS training set is 590,540 rows at a 3.499% fraud rate; if
// these numbers drift, rows were dropped or mangled on the way in.
func report(ctx context.Context, pool *pgxpool.Pool) error {
	var txns, frauds, idents int64
	var minAmt, maxAmt pgtype.Numeric

	err := pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE is_fraud),
		       min(transaction_amt),
		       max(transaction_amt)
		FROM transactions`).Scan(&txns, &frauds, &minAmt, &maxAmt)
	if err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identities`).Scan(&idents); err != nil {
		return err
	}

	minS, _ := minAmt.MarshalJSON()
	maxS, _ := maxAmt.MarshalJSON()

	log.Printf("transactions=%d frauds=%d rate=%.4f%% identities=%d amt=[%s..%s]",
		txns, frauds, 100*float64(frauds)/float64(txns), idents, minS, maxS)
	return nil
}
