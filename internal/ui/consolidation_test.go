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

func activeConsolidationSnapshot() *model.Snapshot {
	now := time.Now()
	snap := testSnapshot(3)
	for i, n := range snap.Nodes[:2] {
		n.Name = []string{"node-out-a", "node-out-b"}[i]
		n.NodePool = "general"
		n.Phase = model.PhaseDraining
		n.Schedulable = false
		n.Message = "disrupted: underutilized"
		n.Consolidatable = model.ConsolidationYes
		n.DisruptionReason = "Underutilized"
		n.ConsolidationReason = "Underutilized/action-id: replace: part of 2-node consolidation"
		n.ConsolidationAt = now.Add(-12 * time.Second)
		n.Transitions = []model.PhaseTransition{
			{Phase: model.PhaseReady, At: now.Add(-time.Hour)},
			{Phase: model.PhaseDraining, At: now.Add(-10 * time.Second)},
		}
		for _, p := range n.Pods {
			p.NodeName = n.Name
		}
	}
	target := snap.Nodes[2]
	target.Name = "claim-in-c"
	target.NodeClaim = "claim-in-c"
	target.NodePool = "general"
	target.Phase = model.PhaseProvisioning
	target.Message = "Initializing"
	target.Created = now.Add(-5 * time.Second)
	target.Pods = nil
	target.Requests = model.Resources{}
	target.Transitions = []model.PhaseTransition{{Phase: model.PhaseProvisioning, At: target.Created}}
	return snap
}

func TestActiveConsolidationIsGroupedAcrossEveryMode(t *testing.T) {
	for _, mode := range []Mode{ModePods, ModeNodes, ModeDense} {
		m := New(Config{FPS: 20, Legend: true})
		m.w, m.h = 180, 34
		m.applySnapshot(activeConsolidationSnapshot())
		m.setMode(mode)

		out := m.View()
		for _, want := range []string{"C1", "2 out → 1 in", "waiting for replacement"} {
			if !strings.Contains(out, want) {
				t.Fatalf("mode=%v is missing %q:\n%s", mode, want, out)
			}
		}
		if mode == ModeDense {
			if !strings.Contains(out, "ACTION") || !strings.Contains(denseRow(t, m, "node-out-a"), "C1 R↓") || !strings.Contains(denseRow(t, m, "claim-in-c"), "C1 R↑") {
				t.Fatalf("dense mode does not expose both action directions:\n%s", out)
			}
		} else if !strings.Contains(out, "C1 ↓ WAITING") || !strings.Contains(out, "C1 ↑ STARTING") {
			t.Fatalf("mode=%v does not expose full action chips:\n%s", mode, out)
		}
		assertFrame(t, m, m.w, m.h, mode, 3, "active consolidation")
	}
}

func TestConsolidatableCandidateIsNotAnActiveAction(t *testing.T) {
	snap := consolidationSnapshot()
	view := detectConsolidations(snap, time.Now())
	if len(view.actions) != 0 || len(view.members) != 0 {
		t.Fatalf("eligibility verdict created an active action: %+v", view)
	}
}

func TestNodeClaimDisruptionReasonActivatesConsolidation(t *testing.T) {
	snap := testSnapshot(1)
	n := snap.Nodes[0]
	n.Phase = model.PhaseDraining
	n.Message = "disrupted: unknown"
	n.DisruptionReason = "Underutilized"
	n.Consolidatable = model.ConsolidationUnknown
	n.Transitions = []model.PhaseTransition{{Phase: model.PhaseDraining, At: time.Now()}}
	view := detectConsolidations(snap, time.Now())
	if len(view.actions) != 1 || view.members[n.Name].role != consolidationOutgoing {
		t.Fatalf("active disruption condition was not recognized: %+v", view)
	}
}

