package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// headerHeight is fixed so the grid geometry does not change as totals grow.
const headerHeight = 3

// renderHeader draws the title row, the cluster-wide meters, and the counts.
//
// The cluster meters use the same fill primitive as the node boxes on purpose:
// one visual language for "how full is this thing", whether the thing is a node
// or the fleet.
func (m *Model) renderHeader(w int) string {
	t := theme.Current
	snap := m.snap
	c := newCanvas(w, headerHeight, t.Bg, t.Fg)

	title := " k8s-node-viewer "
	c.rect(0, 0, w, 1, t.Panel)
	c.text(0, 0, title, t.Accent, true)
	x := len(title) + 1

	ctxName := snap.Context
	if ctxName == "" {
		ctxName = "unknown context"
	}
	c.text(x, 0, ctxName, t.PanelFg, false)

	// Capability chips: say plainly when a feature is unavailable rather than
	// silently showing zeros.
	chips := []string{"meters: " + m.basis.String()}
	if !snap.HasMetrics {
		chips = append(chips, "no metrics-server")
	}
	if !snap.HasKarpenter {
		chips = append(chips, "no karpenter")
	}
	if m.demo != nil {
		chips = append(chips, "DEMO")
	}
	c.textRight(0, 0, w-1, strings.Join(chips, " · ")+" ", t.Dim, false)

	// Row 1-2: fleet totals with meters.
	totals := snap.Totals
	used := totals.Requests
	if m.basis == model.BasisUsage {
		used = totals.Usage
	}
	cpuFrac, memFrac := used.Frac(totals.Allocatable)
	m.fleet.Target(cpuFrac, memFrac, false)

	// Right-hand block first, so its measured width tells the meters how much
	// room they actually have. Laying out right-to-left is what stops the
	// tallies from being clipped on a narrow terminal.
	counts := m.phaseCounts()
	podLine := fmt.Sprintf("%d pods", totals.Pods)
	stateLine := "all ready"
	stateColor := t.Ok
	var parts []string
	for _, p := range []model.Phase{model.PhaseProvisioning, model.PhaseDraining,
		model.PhaseDeleting, model.PhaseNotReady, model.PhaseCordoned} {
		if counts[p] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[p], phaseAbbrev(p)))
		}
	}
	if len(parts) > 0 {
		stateLine, stateColor = strings.Join(parts, " "), t.Warn
	}
	rightW := max(len(podLine), len(stateLine)) + 2

	// Left block: node count over hourly cost. Its width is measured, not
	// assumed, so a four-digit node count cannot collide with the meter labels.
	nodeLine := fmt.Sprintf("%d nodes", totals.Nodes)
	costLine := ""
	if totals.HourlyCost > 0 {
		costLine = fmt.Sprintf("$%.2f/hr", totals.HourlyCost)
	}
	c.text(1, 1, nodeLine, t.Fg, true)
	c.text(1, 2, costLine, t.Warn, true)

	labelW := max(len(nodeLine), len(costLine)) + 6 // left block + "cpu"/"mem"
	statsW := 22                                    // "65 / 224" style absolute figures
	meterW := max(8, w-labelW-statsW-rightW-4)

	drawHeaderMeter(c, labelW, 1, meterW, "cpu", m.fleet.CPU,
		fmt.Sprintf("%s / %s", model.HumanCPU(used.CPUMilli), model.HumanCPU(totals.Allocatable.CPUMilli)))
	drawHeaderMeter(c, labelW, 2, meterW, "mem", m.fleet.Mem,
		fmt.Sprintf("%s / %s", model.HumanMem(used.MemBytes), model.HumanMem(totals.Allocatable.MemBytes)))

	c.textRight(0, 1, w-1, podLine, t.Fg, true)
	c.textRight(0, 2, w-1, stateLine, stateColor, true)
	return c.String()
}

