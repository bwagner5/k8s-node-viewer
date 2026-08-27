package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// The bug these guard against: tiny phase swatches required matching a hairline
// colour to a card border. Nodes now use labelled status chips, while pod cells
// carry one meaning only — state — and the legend mirrors both directly.

func TestEveryPodStateHasALegendEntry(t *testing.T) {
	labelled := map[string]bool{}
	for _, st := range podStates {
		labelled[st.label] = true
	}
	for _, phase := range []model.PodPhase{model.PodPending, model.PodRunning, model.PodTerminating, model.PodFailed} {
		if !labelled[strings.ToLower(phase.String())] {
			t.Errorf("pod phase %v is drawable but the legend never names it", phase)
		}
	}
}

func TestPodGlyphAndColourAgreeWithLegend(t *testing.T) {
	m := newTestModel(t, 200, 44, 8)
	ctx := boxCtx{reg: m.reg, mode: m.mode}
	byLabel := map[string]rune{}
	for _, st := range podStates {
		byLabel[st.label] = st.glyph
	}
	for _, v := range m.vis {
		for _, p := range v.pods {
			label := strings.ToLower(p.Phase.String())
			if got := podGlyph(p); got != byLabel[label] {
				t.Fatalf("pod %s (%s) draws glyph %q, legend shows %q", p.Name, label, got, byLabel[label])
			}
			if p.Phase != model.PodRunning {
				continue // pending/terminating animate away from the flat colour
			}
			m.reg.Pod(p.Key()).Enter = 1
			if got := podColor(p, ctx); got != stateColor(label) {
				t.Fatalf("running pod %s renders %s, legend shows %s", p.Name, got, stateColor(label))
			}
		}
	}
}

func TestPodPaletteDoesNotCollideWithPhaseColours(t *testing.T) {
	// Phase colours live on thin borders and pod colours on large fills; if a
	// pod hue lands on a phase hue the two read as the same thing at a distance.
	for _, th := range []theme.Theme{theme.Dark, theme.Light} {
		pods := map[string]string{
			"running": string(th.PodRunning), "pending": string(th.PodPending),
			"terminating": string(th.PodTerminating), "failed": string(th.PodFailed),
		}
		for name, pod := range pods {
			for phaseIdx, phase := range th.Phase {
				p := model.Phase(phaseIdx)
				if p == model.PhaseGone {
					continue // grey; pods are never grey unless dimmed
				}
				// Terminating pods and dying nodes are meant to rhyme, and a
				// failed pod sharing the terminating red is a feature, not a clash.
				if (name == "terminating" || name == "failed") && (p == model.PhaseTerminating || p == model.PhaseDraining) {
					continue
				}
				if d := colorDistance(t, pod, string(phase)); d < 0.14 {
					t.Errorf("%s theme: pod %s (%s) is %.3f from phase %v (%s) — too close",
						th.Name, name, pod, d, p, phase)
				}
			}
		}
	}
}

func TestLegendIsTwoLabelledRows(t *testing.T) {
	m := newTestModel(t, 200, 44, 8)
	rows := strings.Split(m.renderLegend(200), "\n")
	if len(rows) != legendHeight || legendHeight != 2 {
		t.Fatalf("legend rendered %d rows, want 2", len(rows))
	}
	for i, want := range []string{"node", "pods"} {
		if !strings.Contains(rows[i], want) {
			t.Errorf("legend row %d is not labelled %q: %q", i, want, rows[i])
		}
	}
	// Phase chips must name the state directly and remain distinct from pod-cell
	// glyphs. Colour is reinforcement, not the decoder.
	if strings.ContainsRune(rows[0], glyphRunning) {
		t.Errorf("node row uses the pod-cell glyph: %q", rows[0])
	}
	for _, want := range []string{"DRAINING", "TERMINATING", "CORDONED"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("node row is missing labelled chip %q: %q", want, rows[0])
		}
	}
	for _, st := range podStates {
		if !strings.Contains(strings.ToLower(rows[1]), st.label) {
			t.Errorf("pod row is missing %q: %q", st.label, rows[1])
		}
		if !strings.ContainsRune(rows[1], st.marker) {
			t.Errorf("pod row is missing %q marker %q: %q", st.label, st.marker, rows[1])
		}
	}
}

func TestReadyBorderIsQuieterThanActivePhases(t *testing.T) {
	// A healthy cluster should be calm; only nodes doing something should shout.
	ready := colorDistance(t, string(phaseEdge(model.PhaseReady)), string(theme.Dark.Card))
	draining := colorDistance(t, string(phaseEdge(model.PhaseDraining)), string(theme.Dark.Card))
	if ready >= draining {
		t.Fatalf("ready border (%.3f from card) is not quieter than draining (%.3f)", ready, draining)
	}
}

