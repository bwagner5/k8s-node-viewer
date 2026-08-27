package model

import (
	"testing"
	"time"
)

// TestHumanAgeHoldsSecondsToNinetyNine pins the boundary the whole seconds range
// exists for: an instance takes about ninety seconds to reach Ready, so the age
// has to stay in seconds across that wait and only then round to minutes.
func TestHumanAgeHoldsSecondsToNinetyNine(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{999 * time.Millisecond, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "60s"},
		{90 * time.Second, "90s"},
		{99*time.Second + 999*time.Millisecond, "99s"},
		{100 * time.Second, "1m"},
		{119 * time.Second, "1m"},
		{2 * time.Minute, "2m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{25 * time.Hour, "1d"},
	}
	for _, c := range cases {
		if got := HumanAge(c.in); got != c.want {
			t.Errorf("HumanAge(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestHumanAgeFitsThreeCells guards the column widths the grid and the dense
// table are built around: nothing in the first day may render wider than "999d"
// would, and nothing in the first hour wider than three cells.
func TestHumanAgeFitsThreeCells(t *testing.T) {
	for d := time.Duration(0); d < time.Hour; d += 250 * time.Millisecond {
		if got := HumanAge(d); len(got) > 3 {
			t.Fatalf("HumanAge(%s) = %q, wider than three cells", d, got)
		}
	}
}

// TestClaimConditionsReadPositively covers the polarity split: a NodeClaim's
// conditions are milestones where True is the goal, while a node's pressure
// conditions are alarms where True is the problem. Reading a claim with node
// polarity would paint every milestone it has reached as a fault.
func TestClaimConditionsReadPositively(t *testing.T) {
	cases := []struct {
		cond Condition
		bad  bool
	}{
		{Condition{Type: "Launched", Status: "True"}, false},
		{Condition{Type: "Launched", Status: "Unknown"}, true},
		{Condition{Type: "Registered", Status: "True"}, false},
		{Condition{Type: "Initialized", Status: "Unknown"}, true},
		{Condition{Type: "Ready", Status: "True"}, false},
		{Condition{Type: "Ready", Status: "False"}, true},
		{Condition{Type: "MemoryPressure", Status: "False"}, false},
		{Condition{Type: "MemoryPressure", Status: "True"}, true},
		{Condition{Type: "Drifted", Status: "True"}, true},
	}
	for _, c := range cases {
		if got := c.cond.Bad(); got != c.bad {
			t.Errorf("%s=%s Bad() = %v, want %v", c.cond.Type, c.cond.Status, got, c.bad)
		}
	}
}
