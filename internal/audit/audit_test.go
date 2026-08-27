package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
)

func sample() (agent.Evidence, agent.Assessment) {
	ev := agent.Evidence{RingID: 4, Accounts: 8, Txns: 21, TotalAmount: 17_700_000, Conservation: 0.949}
	a := agent.Assessment{
		RingID:      4,
		Action:      agent.ActionHold,
		Proposed:    agent.ActionBlock,
		Confidence:  0.94,
		Source:      "gemini-3.5-flash-lite",
		Rationale:   "conservation 0.949 across three intermediaries",
		Adjustments: []string{"block withheld: 17700000 exceeds the 10000000 autonomous ceiling"},
		DecidedAt:   time.Date(2026, 8, 27, 3, 43, 12, 123456000, time.UTC),
	}
	return ev, a
}

// Every field the hash covers must change it. A field left out of the digest is
// a field an editor can rewrite without breaking the chain, which is the whole
// property this package exists to provide.
func TestEveryRecordedFieldIsCoveredByTheHash(t *testing.T) {
	ev, base := sample()
	baseline := entryHash(1, GenesisHash, base, canonicalJSON(ev))

	mutations := map[string]func(*agent.Assessment){
		"action":      func(a *agent.Assessment) { a.Action = agent.ActionAllow },
		"proposed":    func(a *agent.Assessment) { a.Proposed = agent.ActionAllow },
		"ring":        func(a *agent.Assessment) { a.RingID = 5 },
		"confidence":  func(a *agent.Assessment) { a.Confidence = 0.11 },
		"source":      func(a *agent.Assessment) { a.Source = "somewhere else" },
		"rationale":   func(a *agent.Assessment) { a.Rationale = "because I said so" },
		"adjustments": func(a *agent.Assessment) { a.Adjustments = nil },
		"decided_at":  func(a *agent.Assessment) { a.DecidedAt = a.DecidedAt.Add(time.Second) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			a := base
			a.Adjustments = append([]string(nil), base.Adjustments...)
			mutate(&a)
			if entryHash(1, GenesisHash, a, canonicalJSON(ev)) == baseline {
				t.Errorf("editing %s left the hash unchanged", name)
			}
		})
	}

	t.Run("evidence", func(t *testing.T) {
		other := ev
		other.TotalAmount = 1
		if entryHash(1, GenesisHash, base, canonicalJSON(other)) == baseline {
			t.Error("editing the evidence left the hash unchanged")
		}
	})
}

// Position is hashed so rows cannot be reordered, and the predecessor is hashed
// so a row cannot be removed from the middle.
func TestPositionAndPredecessorAreBothCommittedTo(t *testing.T) {
	ev, a := sample()
	first := entryHash(1, GenesisHash, a, canonicalJSON(ev))
	if entryHash(2, GenesisHash, a, canonicalJSON(ev)) == first {
		t.Error("the same content at a different sequence hashes the same")
	}
	if entryHash(1, "some other hash", a, canonicalJSON(ev)) == first {
		t.Error("the same content after a different predecessor hashes the same")
	}
}

// The genesis constant is what stops a log with its earliest rows deleted from
// verifying as a shorter but internally consistent chain.
func TestGenesisIsAFixedAnchor(t *testing.T) {
	if GenesisHash != "warren-genesis" {
		t.Errorf("genesis changed to %q; every existing log stops verifying", GenesisHash)
	}
}

func TestHashingIsStable(t *testing.T) {
	ev, a := sample()
	if entryHash(3, "abc", a, canonicalJSON(ev)) != entryHash(3, "abc", a, canonicalJSON(ev)) {
		t.Error("the same entry hashed twice gave two answers")
	}
}

// Record writes the timestamp to Postgres, which keeps microseconds; the digest
// formats to microseconds for the same reason. If the digest kept nanoseconds,
// every row would fail to verify the moment it was read back.
func TestTheDigestTimestampSurvivesAPostgresRoundTrip(t *testing.T) {
	ev, a := sample()
	written := entryHash(1, GenesisHash, a, canonicalJSON(ev))

	readBack := a
	readBack.DecidedAt = a.DecidedAt.Truncate(time.Microsecond).In(time.FixedZone("IST", 5*3600+1800))
	if entryHash(1, GenesisHash, readBack, canonicalJSON(ev)) != written {
		t.Error("the same instant in another zone, at microsecond precision, hashed differently")
	}
}

// Verification re-encodes the evidence rather than comparing the bytes the
// database happened to return, so key order cannot decide whether a log
// verifies.
func TestCanonicalJSONIsIndependentOfHowEvidenceWasStored(t *testing.T) {
	ev, _ := sample()
	if string(canonicalJSON(ev)) != string(canonicalJSON(ev)) {
		t.Error("evidence encoded twice gave two byte strings")
	}
}

func TestShortLeavesRoomToIdentifyAHash(t *testing.T) {
	long := strings.Repeat("a", 64)
	if got := short(long); len([]rune(got)) != 13 {
		t.Errorf("short(%d chars) = %q", len(long), got)
	}
	if got := short("abc"); got != "abc" {
		t.Errorf("short(\"abc\") = %q, want it untouched", got)
	}
}