// phaseAbbrev keeps the header tallies short enough to survive a narrow
// terminal; the legend carries the full names.
func phaseAbbrev(p model.Phase) string {
	switch p {
	case model.PhaseProvisioning:
		return "new"
	case model.PhaseDraining:
		return "drain"
	case model.PhaseDeleting:
		return "term"
	case model.PhaseNotReady:
		return "down"
	case model.PhaseCordoned:
		return "cord"
	default:
		return strings.ToLower(p.String())
	}
}

func drawHeaderMeter(c *canvas, x, y, w int, label string, frac float64, stats string) {
	t := theme.Current
	c.text(x-4, y, label, t.Dim, false)
	c.hMeter(x, y, w, frac, t.Util(frac), t.Empty)
	c.textContrastRight(x, y, w-1, fmt.Sprintf("%.0f%%", frac*100), true)
	c.hMeterTip(x, y, w, frac, t.Util(frac), t.Empty)
	c.text(x+w+1, y, stats, t.Dim, false)
}

// renderStatus is the bottom line: filters, sort, selection detail, and any
// transient message.
func (m *Model) renderStatus(w int) string {
	t := theme.Current
	c := newCanvas(w, 1, t.Panel, t.PanelFg)

	// The mouse readout outranks everything, including the transient messages the
	// zoom itself emits — a diagnostic you can only see between notifications is
	// not a diagnostic.
	if debugMouse {
		c.rect(0, 0, w, 1, t.Panel)
		line := m.lastMouse
		if line == "" {
			line = "KNV_DEBUG_MOUSE: waiting for a mouse event — none received yet"
		}
		c.text(1, 0, truncHead(line, w-2), t.Accent, false)
		return c.String()
	}

	// A message takes over the whole line — an error you cannot see is useless.
	if m.msg != "" && time.Since(m.msgAt) < messageTTL {
		fg, bg := t.PanelFg, t.Panel
		if m.msgErr {
			fg, bg = contrastOn(t.Err), t.Err
		} else {
			bg = theme.Mix(t.Panel, t.Ok, 0.35)
		}
		c.rect(0, 0, w, 1, bg)
		c.text(1, 0, m.msg, fg, true)
		return c.String()
	}

	left := []string{"mode:" + m.mode.String(), "sort:" + m.sortKey.String()}
	if m.sortDesc {
		left[1] += "↓"
	}
	// Only when it is not "fit": a zoomed grid looks like a small cluster, and
	// the chip is the only thing that says otherwise.
	if m.zoom != 0 && m.mode != ModeDense {
		left = append(left, "zoom:"+zoomLabel(m.lay.scale))
	}
	left = append(left, m.filter.Describe()...)
	if !m.filter.Active() {
		left = append(left, "no filters")
	}
	c.text(1, 0, strings.Join(left, "  "), t.PanelFg, false)

	// Selected-node detail, right-aligned: enough to identify what you are
	// pointing at without opening anything.
	if sel := m.selected(); sel != nil {
		n := sel.node
		detail := n.Name
		if n.Zone != "" {
			detail += " · " + n.Zone
		}
		if n.CapacityType != "" {
			detail += " · " + n.CapacityType
		}
		if n.Message != "" {
			detail += " · " + n.Message
		}
		if n.HasPrice {
			detail += fmt.Sprintf(" · $%.3f/hr", n.Price)
		}
		col := t.PhaseColor(int(n.Phase))
		c.textRight(0, 0, w-1, shorten(detail, w/2)+" ", col, true)
	} else {
		c.textRight(0, 0, w-1, "press : for commands · ? for help ", t.Dim, false)
	}
	return c.String()
}

// legendHeight is the row count of the legend strip.
//
// Two rows, because there are exactly two visual languages on screen: the
// border says what the node is doing, the fill says what its pods are doing.
// (This was three rows while pod cells could be coloured by workload; with
// state-only colouring the "colour" and "shape" rows said the same thing, which
// is precisely the clutter that made the picture hard to read.)
const legendHeight = 2

