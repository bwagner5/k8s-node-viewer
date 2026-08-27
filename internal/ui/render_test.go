package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// The renderer writes fixed-size frames; bubbletea's diffing and every layout
// assumption downstream depend on that. These tests assert it directly, because
// an off-by-one in a box is invisible in review and obvious on a projector.

func TestMain(m *testing.M) {
	// Deterministic, escape-free output so width assertions measure glyphs.
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

func testSnapshot(nodes int) *model.Snapshot {
	snap := &model.Snapshot{
		Taken:        time.Now(),
		Context:      "test",
		HasKarpenter: true,
		NodePools:    []*model.NodePool{{Name: "general"}, {Name: "spot-batch"}},
	}
	phases := []model.Phase{model.PhaseReady, model.PhaseProvisioning, model.PhaseDraining,
		model.PhaseTerminating, model.PhaseCordoned, model.PhaseNotReady, model.PhaseGone}
	for i := 0; i < nodes; i++ {
		n := &model.Node{
			Name:         "ip-10-0-0-" + itoa(i),
			InstanceType: "m5.4xlarge",
			NodePool:     []string{"general", "spot-batch"}[i%2],
			Zone:         "us-west-2a",
			CapacityType: "spot",
			Phase:        phases[i%len(phases)],
			Ready:        true,
			Created:      time.Now().Add(-time.Duration(i) * time.Minute),
			Allocatable:  model.Resources{CPUMilli: 16000, MemBytes: 64 << 30, Pods: 110},
			Usage:        model.Resources{CPUMilli: int64(1000 * i), MemBytes: int64(i) << 30},
			HasUsage:     true,
			Price:        0.768,
			HasPrice:     true,
		}
		if n.Phase == model.PhaseGone {
			n.DeletedAt = time.Now()
		}
		for j := 0; j <= i%9; j++ {
			p := &model.Pod{
				Namespace: []string{"shop", "ml"}[j%2],
				Name:      "workload-" + itoa(i) + "-" + itoa(j),
				NodeName:  n.Name,
				Owner:     []string{"checkout", "trainer", "catalog"}[j%3],
				Phase:     []model.PodPhase{model.PodRunning, model.PodPending, model.PodTerminating}[j%3],
				DaemonSet: j == 0,
				Requests:  model.Resources{CPUMilli: int64(500 + 250*j), MemBytes: int64(j+1) << 29, Pods: 1},
			}
			n.Pods = append(n.Pods, p)
			n.Requests = n.Requests.Add(p.Requests)
		}
		snap.Totals.Nodes++
		snap.Totals.Pods += len(n.Pods)
		snap.Totals.Allocatable = snap.Totals.Allocatable.Add(n.Allocatable)
		snap.Totals.Requests = snap.Totals.Requests.Add(n.Requests)
		snap.Totals.HourlyCost += n.Price
		snap.Nodes = append(snap.Nodes, n)
	}
	// A backlog scaled to the fleet, so every geometry case also exercises the
	// pending meter — including the zero-node case, which must render the empty
	// bar rather than a division by nothing.
	snap.Totals.Pending = nodes * 2
	snap.Totals.Unschedulable = nodes / 2
	return snap
}

func newTestModel(t *testing.T, w, h, nodes int) *Model {
	t.Helper()
	m := New(Config{FPS: 20, Legend: true})
	m.w, m.h = w, h
	m.applySnapshot(testSnapshot(nodes))
	return m
}

func TestFrameGeometry(t *testing.T) {
	sizes := []struct{ w, h int }{
		{200, 60}, {160, 48}, {120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 14},
	}
	nodeCounts := []int{0, 1, 3, 14, 60}
	modes := []Mode{ModePods, ModeNodes, ModeDense}

	for _, size := range sizes {
		for _, count := range nodeCounts {
			for _, mode := range modes {
				m := newTestModel(t, size.w, size.h, count)
				m.setMode(mode)
				// Advance past the entry animation so the steady state is what
				// gets measured, then check a mid-animation frame separately.
				for i := 0; i < 40; i++ {
					m.reg.Advance(25 * time.Millisecond)
				}
				assertFrame(t, m, size.w, size.h, mode, count, "settled")

				// A frame captured mid-animation must be exactly as rigid.
				mid := newTestModel(t, size.w, size.h, count)
				mid.setMode(mode)
				mid.reg.Advance(120 * time.Millisecond)
				assertFrame(t, mid, size.w, size.h, mode, count, "animating")
			}
		}
	}
}

func TestPhaseChangeTriggersOneShotHighlight(t *testing.T) {
	m := newTestModel(t, 120, 36, 1)
	for i := 0; i < 40; i++ {
		m.reg.Advance(25 * time.Millisecond)
	}
	next := testSnapshot(1)
	next.Nodes[0].Phase = model.PhaseDraining
	m.applySnapshot(next)
	if got := m.reg.Node(next.Nodes[0].Name).Flash; got != 1 {
		t.Fatalf("phase transition flash = %v, want 1", got)
	}
}

func TestRewindSeekOverlayIsCenteredAndAccumulates(t *testing.T) {
	m := newTestModel(t, 100, 30, 3)
	m.showSeekOverlay(5 * time.Second)
	m.showSeekOverlay(5 * time.Second)
	frame := m.View()
	label := "− 10 seconds"
	if !strings.Contains(frame, label) {
		t.Fatalf("rewind frame is missing %q", label)
	}
	assertFrame(t, m, m.w, m.h, m.mode, len(m.vis), "rewind overlay")

	wantX := (m.w - runewidth.StringWidth(label)) / 2
	wantY := m.h / 2
	foundY := -1
	for y, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		if byteX := strings.Index(plain, "−"); byteX >= 0 {
			foundY = y
			x := runewidth.StringWidth(plain[:byteX])
			if x != wantX {
				t.Errorf("overlay starts at column %d, want %d", x, wantX)
			}
			break
		}
	}
	if foundY < wantY-1 || foundY > wantY {
		t.Errorf("overlay text is on row %d, want the centre near %d", foundY, wantY)
	}

	m.seekOverlayAt = time.Now().Add(-seekOverlayTTL)
	if expired := m.View(); strings.Contains(expired, label) {
		t.Fatal("expired rewind overlay is still visible")
	}
}

