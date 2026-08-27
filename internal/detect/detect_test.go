package detect

import (
	"math"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/score"
)

var epoch = time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

func at(h float64) time.Time {
	return epoch.Add(time.Duration(h * float64(time.Hour)))
}

// tx builds a transfer. Amounts default to something above every threshold the
// tests care about so a test that is about shape is not accidentally about size.
func tx(id int32, hours float64, from, to int32, amount float64) Txn {
	return Txn{ID: id, TS: at(hours), From: from, To: to, Amount: amount}
}

func testConfig() Config {
	c := DefaultConfig()
	c.PaymentFormats = nil
	return c
}

func ledgerOf(txns ...Txn) *Ledger {
	led := &Ledger{Txns: txns}
	max := int32(0)
	for _, t := range txns {
		if t.From > max {
			max = t.From
		}
		if t.To > max {
			max = t.To
		}
	}
	for i := int32(0); i <= max; i++ {
		led.Accounts = append(led.Accounts, string(rune('A'+i%26)))
	}
	return led
}

// --------------------------------------------------------------------------
// the feature vector is a contract with the model
// --------------------------------------------------------------------------

// The ranker reports coefficients against score.RingFeatureNames by position.
// If the two ever drift apart, every explanation the console prints is silently
// mislabelled — a coefficient attributed to the wrong feature — and nothing
// else in the system would notice.
func TestVectorMatchesTheDeclaredFeatureNames(t *testing.T) {
	v := Features{}.Vector()
	if len(v) != len(score.RingFeatureNames) {
		t.Fatalf("vector has %d entries, score.RingFeatureNames declares %d",
			len(v), len(score.RingFeatureNames))
	}
}

func TestVectorLogScalesAmountsAndCounts(t *testing.T) {
	f := Features{
		TotalAmount: 999, MaxAmount: 99, MeanAmount: 9,
		Txns: 9, Accounts: 3,
		Conservation: 0.5, PassThroughRatio: 0.25, Density: 3, SpanHours: 12,
	}
	v := f.Vector()
	want := []float64{
		math.Log1p(999), math.Log1p(99), math.Log1p(9),
		0.5, 0.25,
		math.Log1p(9), math.Log1p(3),
		3, 12,
	}
	for i := range want {
		if math.Abs(v[i]-want[i]) > 1e-9 {
			t.Errorf("%s: got %g want %g", score.RingFeatureNames[i], v[i], want[i])
		}
	}
}

// --------------------------------------------------------------------------
// features: conservation is the model's strongest signal, so pin its meaning
// --------------------------------------------------------------------------

func featuresOf(group []Txn) Features {
	senders, receivers, accounts := roles(group)
	return features(group, senders, receivers, accounts)
}

func roles(group []Txn) (senders, receivers, accounts map[int32]bool) {
	senders = map[int32]bool{}
	receivers = map[int32]bool{}
	accounts = map[int32]bool{}
	for _, t := range group {
		senders[t.From] = true
		receivers[t.To] = true
		accounts[t.From] = true
		accounts[t.To] = true
	}
	return
}

// A mule forwards what it receives. Perfect forwarding is conservation 1.
func TestConservationIsOneWhenAnIntermediaryForwardsEverything(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 3, 1000),
	})
	if math.Abs(f.Conservation-1) > 1e-9 {
		t.Errorf("conservation %g, want 1", f.Conservation)
	}
	// One of three accounts both sends and receives.
	if math.Abs(f.PassThroughRatio-1.0/3.0) > 1e-9 {
		t.Errorf("pass-through %g, want 1/3", f.PassThroughRatio)
	}
}

