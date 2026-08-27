package ui

import (
	"strings"
	"testing"
)

// pendingRow is the header row the backlog meter draws, from a rendered frame.
// It is found by position rather than by content: the point of the row is that
// it is always there, in the same place, whether or not anything is pending.
func pendingRow(t *testing.T, m *Model) string {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) < headerHeight {
		t.Fatalf("frame has %d lines, no header", len(lines))
	}
	return lines[headerHeight-1]
}

func TestPendingMeterReportsBacklog(t *testing.T) {
	cases := []struct {
		name              string
		pending, unsched  int
		want, wantMissing []string
	}{
		{
			name:        "nothing waiting says so",
			want:        []string{"pend", "no pods waiting"},
			wantMissing: []string{"pending", "unsched"},
		},
		{
			name:    "waiting pods are counted",
			pending: 12,
			want:    []string{"pend", "12 pending"},
			// Nothing has been refused, so nothing may claim it has.
			wantMissing: []string{"unsched"},
		},
		{
			name:    "refused pods are called out separately",
			pending: 12, unsched: 4,
			want: []string{"pend", "12 pending", "4 unsched"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, 160, 44, 6)
			m.snap.Totals.Pending, m.snap.Totals.Unschedulable = tc.pending, tc.unsched
			// The meter is smoothed, so let it settle before reading the percentage.
			for i := 0; i < 60; i++ {
				m.View()
				m.pend.Step(25_000_000)
			}
			row := pendingRow(t, m)
			for _, want := range tc.want {
				if !strings.Contains(row, want) {
					t.Errorf("row %q is missing %q", row, want)
				}
			}
			for _, unwanted := range tc.wantMissing {
				if strings.Contains(row, unwanted) {
					t.Errorf("row %q should not mention %q", row, unwanted)
				}
			}
		})
	}
}

// TestPendingMeterFillsProportionally checks the two things the bar has to get
// right: a backlog is measured against every pod in the cluster, and a backlog
// too small to fill a cell is still not nothing.
func TestPendingMeterFillsProportionally(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)
	total := m.snap.Totals.Pods

	m.snap.Totals.Pending, m.snap.Totals.Unschedulable = total, 0
	settle(m)
	if got := m.pend.CPU; got < 0.49 || got > 0.51 {
		t.Errorf("a backlog the size of the running fleet filled %.2f, want ~0.5", got)
	}

	m.snap.Totals.Pending = 1
	settle(m)
	if m.pend.CPU <= 0 {
		t.Errorf("one pending pod filled nothing (%v); it must claim at least a cell", m.pend.CPU)
	}
	if strings.Contains(pendingRow(t, m), "no pods waiting") {
		t.Error("one pending pod rendered as an idle meter")
	}
}

func settle(m *Model) {
	for i := 0; i < 80; i++ {
		m.View()
		m.pend.Step(25_000_000)
	}
}
