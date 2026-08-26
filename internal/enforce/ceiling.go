package enforce

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vinayaktyagi10/warren/internal/detect"
)

// Ceiling is the most value any ring-level system could possibly have stopped,
// given how long it has to wait before a ring is visible at all.
//
// It exists because a poor enforcement result has two very different causes and
// they demand opposite responses. If the detector is leaving stoppable money on
// the table, the fix is a better detector. If almost none of a ring's money is
// still in flight by the time any windowed detector could have seen the ring,
// then no detector fixes it and the architecture is what has to change. Guessing
// which one is happening is how a project spends a week on the wrong thing.
//
// The oracle deliberately assumes away everything except time: it is told which
// transfers belong to which labelled ring, and it acts the instant the earliest
// window that could contain enough of the ring closes. It has perfect precision,
// perfect recall, and no model. Whatever it cannot stop, nothing can.
type Ceiling struct {
	WindowHours int
	StrideHours int

	Rings int

	// Total is every labelled ring transfer in the period.
	Total      int
	TotalValue float64

	// Stoppable is what remains after the oracle's earliest possible action.
	Stoppable      int
	StoppableValue float64

	// NeverVisible counts rings that never reach MinTxns inside one window, so
	// no windowed detector sees them at all.
	NeverVisible int
}

func (c Ceiling) Share() float64 {
	if c.TotalValue == 0 {
		return 0
	}
	return c.StoppableValue / c.TotalValue
}

// MeasureCeilingIn is MeasureCeiling restricted to a time span: only ring value
// moving inside [from, to) counts, though visibility is still judged against the
// ring's whole history.
//
// This is the number a replay result has to be read against. Comparing what
// enforcement stopped in a 48-hour runway to a ceiling computed over the whole
// held-out period would flatter nothing and mislead badly — it would make a
// short runway look like a weak system.
func MeasureCeilingIn(led *detect.Ledger, cfg detect.Config, from, to time.Time) Ceiling {
	return measureCeiling(led, cfg, &from, &to)
}

// MeasureCeiling computes the ceiling for one window geometry.
//
// A ring becomes visible no earlier than the first window closure at or after
// its MinTxns-th transfer: before that, no single window holds enough of it to
// raise a candidate. Everything the ring moves at or after that instant is
// stoppable in principle; everything before it is already gone.
func MeasureCeiling(led *detect.Ledger, cfg detect.Config) Ceiling {
	return measureCeiling(led, cfg, nil, nil)
}

func measureCeiling(led *detect.Ledger, cfg detect.Config, from, to *time.Time) Ceiling {
	in := func(t time.Time) bool {
		if from != nil && t.Before(*from) {
			return false
		}
		if to != nil && !t.Before(*to) {
			return false
		}
		return true
	}
	c := Ceiling{WindowHours: cfg.WindowHours, StrideHours: cfg.StrideHours}
	if len(led.Txns) == 0 {
		return c
	}
	width := time.Duration(cfg.WindowHours) * time.Hour
	stride := time.Duration(cfg.StrideHours) * time.Hour
	origin := led.Txns[0].TS

	byRing := make(map[int32][]detect.Txn)
	for _, t := range led.Txns {
		if t.PatternID != 0 && t.IsLaundering {
			byRing[t.PatternID] = append(byRing[t.PatternID], t)
		}
	}

	for _, txns := range byRing {
		c.Rings++
		sort.Slice(txns, func(i, j int) bool { return txns[i].TS.Before(txns[j].TS) })
		for _, t := range txns {
			if !in(t.TS) {
				continue
			}
			c.Total++
			c.TotalValue += t.Amount
		}
		if len(txns) < cfg.MinTxns {
			c.NeverVisible++
			continue
		}

		// The window that first holds MinTxns of this ring closes no earlier
		// than the MinTxns-th transfer plus the remainder of that window.
		visible := txns[cfg.MinTxns-1].TS
		closure := nextClosure(origin, visible, width, stride)
		for _, t := range txns {
			if !t.TS.Before(closure) && in(t.TS) {
				c.Stoppable++
				c.StoppableValue += t.Amount
			}
		}
	}
	return c
}

// nextClosure returns the first window closure at or after t, on the grid of
// windows that start at origin and advance by stride.
func nextClosure(origin, t time.Time, width, stride time.Duration) time.Time {
	first := origin.Add(width)
	if !t.After(first) {
		return first
	}
	// Round up to the next grid point, and stay put when t is already one: a
	// transfer landing exactly on a closure is inside that closure's window.
	d := t.Sub(first)
	n := d / stride
	if n*stride < d {
		n++
	}
	return first.Add(n * stride)
}

// CeilingTable sweeps window geometries so the tradeoff is visible: a narrower
// window leaves more of a ring's money still in flight when the alarm goes off,
// and sees less of the ring's shape when it does.
func CeilingTable(led *detect.Ledger, base detect.Config, widths []int) []Ceiling {
	out := make([]Ceiling, 0, len(widths))
	for _, w := range widths {
		cfg := base
		cfg.WindowHours = w
		if cfg.StrideHours > w {
			cfg.StrideHours = w
		}
		out = append(out, MeasureCeiling(led, cfg))
	}
	return out
}

func FormatCeilings(cs []Ceiling) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%8s %8s %8s %12s %20s %10s\n",
		"window", "stride", "rings", "stoppable", "stoppable value", "share")
	for _, c := range cs {
		fmt.Fprintf(&b, "%7dh %7dh %8d %12d %20.0f %9.2f%%\n",
			c.WindowHours, c.StrideHours, c.Rings, c.Stoppable, c.StoppableValue, 100*c.Share())
	}
	return b.String()
}
