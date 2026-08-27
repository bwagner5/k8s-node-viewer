package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
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

	m := New(Config{Snapshots: snaps, Demo: cluster, FPS: 20, Legend: true})
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
		if v.node.NodePool != "general" && v.node.Phase != model.PhaseGone {
			t.Fatalf("node %s survived the nodepool filter", v.node.Name)
		}
	}
	assertFrame(t, m, 150, 42, m.mode, len(m.vis), "e2e filtered")

	// Quitting must produce a Quit command, not just set a flag.
	if !quits(t, m) {
		t.Fatal("q did not return tea.Quit")
	}
}

// TestDetailPaneAgainstSimulatedCluster drives the pane through the real wiring:
// simulated source as the Describer, its own recorded history, no stubs. It is
// what catches the pane being connected to nothing.
func TestDetailPaneAgainstSimulatedCluster(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cluster, store := fake.New(fake.Options{Nodes: 8, Seed: 21})
	snaps := store.Watch(ctx, 20*time.Millisecond)

	m := New(Config{Snapshots: snaps, Demo: cluster, Describe: cluster, FPS: 20, Legend: true})
	run(t, m, tea.WindowSizeMsg{Width: 150, Height: 44})

	// The simulation seeds itself when it starts running, so wait for a snapshot
	// that has nodes in it rather than for the first one.
	go func() { _ = cluster.Run(ctx) }()
	// Wait for a registered node rather than for any box at all: the first
	// snapshots can be all provisioning placeholders, and a placeholder has no
	// kubelet history to assert on.
	deadline := time.Now().Add(3 * time.Second)
	ready := -1
	for ready < 0 {
		if time.Now().After(deadline) {
			t.Fatal("no ready node to describe")
		}
		run(t, m, snapshotMsg{<-snaps})
		for i, v := range m.vis {
			if v.node.Phase == model.PhaseReady {
				ready = i
				break
			}
		}
	}

	m.setCursor(ready)
	name := m.cursorName
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not start a fetch against the simulated source")
	}
	run(t, m, cmd())

	if m.detail == nil || m.detail.detail == nil {
		t.Fatalf("pane has no detail: %+v", m.detail)
	}
	if m.detail.err != nil {
		t.Fatalf("fetch failed: %v", m.detail.err)
	}
	out := m.View()
	for _, want := range []string{name, "EVENTS", "Launched", "NodeReady", "CONDITIONS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pane is missing %q\n%s", want, out)
		}
	}
	assertFrame(t, m, 150, 44, m.mode, len(m.vis), "simulated detail")

	run(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.detail != nil {
		t.Fatal("esc did not close the pane")
	}
	assertFrame(t, m, 150, 44, m.mode, len(m.vis), "back to the grid")
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
