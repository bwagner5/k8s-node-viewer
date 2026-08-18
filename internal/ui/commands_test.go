package ui

import (
	"errors"
	"strings"
	"testing"

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

func TestPhaseCommandNamesMatchModel(t *testing.T) {
	// These two lists are index-aligned by hand; drift would silently make
	// ":phase draining" filter for the wrong state.
	for i, name := range phaseCommandNames {
		if got := strings.ToLower(model.Phase(i).String()); got != name {
			t.Fatalf("phase %d: command name %q, model name %q", i, name, got)
		}
	}
	if len(phaseCommandNames) != len(phaseNamesForTest()) {
		t.Fatal("phase list lengths differ")
	}
}

func phaseNamesForTest() []string {
	var out []string
	for p := model.PhaseProvisioning; ; p++ {
		s := p.String()
		if s == "Unknown" {
			return out
		}
		out = append(out, s)
	}
}

func TestCommandsAffectViewState(t *testing.T) {
	m := newTestModel(t, 160, 44, 6)
	m.hasMetrics = true

	for _, tc := range []struct {
		line  string
		check func() bool
	}{
		{":nodepool general", func() bool { return m.filter.NodePool == "general" }},
		{":nodepool all", func() bool { return m.filter.NodePool == "" }},
		{":ns ml", func() bool { return m.filter.Namespace == "ml" }},
		{":mode nodes", func() bool { return m.mode == ModeNodes }},
		{":pods on", func() bool { return m.mode == ModePods }},
		{":pods off", func() bool { return m.mode == ModeNodes }},
		{":sort cpu", func() bool { return m.sortKey == SortCPU }},
		{":util usage", func() bool { return m.basis == model.BasisUsage }},
		{":ds off", func() bool { return m.filter.HideDaemonSets }},
		{":only on", func() bool { return m.filter.Only }},
		{":node ip-10", func() bool { return m.filter.NodeQuery == "ip-10" }},
		{":phase draining", func() bool { return m.filter.Phases[model.PhaseDraining] }},
		{":phase draining", func() bool { return m.filter.Phases == nil }}, // toggles back off
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

	m.hasMetrics = false
	if _, err := m.Run(":util usage"); err == nil {
		t.Fatal(":util usage should be refused without metrics-server")
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

func TestFuzzyRanking(t *testing.T) {
	got := rank([]string{"general", "spot-batch", "gpu"}, "spt")
	if len(got) == 0 || got[0] != "spot-batch" {
		t.Fatalf("fuzzy match failed: %v", got)
	}
}

func TestNamespaceFilterHighlightsRatherThanHides(t *testing.T) {
	snap := testSnapshot(6)
	f := Filter{Namespace: "ml"}

	vs := f.Apply(snap, SortName, false)
	if len(vs) != len(snap.Nodes) {
		t.Fatal("a namespace filter must not remove nodes by default")
	}
	sawUnmatched := false
	for _, v := range vs {
		for i, p := range v.pods {
			if v.matched[i] != (p.Namespace == "ml") {
				t.Fatalf("pod %s match flag is wrong", p.Name)
			}
			if !v.matched[i] {
				sawUnmatched = true
			}
		}
	}
	if !sawUnmatched {
		t.Fatal("test data has no pods outside the filtered namespace")
	}

	// With :only on, nodes with nothing matching drop out.
	f.Only = true
	only := f.Apply(snap, SortName, false)
	for _, v := range only {
		if v.matchCount == 0 && v.node.Phase != model.PhaseGone {
			t.Fatalf("node %s survived :only with no matching pods", v.node.Name)
		}
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