func TestSelectionDoesNotChangePhaseColour(t *testing.T) {
	m := newTestModel(t, 120, 36, 1)
	n := &model.Node{Name: "node-a", Phase: model.PhaseDraining}
	track := m.reg.Node(n.Name)
	_, plain := borderStyle(n, track, boxCtx{reg: m.reg})
	_, selected := borderStyle(n, track, boxCtx{reg: m.reg, selected: true})
	if plain != selected {
		t.Fatalf("selection changed phase colour from %s to %s", plain, selected)
	}
}

func TestRecentPhaseTrailKeepsFastIntermediateState(t *testing.T) {
	now := time.Now()
	n := &model.Node{Phase: model.PhaseDraining, Transitions: []model.PhaseTransition{
		{Phase: model.PhaseReady, At: now.Add(-3 * time.Second)},
		{Phase: model.PhaseCordoned, At: now.Add(-2 * time.Second)},
		{Phase: model.PhaseDraining, At: now.Add(-time.Second)},
	}}
	if got, want := recentPhaseTrail(n, now, false), "ready → cordoned → draining"; got != want {
		t.Fatalf("trail = %q, want %q", got, want)
	}
	if got := densePhaseState(n, now); !strings.Contains(got, "draining ← cord") {
		t.Fatalf("dense state lost the prior cordon: %q", got)
	}
}

func TestNodeFooterShowsRecentLifecycleWithoutChangingCurrentState(t *testing.T) {
	now := time.Now()
	n := &model.Node{
		Name: "node-a", InstanceType: "m5.large", Phase: model.PhaseDraining,
		Created: now.Add(-time.Hour), Transitions: []model.PhaseTransition{
			{Phase: model.PhaseReady, At: now.Add(-3 * time.Second)},
			{Phase: model.PhaseCordoned, At: now.Add(-2 * time.Second)},
			{Phase: model.PhaseDraining, At: now.Add(-time.Second)},
		},
	}
	m := newTestModel(t, 80, 24, 0)
	m.reg.Node(n.Name).Enter = 1
	box := strings.Join(renderNodeBox(visible{node: n}, 50, 10,
		boxCtx{reg: m.reg, mode: ModePods, now: now}), "\n")
	if !strings.Contains(box, "▼ DRAINING") || !strings.Contains(box, "cordoned → draining") {
		t.Fatalf("card did not preserve current state and recent trail:\n%s", box)
	}
}

func TestRemovedNodeRemainsExpandedBeforeExitAnimation(t *testing.T) {
	now := time.Now()
	n := &model.Node{Phase: model.PhaseGone, DeletedAt: now}
	if removalReadyToCollapse(n, now.Add(time.Second)) {
		t.Fatal("removed node began collapsing before the observation hold elapsed")
	}
	if !removalReadyToCollapse(n, now.Add(removedHold)) {
		t.Fatal("removed node did not begin collapsing after the observation hold")
	}
}

// colorDistance returns the perceptual (Lab) distance between two hex colours.
func colorDistance(t *testing.T, a, b string) float64 {
	t.Helper()
	ca, err := colorful.Hex(a)
	if err != nil {
		t.Fatalf("bad colour %q: %v", a, err)
	}
	cb, err := colorful.Hex(b)
	if err != nil {
		t.Fatalf("bad colour %q: %v", b, err)
	}
	return ca.DistanceLab(cb)
}

func TestNodeCardHasItsOwnBackground(t *testing.T) {
	// The complaint this answers: adjacent nodes were hard to tell apart because
	// a card was just a hairline on the page background. A card now has a body
	// colour, an inset capacity well, and a title strip — all distinct.
	th := theme.Dark
	for _, pair := range []struct{ name, a, b string }{
		{"card vs page", string(th.Card), string(th.Bg)},
		{"well vs card", string(th.Well), string(th.Card)},
		{"title vs card", string(th.Title), string(th.Card)},
	} {
		if colorDistance(t, pair.a, pair.b) < 0.02 {
			t.Errorf("%s: %s and %s are indistinguishable", pair.name, pair.a, pair.b)
		}
	}
}

func TestRenderedCardPaintsCardBackground(t *testing.T) {
	// Guards the wiring, not just the palette: the card colour must actually
	// reach the emitted frame.
	defer lipgloss.SetColorProfile(termenv.Ascii)
	lipgloss.SetColorProfile(termenv.TrueColor)

	m := newTestModel(t, 160, 44, 4)
	for i := 0; i < 40; i++ {
		m.reg.Advance(25 * time.Millisecond)
	}
	frame := m.View()

	r, g, b := mustRGB(t, string(theme.Dark.Card))
	want := fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
	if !strings.Contains(frame, want) {
		t.Fatalf("frame never paints the card background %s (%s)", theme.Dark.Card, want)
	}
}

func mustRGB(t *testing.T, hex string) (int, int, int) {
	t.Helper()
	c, err := colorful.Hex(hex)
	if err != nil {
		t.Fatalf("bad colour %q: %v", hex, err)
	}
	r, g, b := c.RGB255()
	return int(r), int(g), int(b)
}