// Conservation is deliberately symmetric: forwarding 900 of 1000 and forwarding
// 1000 of 900 are equally close to "passed it straight on". Without the
// inversion an account that tops up slightly would score above 1 and swamp the
// mean, and the feature would measure magnitude rather than closeness.
func TestConservationIsSymmetricAboutForwardingExactly(t *testing.T) {
	under := featuresOf([]Txn{tx(1, 0, 1, 2, 1000), tx(2, 1, 2, 3, 900)})
	over := featuresOf([]Txn{tx(1, 0, 1, 2, 900), tx(2, 1, 2, 3, 1000)})
	if math.Abs(under.Conservation-over.Conservation) > 1e-9 {
		t.Errorf("asymmetric: %g vs %g", under.Conservation, over.Conservation)
	}
	if under.Conservation > 1 {
		t.Errorf("conservation %g exceeds 1", under.Conservation)
	}
}

// An account that only receives is not an intermediary and must not be averaged
// into conservation — a terminal account has no outflow, and counting it as a
// zero would drag every group's conservation toward zero in proportion to how
// many leaves it has.
func TestConservationIgnoresAccountsThatOnlyReceive(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 3, 1000),
		tx(3, 2, 2, 4, 0.0001), // 4 is a leaf; 2 stays a near-perfect forwarder
	})
	if f.Conservation < 0.99 {
		t.Errorf("conservation %g dragged down by a leaf account", f.Conservation)
	}
}

func TestSpanAndDensity(t *testing.T) {
	f := featuresOf([]Txn{
		tx(1, 0, 1, 2, 100),
		tx(2, 6, 2, 3, 100),
		tx(3, 30, 3, 1, 100),
	})
	if f.SpanHours != 30 {
		t.Errorf("span %g, want 30", f.SpanHours)
	}
	if f.Density != 1 {
		t.Errorf("density %g, want 3 txns / 3 accounts = 1", f.Density)
	}
	if f.MaxAmount != 100 || f.TotalAmount != 300 || f.MeanAmount != 100 {
		t.Errorf("amounts wrong: %+v", f)
	}
}