func TestEmptyConsolidationNeverClaimsAReplacement(t *testing.T) {
	now := time.Now()
	snap := testSnapshot(2)
	source := snap.Nodes[0]
	source.Name = "empty-node"
	source.NodePool = "general"
	source.Phase = model.PhaseDraining
	source.Message = "disrupted: unknown"
	source.DisruptionReason = "Empty"
	// Deliberately stale/conflicting candidate text: the active Empty condition
	// must win, which is the regression this test protects.
	source.ConsolidationReason = "Underutilized/old-action: replace: [empty-node] -> [1 replacement]"
	source.ConsolidationAt = now
	source.Pods = nil
	source.Transitions = []model.PhaseTransition{{Phase: model.PhaseDraining, At: now}}

	unrelated := snap.Nodes[1]
	unrelated.Name = "unrelated-scale-up"
	unrelated.NodeClaim = unrelated.Name
	unrelated.NodePool = "general"
	unrelated.Phase = model.PhaseProvisioning
	unrelated.Created = now.Add(time.Second)
	unrelated.Transitions = []model.PhaseTransition{{Phase: model.PhaseProvisioning, At: unrelated.Created}}

	view := detectConsolidations(snap, now.Add(2*time.Second))
	if len(view.actions) != 1 {
		t.Fatalf("actions = %d, want one: %+v", len(view.actions), view)
	}
	a := view.actions[0]
	if a.kind != consolidationKindEmpty || len(a.targets) != 0 {
		t.Fatalf("empty action classified as kind=%v with %d targets", a.kind, len(a.targets))
	}
	summary := actionSummary(a)
	if !strings.Contains(summary, "EMPTY SCALE-DOWN") || !strings.Contains(summary, "removing empty node") {
		t.Fatalf("empty action summary is unclear: %q", summary)
	}
	if strings.Contains(strings.ToLower(summary), "replacement") {
		t.Fatalf("empty action falsely claims a replacement: %q", summary)
	}
	if got := view.members[source.Name].stage; got != "EMPTY" {
		t.Fatalf("empty source chip stage = %q, want EMPTY", got)
	}
}

func TestUnderutilizedDeleteNamesExistingCapacity(t *testing.T) {
	now := time.Now()
	snap := testSnapshot(1)
	n := snap.Nodes[0]
	n.Phase = model.PhaseDraining
	n.Message = "disrupted: unknown"
	n.DisruptionReason = "Underutilized"
	n.ConsolidationReason = "Underutilized/action-id: delete: [node-a]"
	n.ConsolidationAt = now
	n.Transitions = []model.PhaseTransition{{Phase: model.PhaseDraining, At: now}}

	a := detectConsolidations(snap, now).actions[0]
	summary := actionSummary(a)
	for _, want := range []string{"BIN-PACK SCALE-DOWN", "existing capacity", "moving pods"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("bin-pack summary is missing %q: %q", want, summary)
		}
	}
	if strings.Contains(summary, "replacement pending") {
		t.Fatalf("delete action falsely waits for replacement: %q", summary)
	}
}

func TestConsolidationRibbonParticipatesInGridGeometry(t *testing.T) {
	m := New(Config{FPS: 20, Legend: true})
	m.w, m.h = 120, 30
	m.applySnapshot(activeConsolidationSnapshot())
	f := m.layoutFrame()
	if f.action != 1 {
		t.Fatalf("action ribbon height = %d, want 1", f.action)
	}
	if idx, ok := m.hitTest(1, f.gridTop()); !ok || idx != 0 {
		t.Fatalf("first card hit at grid top = %d, %v; want 0, true", idx, ok)
	}
}

func TestNodeCardsUseHeavyOutlines(t *testing.T) {
	m := newTestModel(t, 100, 24, 1)
	out := m.View()
	for _, want := range []string{"━", "┃"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ready node does not use heavy outline glyph %q:\n%s", want, out)
		}
	}

	snap := testSnapshot(1)
	snap.Nodes[0].Phase = model.PhaseProvisioning
	m.applySnapshot(snap)
	if out := m.View(); !strings.Contains(out, "╍") || !strings.Contains(out, "╏") {
		t.Fatalf("provisioning node does not use a heavy dashed outline:\n%s", out)
	}
}

func TestDenseActionOutlivesSecondaryColumns(t *testing.T) {
	var found bool
	for w := 200; w >= 30; w-- {
		d := denseColumnsFor(w, true)
		if d.showAction && !d.showCons && !d.showPool && !d.showType {
			found = true
		}
		if !d.showAction && (d.showCons || d.showPool || d.showType) {
			t.Fatalf("width %d dropped ACTION before a secondary column: %+v", w, d)
		}
	}
	if !found {
		t.Fatal("no width keeps ACTION after secondary columns are removed")
	}
}