func assertFrame(t *testing.T, m *Model, w, h int, mode Mode, count int, when string) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != h {
		t.Fatalf("%s mode=%v nodes=%d %dx%d: got %d lines, want %d", when, mode, count, w, h, len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Fatalf("%s mode=%v nodes=%d %dx%d: line %d width %d, want %d\n%q",
				when, mode, count, w, h, i, got, w, line)
		}
	}
}

func TestFrameGeometryWithOverlays(t *testing.T) {
	m := newTestModel(t, 120, 40, 12)
	m.showLegend = false
	m.derive()
	assertFrame(t, m, 120, 40, m.mode, 12, "no legend")

	// Command bar open with a full completion list is the tallest chrome.
	m.bar.open("")
	m.bar.refresh(m)
	m.derive()
	assertFrame(t, m, 120, 40, m.mode, 12, "cmdbar")

	m.bar.close()
	m.showHelp = true
	m.derive()
	assertFrame(t, m, 120, 40, m.mode, 12, "help")
}

func TestFrameGeometryTinyTerminal(t *testing.T) {
	// Smaller than any box: must still emit a well-formed frame rather than
	// panicking or producing ragged lines.
	for _, size := range []struct{ w, h int }{{20, 8}, {30, 6}, {10, 5}} {
		m := newTestModel(t, size.w, size.h, 5)
		assertFrame(t, m, size.w, size.h, m.mode, 5, "tiny")
	}
}

func TestPodCellsNeverExceedArea(t *testing.T) {
	snap := testSnapshot(20)
	f := Filter{}
	vs := f.Apply(snap, SortName, false)
	for _, v := range vs {
		total := 30
		sizes, _ := podCellSizes(v, total)
		if sum := sumInts(sizes); sum > total {
			t.Fatalf("node %s: pod cells sum to %d, area is %d", v.node.Name, sum, total)
		}
	}
}

func TestShortenKeepsTail(t *testing.T) {
	got := shorten("ip-10-0-142-77.us-west-2.compute.internal", 12)
	if lipgloss.Width(got) != 12 {
		t.Fatalf("width %d, want 12: %q", lipgloss.Width(got), got)
	}
	if !strings.HasSuffix(got, "internal") {
		t.Fatalf("want the distinguishing tail preserved, got %q", got)
	}
	if shorten("short", 12) != "short" {
		t.Fatal("names that fit must not be modified")
	}
}

func TestCanvasWidthWithWideRunes(t *testing.T) {
	c := newCanvas(10, 1, "#000000", "#ffffff")
	c.text(0, 0, "日本語ab", "#ffffff", false)
	if got := lipgloss.Width(c.String()); got != 10 {
		t.Fatalf("wide runes desynchronised the grid: width %d, want 10", got)
	}
}

func TestMeterFractionClamped(t *testing.T) {
	c := newCanvas(10, 1, "#000000", "#ffffff")
	// Over- and under-committed nodes are real; the meter must not write out of
	// bounds for either.
	c.hMeter(0, 0, 10, 4.2, "#ff0000", "#111111")
	c.hMeter(0, 0, 10, -3, "#ff0000", "#111111")
	c.vMeter(0, 0, 10, 1, 2.5, "#ff0000", "#111111")
	if got := lipgloss.Width(c.String()); got != 10 {
		t.Fatalf("width %d, want 10", got)
	}
}