// --------------------------------------------------------------------------
// classify: the shape names an analyst reads off the alert
// --------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		txns []Txn
		want string
	}{
		{"one sender feeding many", []Txn{
			tx(1, 0, 1, 2, 100), tx(2, 1, 1, 3, 100), tx(3, 2, 1, 4, 100)}, "FAN-OUT"},
		{"many senders into one", []Txn{
			tx(1, 0, 2, 1, 100), tx(2, 1, 3, 1, 100), tx(3, 2, 4, 1, 100)}, "FAN-IN"},
		{"every account does both", []Txn{
			tx(1, 0, 1, 2, 100), tx(2, 1, 2, 3, 100), tx(3, 2, 3, 1, 100)}, "CYCLE"},
		{"two clean sides", []Txn{
			tx(1, 0, 1, 3, 100), tx(2, 1, 1, 4, 100),
			tx(3, 2, 2, 3, 100), tx(4, 3, 2, 4, 100)}, "BIPARTITE"},
		{"neither, and not guessed at", []Txn{
			tx(1, 0, 1, 2, 100), tx(2, 1, 2, 3, 100),
			tx(3, 2, 4, 3, 100), tx(4, 3, 1, 5, 100)}, "MIXED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, r, a := roles(c.txns)
			if got := classify(s, r, a); got != c.want {
				t.Errorf("classify = %q, want %q", got, c.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// windowing
// --------------------------------------------------------------------------

// Windows overlap by design, so the same group is found several times. Reporting
// each sighting would multiply the alert queue by window/stride and make every
// precision number meaningless.
func TestOverlappingWindowsReportAGroupOnce(t *testing.T) {
	led := ledgerOf(
		tx(1, 0, 1, 2, 1000),
		tx(2, 1, 2, 3, 1000),
		tx(3, 2, 3, 4, 1000),
	)
	cfg := testConfig()
	cfg.WindowHours, cfg.StrideHours = 72, 24

	got := Detect(led, cfg)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 — the same group seen in %d overlapping windows",
			len(got), cfg.WindowHours/cfg.StrideHours)
	}
	if got[0].Window != at(0) {
		t.Errorf("kept the sighting at %v, want the earliest at %v", got[0].Window, at(0))
	}
}

// Windowing exists to keep rings apart that merely share an account months
// later. Over the whole ledger union-find would fuse these into one group.
func TestWindowingKeepsDistantRingsApart(t *testing.T) {
	led := ledgerOf(
		tx(1, 0, 1, 2, 1000), tx(2, 1, 2, 3, 1000), tx(3, 2, 3, 1, 1000),
		// same account 1, three weeks later
		tx(4, 500, 1, 4, 1000), tx(5, 501, 4, 5, 1000), tx(6, 502, 5, 1, 1000),
	)
	cfg := testConfig()
	got := Detect(led, cfg)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 separate rings", len(got))
	}
	for _, c := range got {
		if len(c.Accounts) != 3 {
			t.Errorf("candidate has %d accounts, want 3 — the windows fused", len(c.Accounts))
		}
	}
}

// A hub with more counterparties than MaxAccountDegree is dropped from linking
// entirely: it is the mechanism that fused 2,219 links into one 8,930-account
// "ring" the first time round.
func TestOverConnectedAccountsDoNotFuseGroups(t *testing.T) {
	var txns []Txn
	id := int32(1)
	// Two independent triangles.
	for _, base := range []int32{10, 20} {
		txns = append(txns,
			tx(id, 0, base, base+1, 1000),
			tx(id+1, 1, base+1, base+2, 1000),
			tx(id+2, 2, base+2, base, 1000))
		id += 3
	}
	// A hub touching both, with degree far past the cap.
	hub := int32(99)
	for i := int32(0); i < 40; i++ {
		txns = append(txns, tx(id, 3, hub, 200+i, 1000))
		id++
	}
	txns = append(txns, tx(id, 4, 10, hub, 1000), tx(id+1, 5, hub, 20, 1000))

	cfg := testConfig()
	cfg.MaxAccountDegree = 10
	for _, c := range Detect(ledgerOf(txns...), cfg) {
		for _, a := range c.Accounts {
			if a == hub {
				t.Fatalf("the hub linked %d accounts into one candidate", len(c.Accounts))
			}
		}
	}
}

func TestGroupsOutsideTheSizeBoundsAreDropped(t *testing.T) {
	cfg := testConfig()
	cfg.MinTxns, cfg.MinAccounts = 3, 3

	tooSmall := ledgerOf(tx(1, 0, 1, 2, 1000), tx(2, 1, 2, 1, 1000), tx(3, 2, 1, 2, 1000))
	if got := Detect(tooSmall, cfg); len(got) != 0 {
		t.Errorf("2 accounts passed MinAccounts=3: %+v", got)
	}

	cfg.MaxAccounts = 3
	var big []Txn
	for i := int32(0); i < 6; i++ {
		big = append(big, tx(i+1, float64(i), 1, i+2, 1000))
	}
	if got := Detect(ledgerOf(big...), cfg); len(got) != 0 {
		t.Errorf("7 accounts passed MaxAccounts=3: %+v", got)
	}
}

// Every window is timed, including the ones too quiet to run the graph pass on.
// Dropping them would report the cost of only the expensive half of the work.
func TestQuietWindowsAreStillTimed(t *testing.T) {
	led := ledgerOf(
		tx(1, 0, 1, 2, 1000), tx(2, 1, 2, 3, 1000), tx(3, 2, 3, 1, 1000),
		tx(4, 400, 4, 5, 1000),
	)
	cfg := testConfig()
	cfg.WindowHours, cfg.StrideHours = 24, 24

	_, windows := DetectTimed(led, cfg)
	if len(windows) < 17 {
		t.Fatalf("recorded %d windows over a 400h ledger at a 24h stride, want ~17", len(windows))
	}
	var counted int
	for _, w := range windows {
		counted += w.Txns
	}
	if counted != len(led.Txns) {
		t.Errorf("windows account for %d transfers, ledger holds %d", counted, len(led.Txns))
	}
}

func TestDetectOnAnEmptyLedgerReturnsNothing(t *testing.T) {
	c, w := DetectTimed(&Ledger{}, testConfig())
	if c != nil || w != nil {
		t.Errorf("got %v / %v, want nothing", c, w)
	}
}

// --------------------------------------------------------------------------
// ActivePeriod — the trap that faked precision 0.598 / recall 1.000
// --------------------------------------------------------------------------

// The generator stops background traffic while laundering already in flight
// plays out, leaving a stretch that is mostly laundering. Any detector looks
// brilliant there. The cut has to be found from the volume collapse, not from
// a hardcoded date, or it does not transfer to another ledger.
func TestActivePeriodFindsTheVolumeCollapse(t *testing.T) {
	var txns []Txn
	id := int32(1)
	for day := 0; day < 10; day++ { // busy: 100/day
		for i := 0; i < 100; i++ {
			txns = append(txns, tx(id, float64(day*24)+float64(i)*0.1, 1, 2, 100))
			id++
		}
	}
	for day := 10; day < 15; day++ { // the tail: 2/day
		for i := 0; i < 2; i++ {
			txns = append(txns, tx(id, float64(day*24)+float64(i), 1, 2, 100))
			id++
		}
	}
	cut := ActivePeriod(txns)
	want := epoch.Add(240 * time.Hour)
	if !cut.Equal(want) {
		t.Fatalf("cut at %v, want the first quiet day %v", cut, want)
	}

	trimmed := Trim(&Ledger{Txns: txns}, cut)
	if len(trimmed.Txns) != 1000 {
		t.Errorf("trimmed to %d transfers, want the 1000 busy ones", len(trimmed.Txns))
	}
	for _, tn := range trimmed.Txns {
		if !tn.TS.Before(cut) {
			t.Fatalf("transfer at %v survived a cut at %v", tn.TS, cut)
		}
	}
}

// A ledger that never collapses must not be trimmed at all — otherwise the
// heuristic would quietly discard real data on a well-behaved feed.
func TestActivePeriodKeepsAnEvenLedgerWhole(t *testing.T) {
	var txns []Txn
	id := int32(1)
	for day := 0; day < 12; day++ {
		for i := 0; i < 50; i++ {
			txns = append(txns, tx(id, float64(day*24)+float64(i)*0.1, 1, 2, 100))
			id++
		}
	}
	cut := ActivePeriod(txns)
	if n := len(Trim(&Ledger{Txns: txns}, cut).Txns); n != len(txns) {
		t.Errorf("trimmed an even ledger from %d to %d", len(txns), n)
	}
}

func TestActivePeriodOnAnEmptyLedger(t *testing.T) {
	if got := ActivePeriod(nil); !got.IsZero() {
		t.Errorf("got %v, want the zero time", got)
	}
}

// --------------------------------------------------------------------------
// splitting: temporal, never random
// --------------------------------------------------------------------------

func TestSplitIsTemporal(t *testing.T) {
	cands := []Candidate{
		{Window: at(0)}, {Window: at(10)}, {Window: at(50)}, {Window: at(90)},
	}
	train, test := Split(cands, at(50))
	if len(train) != 2 || len(test) != 2 {
		t.Fatalf("split %d/%d, want 2/2", len(train), len(test))
	}
	for _, c := range train {
		for _, h := range test {
			if !c.Window.Before(h.Window) {
				t.Fatalf("train window %v is not before test window %v", c.Window, h.Window)
			}
		}
	}
}

func TestSplitTimePlacesTheCutByFraction(t *testing.T) {
	led := ledgerOf(tx(1, 0, 1, 2, 1), tx(2, 100, 1, 2, 1))
	if got := SplitTime(led, 0.7); !got.Equal(at(70)) {
		t.Errorf("cut at %v, want %v", got, at(70))
	}
}

func TestLabelsMarkCandidatesTouchingALabelledRing(t *testing.T) {
	led := ledgerOf(
		Txn{ID: 1, TS: at(0), From: 1, To: 2, Amount: 100, PatternID: 7},
		tx(2, 1, 3, 4, 100),
	)
	got := Labels(led, []Candidate{{TxnIDs: []int32{1}}, {TxnIDs: []int32{2}}, {TxnIDs: []int32{9}}})
	want := []bool{true, false, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d: got %v want %v", i, got[i], want[i])
		}
	}
}
