package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

func TestRegistryIsWellFormed(t *testing.T) {
	seen := map[string]string{}
	for i := range registry {
		cmd := &registry[i]
		if cmd.help == "" {
			t.Errorf("%s: no help text; it would show as a blank row in the overlay", cmd.name)
		}
		if cmd.run == nil {
			t.Errorf("%s: no run function", cmd.name)
		}
		if (cmd.arg == argNone) != (cmd.argHint == "") {
			t.Errorf("%s: arg kind and argHint disagree", cmd.name)
		}
		for _, name := range append([]string{cmd.name}, cmd.aliases...) {
			if prev, ok := seen[name]; ok {
				t.Errorf("%q is claimed by both %s and %s", name, prev, cmd.name)
			}
			seen[name] = cmd.name
		}
	}
}

func TestCommandsAffectViewState(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)

	for _, tc := range []struct {
		line  string
		check func() bool
	}{
		{":nodepool general", func() bool { return m.filter.NodePool == "general" }},
		{":nodepool all", func() bool { return m.filter.NodePool == "" }},
		{":mode nodes", func() bool { return m.mode == ModeNodes }},
		{":sort cpu", func() bool { return m.sortKey == SortCPU }},
		{":node ip-10", func() bool { return m.filter.NodeQuery == "ip-10" }},
		{":clear", func() bool { return !m.filter.Active() }},
	} {
		if _, err := m.Run(tc.line); err != nil {
			t.Fatalf("%s: %v", tc.line, err)
		}
		if !tc.check() {
			t.Fatalf("%s: did not take effect", tc.line)
		}
	}
}

func TestCommandErrors(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)

	if _, err := m.Run(":nope"); err == nil {
		t.Fatal("unknown command should error")
	}
	if _, err := m.Run(":nodepool does-not-exist"); err == nil {
		t.Fatal("unknown nodepool should error rather than filtering everything away")
	}
	if _, err := m.Run(":node ["); err == nil {
		t.Fatal("invalid regex should error")
	}
	// An argument-taking command with no argument opens the picker instead of
	// failing; that is what makes ":nodepool<Enter>" useful.
	if _, err := m.Run(":nodepool"); !errors.Is(err, errNeedsArg) {
		t.Fatalf("want errNeedsArg, got %v", err)
	}
	// Demo commands must be refused against a real cluster.
	if _, err := m.Run(":drain"); err == nil {
		t.Fatal(":drain should be refused when not in demo mode")
	}

}

func TestPlaybackCommandsDistinguishOneSpeedFromRealtime(t *testing.T) {
	m := newTestModel(t, 160, 44, 3)
	now := time.Now()
	m.playback.latest = &model.Snapshot{Generation: 99, Taken: now.Add(10 * time.Second)}

	if _, err := m.Run(":speed 0.5x"); err != nil {
		t.Fatal(err)
	}
	m.playback.queue = append(m.playback.queue, m.playback.latest)
	if _, err := m.Run(":speed 1"); err != nil {
		t.Fatal(err)
	}
	if m.playback.live || m.playback.speed != 1 || len(m.playback.queue) != 1 {
		t.Fatalf("1x should preserve delayed playback: live=%v speed=%v queue=%d",
			m.playback.live, m.playback.speed, len(m.playback.queue))
	}

	if _, err := m.Run(":speed realtime"); err != nil {
		t.Fatal(err)
	}
	if !m.playback.live || len(m.playback.queue) != 0 || m.snap.Generation != 99 {
		t.Fatalf("realtime did not flush to latest: live=%v queue=%d generation=%d",
			m.playback.live, len(m.playback.queue), m.snap.Generation)
	}
}

func TestPlaybackRewindCommandUsesRollingLiveHistory(t *testing.T) {
	m := newTestModel(t, 160, 44, 3)
	now := time.Now()
	old := &model.Snapshot{Generation: 1, Taken: now.Add(-10 * time.Second)}
	current := &model.Snapshot{Generation: 2, Taken: now}
	for _, item := range []struct {
		snap *model.Snapshot
		at   time.Time
	}{{old, old.Taken}, {current, current.Taken}} {
		m.playback.Ingest(item.snap, item.at)
		m.applySnapshot(item.snap)
	}

	msg, err := m.Run(":rewind 5s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "rewound") || m.playback.live || m.snap.Generation != 1 || len(m.playback.queue) != 1 {
		t.Fatalf("rewind command: msg=%q live=%v generation=%d queue=%d",
			msg, m.playback.live, m.snap.Generation, len(m.playback.queue))
	}
	if _, err := m.Run(":rewind 0s"); err == nil {
		t.Fatal("zero-duration rewind succeeded")
	}
}

func TestFrameDelayDoesNotDistortPlaybackSpeed(t *testing.T) {
	m := newTestModel(t, 120, 40, 1)
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.Local)
	m.last = start
	m.playback.live = false
	m.playback.speed = .5
	m.playback.now = start

	m.Update(frameMsg(start.Add(2 * time.Second)))
	if want := start.Add(time.Second); m.playback.now != want {
		t.Fatalf("2s frame delay at 0.5x advanced to %s, want %s", m.playback.now, want)
	}
}

