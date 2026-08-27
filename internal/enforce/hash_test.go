package enforce

import (
	"testing"
	"time"
)

// A field left out of the digest is a field someone can rewrite without
// breaking the chain. For this table that means silently lengthening a freeze,
// re-pointing a hold at a different decision, or moving it to another account.
func TestEveryRecordedFieldIsCoveredByTheHash(t *testing.T) {
	seq := int64(4)
	base := Event{
		Seq: 2, Event: EventImpose, Account: "1241|80121F600", Tier: TierFrozen,
		RingID: 4, DecisionSeq: &seq, Pass: "24h/6h", Reason: "policy approved block",
		ImposedAt: time.Date(2026, 8, 27, 3, 43, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 8, 30, 3, 43, 0, 0, time.UTC),
		PrevHash:  "abcdef",
	}
	baseline := eventHash(base)

	other := int64(5)
	mutations := map[string]func(*Event){
		"seq":         func(e *Event) { e.Seq = 3 },
		"event":       func(e *Event) { e.Event = EventLift },
		"account":     func(e *Event) { e.Account = "1241|80121F601" },
		"tier":        func(e *Event) { e.Tier = TierWatch },
		"ring":        func(e *Event) { e.RingID = 5 },
		"decision":    func(e *Event) { e.DecisionSeq = &other },
		"no decision": func(e *Event) { e.DecisionSeq = nil },
		"pass":        func(e *Event) { e.Pass = "72h/24h" },
		"reason":      func(e *Event) { e.Reason = "no reason at all" },
		"imposed":     func(e *Event) { e.ImposedAt = e.ImposedAt.Add(time.Hour) },
		"expiry":      func(e *Event) { e.ExpiresAt = e.ExpiresAt.Add(30 * 24 * time.Hour) },
		"predecessor": func(e *Event) { e.PrevHash = "fedcba" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			e := base
			mutate(&e)
			if eventHash(e) == baseline {
				t.Errorf("editing %s left the hash unchanged", name)
			}
		})
	}
}

// The digest must survive the round trip through a column that keeps
// microseconds. Hashing nanoseconds meant this chain never verified once.
func TestTheDigestSurvivesAStorageRoundTrip(t *testing.T) {
	written := Event{Seq: 1, Event: EventImpose, Account: "a", Tier: TierFrozen,
		ImposedAt: time.Date(2026, 8, 27, 3, 43, 12, 123456789, time.UTC),
		ExpiresAt: time.Date(2026, 8, 30, 3, 43, 12, 987654321, time.UTC)}

	readBack := written
	readBack.ImposedAt = written.ImposedAt.Truncate(time.Microsecond).
		In(time.FixedZone("IST", 5*3600+1800))
	readBack.ExpiresAt = written.ExpiresAt.Truncate(time.Microsecond).
		In(time.FixedZone("IST", 5*3600+1800))

	if eventHash(written) != eventHash(readBack) {
		t.Error("the same instants at storage precision, in another zone, hashed differently")
	}
}

// A whole second must not render differently from the same instant carrying
// zero fractional digits — RFC3339Nano drops trailing zeroes, which is half of
// why the original format could not survive a round trip.
func TestWholeSecondsRenderAtFullPrecision(t *testing.T) {
	whole := time.Date(2026, 8, 27, 3, 43, 12, 0, time.UTC)
	if got := stamp(whole); got != "2026-08-27T03:43:12.000000Z" {
		t.Errorf("stamp = %q", got)
	}
}
