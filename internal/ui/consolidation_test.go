package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// consolidationSnapshot is one node per verdict, in a known order.
func consolidationSnapshot() *model.Snapshot {
	snap := testSnapshot(3)
	verdicts := []model.Consolidation{model.ConsolidationYes, model.ConsolidationNo, model.ConsolidationUnknown}
	for i, n := range snap.Nodes {
		n.Name = []string{"node-yes", "node-no", "node-unknown"}[i]
		n.Phase = model.PhaseReady
		n.Consolidatable = verdicts[i]
		if verdicts[i] != model.ConsolidationUnknown {
			n.ConsolidationReason = "verdict for " + n.Name
			n.ConsolidationAt = time.Now().Add(-2 * time.Minute)
		}
		for _, p := range n.Pods {
			p.NodeName = n.Name
		}
	}
	return snap
}

// denseRow returns the table row that names node, from a rendered dense frame.
func denseRow(t *testing.T, m *Model, node string) string {
	t.Helper()
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, node) {
			return line
		}
	}
	t.Fatalf("no row for %s in:\n%s", node, m.View())
	return ""
}

func TestDenseTableShowsConsolidatableColumn(t *testing.T) {
	m := New(Config{FPS: 20, Legend: true})
	m.w, m.h = 160, 20
	m.applySnapshot(consolidationSnapshot())
	m.setMode(ModeDense)

	header := strings.Split(m.View(), "\n")[headerHeight+legendHeight]
	if !strings.Contains(header, "CONS") {
		t.Fatalf("no CONS header in the dense table:\n%s", header)
	}
	consAt := denseColumns(m.w).xCons + 1

	// The column reads y / n / · per node, at the column's own offset — matching
	// somewhere else on the row would prove nothing.
	for _, tc := range []struct{ node, want string }{
		{"node-yes", "y"}, {"node-no", "n"}, {"node-unknown", "·"},
	} {
		row := []rune(denseRow(t, m, tc.node))
		if consAt >= len(row) {
			t.Fatalf("%s: row is shorter than the CONS column", tc.node)
		}
		if got := string(row[consAt]); got != tc.want {
			t.Fatalf("%s: CONS cell is %q, want %q\n%s", tc.node, got, tc.want, string(row))
		}
	}
}

// TestDenseColumnsKeepStateAheadOfConsolidatable pins the live-demo priority:
// current lifecycle state survives every secondary descriptor.
func TestDenseColumnsKeepStateAheadOfConsolidatable(t *testing.T) {
	wide := denseColumns(200)
	if !wide.showCons || !wide.showState || !wide.showPool {
		t.Fatalf("a 200-wide table should show everything: %+v", wide)
	}
	// Narrow enough that CONS has gone, and STATE is still there.
	var found bool
	for w := 200; w >= 30; w-- {
		d := denseColumns(w)
		if d.showState && !d.showCons {
			found = true
		}
		if !d.showState && d.showCons {
			t.Fatalf("width %d dropped STATE before CONS: %+v", w, d)
		}
	}
	if !found {
		t.Fatal("no width where STATE outlives CONS, so the priority is untested")
	}
}

func TestDetailPaneShowsConsolidationVerdict(t *testing.T) {
	m := New(Config{FPS: 20, Legend: true})
	m.w, m.h = 150, 44
	m.applySnapshot(consolidationSnapshot())
	m.describe = &stubDescriber{detail: sampleDetail("")}
	for i, v := range m.vis {
		if v.node.Name == "node-yes" {
			m.setCursor(i)
		}
	}
	openPane(t, m)

	out := m.View()
	if !strings.Contains(out, "consolidate") || !strings.Contains(out, "verdict for node-yes") {
		t.Fatalf("pane does not carry the verdict and its reason\n%s", out)
	}
	if !strings.Contains(out, "2m ago") {
		t.Fatalf("pane does not say how old the verdict is\n%s", out)
	}
}

func TestDenseFrameGeometryWithConsolidation(t *testing.T) {
	// The new column must not be able to ruin the frame at any width.
	for _, size := range []struct{ w, h int }{{200, 30}, {160, 20}, {120, 16}, {90, 12}, {70, 10}, {40, 8}, {24, 6}} {
		m := New(Config{FPS: 20, Legend: true})
		m.w, m.h = size.w, size.h
		m.applySnapshot(consolidationSnapshot())
		m.setMode(ModeDense)
		assertFrame(t, m, size.w, size.h, ModeDense, 3, "dense with CONS")
	}
}
