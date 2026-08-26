package enforce

import (
	"testing"
	"time"

	"github.com/vinayaktyagi10/warren/internal/agent"
	"github.com/vinayaktyagi10/warren/internal/detect"
)

func at(h int) time.Time {
	return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(h) * time.Hour)
}

func ring(n int) detect.Candidate {
	c := detect.Candidate{Window: at(0)}
	for i := 0; i < n; i++ {
		c.Accounts = append(c.Accounts, int32(i+1))
	}
	return c
}

func assessed(action agent.Action) agent.Assessment {
	return agent.Assessment{RingID: 7, Action: action, Proposed: action, Source: "test"}
}

// Only a block that survived the policy may stop money. Everything else the
// system decides produces information, never enforcement.
func TestOnlyBlockFreezes(t *testing.T) {
	cases := []struct {
		action   agent.Action
		wantTier Tier
	}{
		{agent.ActionBlock, TierFrozen},
		{agent.ActionHold, TierWatch},
		{agent.ActionAllow, ""},
	}
	for _, c := range cases {
		got, _ := Restrictions(assessed(c.action), ring(3), at(72), DefaultLimits())
		if c.wantTier == "" {
			if len(got) != 0 {
				t.Errorf("%s produced %d restrictions, want none", c.action, len(got))
			}
			continue
		}
		if len(got) != 3 {
			t.Fatalf("%s produced %d restrictions, want 3", c.action, len(got))
		}
		for _, r := range got {
			if r.Tier != c.wantTier {
				t.Errorf("%s produced tier %q, want %q", c.action, r.Tier, c.wantTier)
			}
		}
	}
}

// A watch never stops a transfer. It exists to be seen, not to act.
func TestWatchStopsNothing(t *testing.T) {
	if TierWatch.stops() {
		t.Error("watch tier must not stop transfers")
	}
	if !TierFrozen.stops() {
		t.Error("frozen tier must stop transfers")
	}
}

// One automated decision may not freeze an unbounded number of people. Past the
// cap the system declines to act at all rather than freezing an arbitrary
// subset, because which subset it picked would be indefensible.
func TestBlastRadiusCap(t *testing.T) {
	lim := DefaultLimits()
	got, bounds := Restrictions(assessed(agent.ActionBlock), ring(lim.MaxAccountsPerRing+1), at(72), lim)
	if len(got) != 0 {
		t.Errorf("oversized ring produced %d restrictions, want none", len(got))
	}
	if len(bounds) == 0 {
		t.Error("declining to act must be recorded, not silent")
	}

	got, _ = Restrictions(assessed(agent.ActionBlock), ring(lim.MaxAccountsPerRing), at(72), lim)
	if len(got) != lim.MaxAccountsPerRing {
		t.Errorf("ring at the cap produced %d restrictions, want %d", len(got), lim.MaxAccountsPerRing)
	}
}

// Nothing an automated decision imposes is permanent.
func TestEverythingExpires(t *testing.T) {
	lim := DefaultLimits()
	got, _ := Restrictions(assessed(agent.ActionBlock), ring(2), at(72), lim)
	for _, r := range got {
		if !r.Expires.Equal(at(72).Add(lim.FrozenFor)) {
			t.Errorf("expiry %v, want %v", r.Expires, at(72).Add(lim.FrozenFor))
		}
	}
}

func TestRegisterChecksExpiry(t *testing.T) {
	var reg Register
	reg.Impose(Restriction{Account: 5, Tier: TierFrozen, Imposed: at(0), Expires: at(72)})

	if _, ok := reg.Stopped(5, at(-1)); ok {
		t.Error("a restriction stopped a transfer before it was imposed")
	}
	if _, ok := reg.Stopped(5, at(10)); !ok {
		t.Error("an active restriction failed to stop a transfer")
	}
	if _, ok := reg.Stopped(5, at(72)); ok {
		t.Error("a restriction stopped a transfer at its expiry instant")
	}
	if _, ok := reg.Stopped(5, at(100)); ok {
		t.Error("an expired restriction stopped a transfer")
	}
	if _, ok := reg.Stopped(6, at(10)); ok {
		t.Error("an unrestricted account was stopped")
	}
}

// Lifting is a first-class operation: the whole point of a bounded action is
// that it can be undone before its expiry.
func TestLift(t *testing.T) {
	var reg Register
	reg.Impose(Restriction{Account: 5, Tier: TierFrozen, Imposed: at(0), Expires: at(72)})
	if !reg.Lift(5, at(10), "analyst cleared the ring") {
		t.Fatal("lifting an active restriction reported failure")
	}
	if _, ok := reg.Stopped(5, at(11)); ok {
		t.Error("a lifted restriction still stopped a transfer")
	}
	if reg.Lift(5, at(12), "again") {
		t.Error("lifting an already-lifted restriction reported success")
	}
}

// An account caught in two rings keeps the protection that lasts longer, and a
// watch never downgrades a freeze.
func TestImposeKeepsTheStrongerRestriction(t *testing.T) {
	var reg Register
	reg.Impose(Restriction{Account: 5, Tier: TierFrozen, Imposed: at(0), Expires: at(72)})
	reg.Impose(Restriction{Account: 5, Tier: TierWatch, Imposed: at(1), Expires: at(200)})

	r, ok := reg.Stopped(5, at(80))
	if ok {
		t.Errorf("a watch extended a freeze past its expiry: %+v", r)
	}
	if r, ok := reg.Stopped(5, at(10)); !ok || r.Tier != TierFrozen {
		t.Error("a watch downgraded an active freeze")
	}

	reg.Impose(Restriction{Account: 5, Tier: TierFrozen, Imposed: at(1), Expires: at(100)})
	if _, ok := reg.Stopped(5, at(80)); !ok {
		t.Error("a longer freeze failed to extend the existing one")
	}
}