func TestRewindSnapsMetersToTheSoughtSnapshot(t *testing.T) {
	m := New(Config{FPS: 20, Legend: true})
	m.w, m.h = 160, 44
	now := time.Now()
	old := testSnapshot(1)
	old.Generation, old.Taken = 1, now.Add(-10*time.Second)
	old.Nodes[0].Requests = model.Resources{CPUMilli: 1000, MemBytes: 4 << 30, Pods: 1}
	old.Totals.Requests = old.Nodes[0].Requests
	current := testSnapshot(1)
	current.Generation, current.Taken = 2, now
	current.Nodes[0].Requests = model.Resources{CPUMilli: 12000, MemBytes: 48 << 30, Pods: 1}
	current.Totals.Requests = current.Nodes[0].Requests

	for _, item := range []struct {
		snap *model.Snapshot
		at   time.Time
	}{{old, old.Taken}, {current, current.Taken}} {
		m.playback.Ingest(item.snap, item.at)
		m.applySnapshot(item.snap)
		m.View()
		m.reg.Advance(time.Second)
		m.fleet.Step(time.Second)
	}
	if m.reg.Node(current.Nodes[0].Name).CPU < .7 {
		t.Fatal("test did not settle on the newer high-utilisation meter")
	}

	if _, moved := m.rewindPlayback(5 * time.Second); moved == 0 {
		t.Fatal("rewind found no history")
	}
	m.View() // establishes meter targets from the sought snapshot
	nodeCPU, _ := old.Nodes[0].Util()
	fleetCPU, _ := old.Totals.Requests.Frac(old.Totals.Allocatable)
	if got := m.reg.Node(old.Nodes[0].Name).CPU; got != nodeCPU {
		t.Fatalf("node meter after rewind = %v, want %v", got, nodeCPU)
	}
	if got := m.fleet.CPU; got != fleetCPU {
		t.Fatalf("fleet meter after rewind = %v, want %v", got, fleetCPU)
	}
}

func TestPlaybackKeysAreGlobal(t *testing.T) {
	m := newTestModel(t, 120, 40, 3)
	m.detail = &detailView{}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.playback.live || m.playback.speed != 0 {
		t.Fatalf("p did not pause from detail pane: live=%v speed=%v", m.playback.live, m.playback.speed)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.playback.live || m.playback.speed != 1 {
		t.Fatalf("p did not resume delayed 1x: live=%v speed=%v", m.playback.live, m.playback.speed)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !m.playback.live {
		t.Fatal("r did not return to realtime from detail pane")
	}
}

func TestCommandBarCompletion(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)

	m.bar.open("")
	m.bar.insert("np")
	m.bar.refresh(m)
	if len(m.bar.items) == 0 {
		t.Fatal("no completions for a command prefix")
	}
	// Completing a command that takes an argument must leave the bar open with a
	// trailing space, ready for the argument's completions.
	if m.bar.accept() {
		t.Fatal("accepting an argument-taking command should not run it")
	}
	if !strings.HasSuffix(m.bar.input, " ") {
		t.Fatalf("want a trailing space after completion, got %q", m.bar.input)
	}

	m.bar.refresh(m)
	if len(m.bar.items) == 0 {
		t.Fatal("no nodepool candidates offered")
	}
	found := false
	for _, it := range m.bar.items {
		if it == "general" {
			found = true
		}
	}
	if !found {
		t.Fatalf("nodepool candidates %v do not include a known pool", m.bar.items)
	}
}

func TestRemovedCommandsAreUnavailable(t *testing.T) {
	removed := []string{
		"capacity", "cap", "namespace", "ns", "only", "owner", "app", "workload",
		"phase", "state", "pods", "type", "instance", "util", "basis", "daemonsets", "ds",
	}
	names := commandNames(false)
	m := newTestModel(t, 160, 44, 6)
	for _, removedName := range removed {
		if _, err := m.Run(":" + removedName); err == nil {
			t.Errorf("removed command %q is still callable", removedName)
		}
	}
	for _, name := range names {
		for _, removedName := range removed {
			if name == removedName {
				t.Errorf("removed command %q appears in completion menu", name)
			}
		}
	}
	for _, line := range helpLines(false) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, removedName := range removed {
			if fields[0] == ":"+removedName {
				t.Errorf("removed command %q appears in help", removedName)
			}
		}
	}
}

func TestCommandListEndsWithClearThenHelp(t *testing.T) {
	for _, includeDemo := range []bool{false, true} {
		names := commandNames(includeDemo)
		if len(names) < 2 || names[len(names)-2] != "clear" || names[len(names)-1] != "help" {
			t.Fatalf("includeDemo=%v: command list ends with %v, want [clear help]", includeDemo, names)
		}

		lines := helpLines(includeDemo)
		if len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-2]), ":clear ") ||
			!strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), ":help ") {
			t.Fatalf("includeDemo=%v: help command list does not end with clear then help: %v",
				includeDemo, lines[max(0, len(lines)-2):])
		}
	}
}

func TestVKeyCyclesAllViewModes(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)
	for _, want := range []Mode{ModeNodes, ModeDense, ModePods} {
		m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		if m.mode != want {
			t.Fatalf("v selected %s, want %s", m.mode, want)
		}
	}
}

func TestFuzzyRanking(t *testing.T) {
	got := rank([]string{"general", "spot-batch", "gpu"}, "spt")
	if len(got) == 0 || got[0] != "spot-batch" {
		t.Fatalf("fuzzy match failed: %v", got)
	}
}

func TestSortIsStableOnTies(t *testing.T) {
	snap := testSnapshot(10)
	f := Filter{}
	// Equal sort values must fall back to name, or boxes swap places between
	// frames and the grid appears to shuffle at random.
	for _, n := range snap.Nodes {
		n.Requests = model.Resources{}
	}
	first := f.Apply(snap, SortCPU, false)
	second := f.Apply(snap, SortCPU, false)
	for i := range first {
		if first[i].node.Name != second[i].node.Name {
			t.Fatalf("position %d: %s then %s", i, first[i].node.Name, second[i].node.Name)
		}
		if i > 0 && first[i-1].node.Name > first[i].node.Name {
			t.Fatalf("tie-break is not by name: %s before %s", first[i-1].node.Name, first[i].node.Name)
		}
	}
}
