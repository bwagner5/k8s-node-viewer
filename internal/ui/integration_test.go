package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/source/fake"
)

// TestEndToEnd drives the real pipeline — simulated cluster, store, snapshot
// channel, bubbletea model — with no terminal involved. It is the closest thing
// to running the binary that can live in CI, and it is what catches wiring bugs
// that per-package tests miss.
func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cluster, store := fake.New(fake.Options{Nodes: 10, Seed: 42, DrainFor: 200 * time.Millisecond})
	snaps := store.Watch(ctx, 20*time.Millisecond)

	m := New(Config{Snapshots: snaps, Demo: cluster, FPS: 20, Legend: true, HasMetrics: true})
	run(t, m, tea.WindowSizeMsg{Width: 150, Height: 42})

	// Drive the simulation and the event loop together for a while, exercising
	// provisioning, pod churn and drains, and rendering every step.
	cluster.ScaleUp(3)
	cluster.DrainOne()
	deadline := time.Now().Add(3 * time.Second)
	frames, snapshots := 0, 0
	for time.Now().After(deadline) == false {
		select {
		case snap := <-snaps:
			run(t, m, snapshotMsg{snap})
			snapshots++
		default:
			cluster.Churn()
			run(t, m, frameMsg(time.Now()))
			frames++
		}
		if frames%17 == 0 {
			cluster.ScaleUp(1)
		}
		if frames%23 == 0 {
			cluster.DrainOne()
		}
		assertFrame(t, m, 150, 42, m.mode, len(m.vis), "e2e")
		if frames > 400 {
			break
		}
	}
	if snapshots == 0 {
		t.Fatal("no snapshots reached the model")
	}
	if len(m.vis) == 0 {
		t.Fatal("model has no visible nodes")
	}

	// Every mode must survive the same live data.
	for _, mode := range []Mode{ModeNodes, ModeDense, ModePods} {
		m.setMode(mode)
		for i := 0; i < 10; i++ {
			run(t, m, frameMsg(time.Now()))
			assertFrame(t, m, 150, 42, mode, len(m.vis), "e2e mode switch")
		}
	}

	// Interactive filtering against live data.
	if _, err := m.Run(":nodepool general"); err != nil {
		t.Fatal(err)
	}
	m.derive()
	for _, v := range m.vis {
		if v.node.NodePool != "general" && v.node.Phase.String() != "Gone" {
			t.Fatalf("node %s survived the nodepool filter", v.node.Name)
		}
	}
	assertFrame(t, m, 150, 42, m.mode, len(m.vis), "e2e filtered")

	// Quitting must produce a Quit command, not just set a flag.
	if !quits(t, m) {
		t.Fatal("q did not return tea.Quit")
	}
}

// run feeds one message through Update. The returned command is discarded: the
// test drives frame ticks and channel reads itself rather than letting the
// bubbletea runtime schedule them.
func run(t *testing.T, m *Model, msg tea.Msg) {
	t.Helper()
	m.Update(msg)
}

func quits(t *testing.T, m *Model) bool {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestResizeMidAnimation(t *testing.T) {
	// Terminal resizes during a demo are routine (projector handshakes, tmux
	// splits) and must never produce a malformed frame.
	m := newTestModel(t, 160, 44, 20)
	for _, size := range []struct{ w, h int }{{80, 24}, {200, 60}, {40, 12}, {120, 30}} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m.reg.Advance(50 * time.Millisecond)
		assertFrame(t, m, size.w, size.h, m.mode, len(m.vis), "resized")
	}
}

func TestSelectionSurvivesResort(t *testing.T) {
	m := newTestModel(t, 160, 44, 12)
	m.setCursor(5)
	want := m.cursorName
	if want == "" {
		t.Fatal("no selection")
	}
	// Re-sorting must keep the highlight on the same node: during a demo the
	// cursor is how you point at something.
	m.sortKey = SortCPU
	m.derive()
	if m.cursorName != want {
		t.Fatalf("selection moved from %s to %s", want, m.cursorName)
	}
	if m.selected().node.Name != want {
		t.Fatalf("cursor index no longer points at %s", want)
	}
}

func TestStatusBarShowsErrors(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)
	m.execute("nodepool nonexistent")
	out := m.renderStatus(160)
	// TestMain pins the Ascii colour profile, so the rendered line is plain text.
	if !strings.Contains(out, "nonexistent") {
		t.Fatalf("error not surfaced in the status bar: %q", out)
	}
}
