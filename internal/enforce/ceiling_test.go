package enforce

import (
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/detect"
)

func tx(id int32, h int, amt float64, ring int32) detect.Txn {
	return detect.Txn{
		ID: id, TS: at(h), Amount: amt,
		IsLaundering: ring != 0, PatternID: ring,
	}
}

// A ring is invisible until a window holds MinTxns of it. With a 72h window
// opening at the ledger's first transfer, the earliest possible alarm is hour
// 72, so everything the ring moved before then is unstoppable by anyone.
func TestCeilingCountsOnlyWhatMovesAfterTheAlarm(t *testing.T) {
	led := &detect.Ledger{Txns: []detect.Txn{
		tx(1, 0, 100, 1),
		tx(2, 1, 100, 1),
		tx(3, 2, 100, 1), // 3rd transfer: ring is knowable, alarm at hour 72
		tx(4, 80, 500, 1),
		tx(5, 100, 400, 1),
	}}
	cfg := detect.DefaultConfig() // 72h window, 24h stride, MinTxns 3

	c := MeasureCeiling(led, cfg)
	if c.Rings != 1 {
		t.Fatalf("rings = %d, want 1", c.Rings)
	}
	if c.TotalValue != 1200 {
		t.Errorf("total value = %v, want 1200", c.TotalValue)
	}
	if c.StoppableValue != 900 {
		t.Errorf("stoppable value = %v, want 900 (the two transfers after hour 72)", c.StoppableValue)
	}
}

// A ring that never reaches MinTxns inside any window is never seen at all.
func TestCeilingCountsRingsNoWindowCanSee(t *testing.T) {
	led := &detect.Ledger{Txns: []detect.Txn{tx(1, 0, 100, 1), tx(2, 1, 100, 1)}}
	c := MeasureCeiling(led, detect.DefaultConfig())
	if c.NeverVisible != 1 {
		t.Errorf("never visible = %d, want 1", c.NeverVisible)
	}
	if c.StoppableValue != 0 {
		t.Errorf("stoppable value = %v, want 0", c.StoppableValue)
	}
}

// The restricted form must actually restrict. This is the test that matters:
// a replay result is judged against MeasureCeilingIn, so a bound that silently
// did nothing would make the action layer look far worse than it is.
func TestCeilingInRestrictsToTheWindowGiven(t *testing.T) {
	led := &detect.Ledger{Txns: []detect.Txn{
		tx(1, 0, 100, 1),
		tx(2, 1, 100, 1),
		tx(3, 2, 100, 1),
		tx(4, 80, 500, 1),  // stoppable, inside the restricted span
		tx(5, 200, 400, 1), // stoppable, outside it
	}}
	cfg := detect.DefaultConfig()

	full := MeasureCeiling(led, cfg)
	if full.StoppableValue != 900 {
		t.Fatalf("unrestricted stoppable = %v, want 900", full.StoppableValue)
	}

	in := MeasureCeilingIn(led, cfg, at(72), at(100))
	if in.StoppableValue != 500 {
		t.Errorf("restricted stoppable = %v, want 500 — the bound did nothing", in.StoppableValue)
	}
	if in.TotalValue != 500 {
		t.Errorf("restricted total = %v, want 500", in.TotalValue)
	}
}

// A narrower window raises the ceiling, because less of the ring has already
// moved by the time the alarm can go off. This is the tradeoff the whole
// enforcement argument turns on, so it is pinned rather than assumed.
func TestNarrowerWindowRaisesTheCeiling(t *testing.T) {
	led := &detect.Ledger{Txns: []detect.Txn{
		tx(1, 0, 100, 1), tx(2, 1, 100, 1), tx(3, 2, 100, 1),
		tx(4, 30, 500, 1), tx(5, 80, 400, 1),
	}}
	wide := MeasureCeiling(led, detect.DefaultConfig())

	narrow := detect.DefaultConfig()
	narrow.WindowHours, narrow.StrideHours = 24, 24
	got := MeasureCeiling(led, narrow)

	if !(got.StoppableValue > wide.StoppableValue) {
		t.Errorf("narrow ceiling %v not above wide ceiling %v", got.StoppableValue, wide.StoppableValue)
	}
}

func TestNextClosureLandsOnTheGrid(t *testing.T) {
	origin := at(0)
	w, s := 72*time.Hour, 24*time.Hour
	cases := []struct{ from, want int }{
		{0, 72}, {10, 72}, {72, 72}, {73, 96}, {96, 96}, {97, 120},
	}
	for _, c := range cases {
		if got := nextClosure(origin, at(c.from), w, s); !got.Equal(at(c.want)) {
			t.Errorf("nextClosure(h%d) = h%v, want h%d", c.from, got.Sub(at(0)).Hours(), c.want)
		}
	}
}