// podStates is the full pod-cell key: colour and glyph together, in one place,
// so the legend and the renderer cannot drift.
var podStates = []struct {
	glyph rune
	label string
}{
	{glyphRunning, "running"},
	{glyphPending, "pending"},
	{glyphTerminating, "terminating"},
	{glyphFailed, "failed"},
}

// renderLegend explains every colour on screen in two labelled rows.
func (m *Model) renderLegend(w int) string {
	t := theme.Current
	c := newCanvas(w, legendHeight, t.Bg, t.Fg)

	const labelCol = 8 // width of the "node"/"pods" row labels

	// Reserve the utilisation ramp on the right first; the chip rows then fill
	// the space to its left and stop cleanly rather than overprinting it.
	const rampPre, rampPost = "meter ", " 0→100%"
	rampW := clampInt(w/6, 0, 22)
	rampX := w - rampW - len(rampPost) - 2
	limit := w - 2
	if rampW >= 8 && rampX-len(rampPre) > labelCol+34 {
		// Drawn as a background fill, exactly as the meters are — using the pod
		// cell glyph here would put a row of █ in the node row and blur the very
		// distinction this legend exists to draw.
		for i := 0; i < rampW; i++ {
			c.rect(rampX+i, 0, 1, 1, t.Util(float64(i)/float64(rampW-1)))
		}
		c.text(rampX-len(rampPre), 0, rampPre, t.Dim, false)
		c.text(rampX+rampW, 0, rampPost, t.Dim, false)
		limit = rampX - len(rampPre) - 2
	}

	// Row 0 — node phase, drawn as the border glyph it actually is, never as the
	// solid block a pod cell uses.
	c.text(1, 0, "node", t.Dim, true)
	x := labelCol
	for _, p := range []model.Phase{model.PhaseReady, model.PhaseProvisioning, model.PhaseCordoned,
		model.PhaseDraining, model.PhaseDeleting, model.PhaseNotReady} {
		label := strings.ToLower(p.String())
		if x+4+len(label) > limit {
			break
		}
		c.text(x, 0, borderSample(p), t.PhaseColor(int(p)), true)
		c.text(x+3, 0, label, t.Dim, false)
		x += 5 + len(label)
	}

	// Row 1 — pod cells: colour and glyph are the same signal, shown together.
	c.text(1, 1, "pods", t.Dim, true)
	x = labelCol
	for _, st := range podStates {
		if x+4+len(st.label) > w-2 {
			break
		}
		c.text(x, 1, string(st.glyph)+string(st.glyph), stateColor(st.label), true)
		c.text(x+3, 1, st.label, t.Dim, false)
		x += 5 + len(st.label)
	}
	if x+24 <= w-2 {
		c.text(x, 1, "· cell size = cpu request", t.Dim, false)
	}
	return c.String()
}

// borderSample is the glyph the legend shows for a phase — the same border
// glyph the node box draws, so the swatch cannot be mistaken for a pod cell.
func borderSample(p model.Phase) string {
	switch p {
	case model.PhaseProvisioning:
		return "┊┊"
	case model.PhaseDraining, model.PhaseDeleting, model.PhaseNotReady:
		return "┃┃"
	default:
		return "││"
	}
}

// helpText is rendered into the overlay. Generated from the registry so it can
// never drift from what the commands actually are.
func helpLines(includeDemo bool) []string {
	out := []string{
		"KEYS",
		"  ↑↓←→ / hjkl   move selection          :        command bar",
		"  pgup pgdn     scroll                  /        filter nodes by name",
		"  g / G         first / last            \\        clear all filters",
		"  p             pods ⇄ utilisation      d        dense table mode",
		"  u             requests ⇄ usage        s / S    cycle sort / reverse",
		"  l             toggle legend           ?        this help",
		"  z / Z         zoom in / out           0        zoom to fit",
		"  q             quit",
		"",
		"MOUSE / TRACKPAD",
		"  two-finger scroll            zoom on the node under the pointer",
		"                               (a pinch never reaches a terminal app)",
		"  ctrl / option / shift +      scroll the grid",
		"    two-finger scroll          (the selection comes with it)",
		"  click / drag                 select a node",
		"",
	}
	if includeDemo {
		out = append(out,
			"DEMO KEYS (simulated cluster only)",
			"  +   scale up one node      -   drain a node      x   churn pods",
			"")
	}
	out = append(out, "COMMANDS")
	for i := range registry {
		cmd := &registry[i]
		if cmd.demoOnly && !includeDemo {
			continue
		}
		name := ":" + cmd.name
		if cmd.argHint != "" {
			name += " " + cmd.argHint
		}
		alias := ""
		if len(cmd.aliases) > 0 {
			alias = "(" + strings.Join(cmd.aliases, ", ") + ")"
		}
		out = append(out, fmt.Sprintf("  %-34s %-16s %s", name, alias, cmd.help))
	}
	return out
}

// helpRows is how many text rows a help panel of this height can show.
func helpRows(panelH int) int { return max(0, panelH-3) }

// helpPanelH is the panel height for a terminal of height h.
func helpPanelH(h, lines int) int { return min(h-2, lines+4) }

// helpViewRows is how many lines of help are on screen at once, and so how far
// a page key should jump.
func (m *Model) helpViewRows() int {
	lines := len(helpLines(m.demo != nil))
	return max(1, helpRows(helpPanelH(m.h, lines)))
}

// helpMaxScroll is how far the overlay can scroll before the last line is on
// screen. Zero means everything already fits.
func (m *Model) helpMaxScroll() int {
	return max(0, len(helpLines(m.demo != nil))-m.helpViewRows())
}

// renderHelp draws the help overlay as a centred panel.
//
// It scrolls, because it no longer fits: on a 44-row terminal the help is three
// lines longer than the panel, and an overlay that quietly drops its last three
// commands is worse than one that admits there is more to see.
func (m *Model) renderHelp(w, h int) string {
	t := theme.Current
	lines := helpLines(m.demo != nil)

	panelW := min(w-4, 120)
	panelH := helpPanelH(h, len(lines))
	scroll := clampInt(m.helpScroll, 0, m.helpMaxScroll())

	// Build the panel as its own canvas and composite it, which keeps every
	// drawing helper working in local coordinates.
	panel := newCanvas(panelW, panelH, t.Panel, t.PanelFg)
	drawBorder(panel, panelW, panelH, borderSolid, t.Accent, t.Panel)
	panel.text(2, 0, " help ", t.Accent, true)

	footer := " esc / ? to close "
	if m.helpMaxScroll() > 0 {
		remaining := m.helpMaxScroll() - scroll
		footer = fmt.Sprintf(" ↑↓ scroll · %d more below · esc to close ", remaining)
		if remaining == 0 {
			footer = " ↑↓ scroll · end · esc to close "
		}
	}
	panel.textRight(0, panelH-1, panelW-2, footer, t.Dim, false)

	for i, line := range lines[min(scroll, len(lines)):] {
		y := 2 + i
		if y >= panelH-1 {
			break
		}
		fg, bold := t.PanelFg, false
		// Section headings are the all-caps lines.
		if line != "" && line == strings.ToUpper(line) && !strings.HasPrefix(line, " ") {
			fg, bold = t.Accent, true
		}
		// Help text truncates from the right, unlike node names: the start of a
		// sentence is the informative part.
		panel.text(2, y, truncHead(line, panelW-4), fg, bold)
	}

	c := newCanvas(w, h, t.Bg, t.Fg)
	blit(c, panel, (w-panelW)/2, max(0, (h-panelH)/2), 1)
	return c.String()
}
