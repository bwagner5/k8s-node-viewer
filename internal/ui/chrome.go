package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// headerHeight is fixed so the grid geometry does not change as totals grow.
// Four rows: title, cpu, mem, pending.
const headerHeight = 4

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
	var chips []string
	if !snap.HasKarpenter {
		chips = append(chips, "no karpenter")
	}
	if m.demo != nil {
		chips = append(chips, "DEMO")
	}
	if len(chips) > 0 {
		c.textRight(0, 0, w-1, strings.Join(chips, " · ")+" ", t.Dim, false)
	}

	// Row 1-2: fleet totals with meters.
	totals := snap.Totals
	used := totals.Requests
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
		model.PhaseTerminating, model.PhaseNotReady, model.PhaseCordoned} {
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
	meterW := max(8, w-labelW-statsW-rightW-pctW-4)

	drawHeaderMeter(c, labelW, 1, meterW, "cpu", m.fleet.CPU,
		fmt.Sprintf("%s / %s", model.HumanCPU(used.CPUMilli), model.HumanCPU(totals.Allocatable.CPUMilli)))
	drawHeaderMeter(c, labelW, 2, meterW, "mem", m.fleet.Mem,
		fmt.Sprintf("%s / %s", model.HumanMem(used.MemBytes), model.HumanMem(totals.Allocatable.MemBytes)))
	// The backlog row has the right-hand block to itself — the pod and state
	// tallies sit on the rows above — so its figures may spill into that space
	// and spell "unschedulable" out in full.
	m.drawPendingMeter(c, labelW, 3, meterW, statsW+rightW)

	c.textRight(0, 1, w-1, podLine, t.Fg, true)
	c.textRight(0, 2, w-1, stateLine, stateColor, true)
	return c.String()
}

// drawPendingMeter is the third header row: the scheduling backlog, in the same
// visual language as the meters above it.
//
// Two things are true at once and both have to be readable from the back of a
// room: how much work is waiting, and how much of it the scheduler has given up
// on. So it is one bar with two segments — refused pods first, in the error
// colour, then merely-waiting pods in the warning colour — measured against
// every pod in the cluster, placed or not. The denominator is what makes it a
// meter rather than a counter: "40 pending" means nothing without knowing
// whether the cluster runs 50 pods or 5000.
func (m *Model) drawPendingMeter(c *canvas, x, y, w, statsW int) {
	t := theme.Current
	totals := m.snap.Totals
	pending, unsched := totals.Pending, totals.Unschedulable

	all := float64(totals.Pods + pending)
	var pendFrac, unschedFrac float64
	if all > 0 {
		pendFrac, unschedFrac = float64(pending)/all, float64(unsched)/all
	}
	// A handful of pending pods on a large cluster is a genuinely small
	// fraction, but "too small to draw" and "none" must not look the same, so
	// anything non-zero claims at least one cell.
	if w > 0 {
		if pending > 0 {
			pendFrac = max(pendFrac, 1/float64(w))
		}
		if unsched > 0 {
			unschedFrac = max(unschedFrac, 1/float64(w))
		}
	}
	m.pend.Target(pendFrac, unschedFrac, false)

	label, labelColor := "pend", t.Dim
	switch {
	case unsched > 0:
		labelColor = t.Err
	case pending > 0:
		labelColor = t.Warn
	}
	c.text(x-5, y, label, labelColor, unsched > 0)

	c.hMeterRail(x, y, w, m.pend.CPU, t.Warn, t.Empty)
	// The refused segment recolours the leading cells of the same rail: it is a
	// subset of the backlog, not a separate quantity, and stacking it that way
	// keeps the bar's length equal to the whole backlog.
	c.recolorRail(x, y, int(clamp01(m.pend.Mem)*float64(w)+0.5), t.Err)

	if pending == 0 {
		c.text(x+w+pctW, y, "no pods waiting", t.Dim, false)
		return
	}
	// A cell is lit, so "0%" would read as a contradiction; say "<1%" instead.
	pct := fmt.Sprintf("%.0f%%", m.pend.CPU*100)
	if m.pend.CPU*100 < 0.5 {
		pct = "<1%"
	}
	col := t.Fg
	if unsched > 0 {
		col = t.Err
	}
	c.textRight(x+w, y, pctW-1, pct, col, true)

	stats := fmt.Sprintf("%d pending", pending)
	if unsched > 0 {
		stats = fmt.Sprintf("%d pending · %d unschedulable", pending, unsched)
		if len(stats) > statsW {
			stats = fmt.Sprintf("%d pend · %d unsched", pending, unsched)
		}
	}
	statsCol := t.Dim
	if unsched > 0 {
		statsCol = t.Err
	}
	c.text(x+w+pctW, y, stats, statsCol, false)
}

// phaseAbbrev is the canonical header label. The meter yields space to this
// text because a lifecycle count is more useful than a slightly longer rail.
func phaseAbbrev(p model.Phase) string {
	return phaseLabel(p)
}

// pctW is the width reserved for a meter's percentage, which sits *beside* the
// bar rather than on it. The header's three meters are stacked one row apart, so
// they are drawn as half-height rails to keep them from merging into one block —
// and nothing can be printed on a rail without punching a hole in it.
const pctW = 6

func drawHeaderMeter(c *canvas, x, y, w int, label string, frac float64, stats string) {
	t := theme.Current
	c.text(x-4, y, label, t.Dim, false)
	c.hMeterRail(x, y, w, frac, t.Util(frac), t.Empty)
	c.textRight(x+w, y, pctW-1, fmt.Sprintf("%.0f%%", frac*100), t.Fg, true)
	c.text(x+w+pctW, y, stats, t.Dim, false)
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
	left = append(left, playbackStatus(m.playback, time.Now()))
	left = append(left, m.filter.Describe()...)
	if !m.filter.Active() {
		left = append(left, "no filters")
	}
	leftText := strings.Join(left, "  ")
	c.text(1, 0, leftText, t.PanelFg, false)

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
		detail += " · [" + phaseChipLabel(n.Phase) + "]"
		if reason := phaseDescription(n); reason != "" {
			detail += " " + reason
		}
		if n.HasPrice {
			detail += fmt.Sprintf(" · $%.3f/hr", n.Price)
		}
		col := t.PhaseColor(int(n.Phase))
		// Playback time is operational state, so selected-node decoration yields
		// space to it instead of painting over the timestamp and lag.
		detailW := min(w/2, max(0, w-runewidth.StringWidth(leftText)-4))
		if detailW > 1 {
			c.textRight(0, 0, w-1, shorten(detail, detailW-1)+" ", col, true)
		}
	} else {
		c.textRight(0, 0, w-1, "press : for commands · ? for help ", t.Dim, false)
	}
	return c.String()
}

// playbackStatus keeps rate, cluster timestamp, and lag in one readout. The
// same wallNow drives both timestamp and lag so the two figures cannot disagree
// at a second boundary.
func playbackStatus(p *playback, wallNow time.Time) string {
	displayAt := p.DisplayNow(wallNow).Local()
	stampLayout := "15:04:05"
	wallLocal := wallNow.Local()
	wy, wm, wd := wallLocal.Date()
	dy, dm, dd := displayAt.Date()
	if wy != dy || wm != dm || wd != dd {
		stampLayout = "Jan 02 15:04:05"
	}
	stamp := displayAt.Format(stampLayout)
	if p.live {
		return "REALTIME · at " + stamp
	}
	state := fmt.Sprintf("%gx", p.speed)
	if p.speed == 0 {
		state = "PAUSED"
	}
	return fmt.Sprintf("%s · at %s · %s behind", state, stamp, playbackLag(p.Behind(wallNow)))
}

func playbackLag(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Truncate(time.Second).String()
}

func (m *Model) showSeekOverlay(moved time.Duration) {
	if moved <= 0 {
		return
	}
	now := time.Now()
	if !m.seekOverlayAt.IsZero() && now.Sub(m.seekOverlayAt) >= 0 && now.Sub(m.seekOverlayAt) < seekOverlayTTL {
		m.seekOverlay += moved
	} else {
		m.seekOverlay = moved
	}
	m.seekOverlayAt = now
}

// renderSeekOverlay composites a small video-player-style badge over an
// already-rendered frame. ansi.Cut preserves the styling on either side while
// replacing only the centred panel's cells, so frame geometry never changes.
func (m *Model) renderSeekOverlay(frame string, w, h int) string {
	if m.seekOverlay <= 0 || m.seekOverlayAt.IsZero() ||
		time.Since(m.seekOverlayAt) >= seekOverlayTTL || w < 8 || h < 3 {
		return frame
	}
	seconds := m.seekOverlay.Seconds()
	var amount string
	if m.seekOverlay%time.Second == 0 {
		amount = fmt.Sprintf("− %d seconds", int64(seconds))
	} else {
		amount = fmt.Sprintf("− %.1f seconds", seconds)
	}
	if seconds == 1 {
		amount = "− 1 second"
	}

	t := theme.Current
	panelW, panelH := min(w, max(18, runewidth.StringWidth(amount)+6)), 3
	panel := newCanvas(panelW, panelH, t.Panel, t.PanelFg)
	drawBorder(panel, panelW, panelH, borderSolid, t.Accent, t.Panel)
	panel.textCenter(0, 1, panelW, amount, t.PanelFg, true)
	panelLines := strings.Split(panel.String(), "\n")

	lines := strings.Split(frame, "\n")
	for len(lines) < h {
		lines = append(lines, padRow("", w))
	}
	x, y := (w-panelW)/2, (h-panelH)/2
	for i, overlay := range panelLines {
		row := padRow(lines[y+i], w)
		left := ansi.Cut(row, 0, x)
		right := ansi.Cut(row, x+panelW, w)
		lines[y+i] = padRow(left+overlay+right, w)
	}
	return strings.Join(lines[:h], "\n")
}

// legendHeight is the row count of the legend strip.
//
// Two rows, because there are exactly two visual languages on screen: the
// labelled chip says what the node is doing, the fill says what its pods are doing.
// (This was three rows while pod cells could be coloured by workload; with
// state-only colouring the "colour" and "shape" rows said the same thing, which
// is precisely the clutter that made the picture hard to read.)
const legendHeight = 2

// podStates is the full pod-cell key: colour and glyph together, in one place,
// so the legend and the renderer cannot drift.
var podStates = []struct {
	glyph  rune
	marker rune
	label  string
}{
	{glyphRunning, '●', "running"},
	{glyphPending, '○', "pending"},
	{glyphTerminating, '◐', "terminating"},
	{glyphFailed, '×', "failed"},
}

// renderLegend uses the same filled, labelled chips for both visual languages.
// A hairline colour sample asks the audience to perform a visual lookup; a
// labelled chip makes the mapping self-evident and remains readable on a
// projector.
func (m *Model) renderLegend(w int) string {
	t := theme.Current
	c := newCanvas(w, legendHeight, t.Bg, t.Fg)

	const labelCol = 8 // width of the "node"/"pods" row labels

	// Current exceptional states come first, followed by the remaining lifecycle
	// reference. Narrow terminals therefore lose quiet/reference chips before a
	// state that is actually happening in the cluster.
	c.text(1, 0, "node", t.Dim, true)
	x := labelCol
	present := map[model.Phase]bool{}
	for _, n := range m.snap.Nodes {
		present[n.Phase] = true
	}
	ordered := []model.Phase{model.PhaseTerminating, model.PhaseDraining, model.PhaseCordoned,
		model.PhaseNotReady, model.PhaseProvisioning, model.PhaseReady}
	var phases []model.Phase
	for _, wantPresent := range []bool{true, false} {
		for _, p := range ordered {
			if present[p] == wantPresent {
				phases = append(phases, p)
			}
		}
	}
	for _, p := range phases {
		label := phaseChipLabel(p)
		chipW := runewidth.StringWidth(label) + 2
		if x+chipW > w-2 {
			label = phaseIcon(p) + " " + strings.ToUpper(phaseShortLabel(p))
			chipW = runewidth.StringWidth(label) + 2
		}
		if x+chipW > w-2 {
			continue
		}
		col := phaseEdge(p)
		c.rect(x, 0, chipW, 1, col)
		c.text(x, 0, " "+label+" ", contrastOn(col), true)
		x += chipW + 1
	}

	// Row 1 — pod states get the same visual weight as node phases. Compact
	// markers keep the chips readable without making the pod field itself busy.
	c.text(1, 1, "pods", t.Dim, true)
	x = labelCol
	for _, st := range podStates {
		label := string(st.marker) + " " + strings.ToUpper(st.label)
		chipW := runewidth.StringWidth(label) + 2
		if x+chipW > w-2 {
			break
		}
		col := stateColor(st.label)
		c.rect(x, 1, chipW, 1, col)
		c.text(x, 1, " "+label+" ", contrastOn(col), true)
		x += chipW + 1
	}
	if x+24 <= w-2 {
		c.text(x, 1, "· cell size = cpu request", t.Dim, false)
		x += 26
	}

	// Utilisation remains a rail because that is the exact shape used by the
	// header and dense view, but it no longer steals room from lifecycle states.
	const rampPre, rampPost = "meter ", " 0→100%"
	rampW := clampInt(w/7, 0, 18)
	rampX := w - rampW - len(rampPost) - 2
	if rampW >= 8 && rampX-len(rampPre) > x {
		for i := 0; i < rampW; i++ {
			c.hMeterRail(rampX+i, 1, 1, 1, t.Util(float64(i)/float64(rampW-1)), t.Empty)
		}
		c.text(rampX-len(rampPre), 1, rampPre, t.Dim, false)
		c.text(rampX+rampW, 1, rampPost, t.Dim, false)
	}
	return c.String()
}

// helpText is rendered into the overlay. Generated from the registry so it can
// never drift from what the commands actually are.
func helpLines(includeDemo bool) []string {
	out := []string{
		"KEYS",
		"  ↑↓←→ / hjkl   move selection          enter    node details + events",
		"  pgup pgdn     scroll                  :        command bar",
		"                                        /        filter nodes by name",
		"  g / G         first / last            \\        clear all filters",
		"  v             cycle view modes          d        dense mode",
		"  s / S         cycle sort / reverse",
		"  l             toggle legend           ?        this help",
		"  z / Z         zoom in / out           0        zoom to fit",
		"  p             pause / resume          [        rewind 5 seconds",
		"                                        r        jump to realtime",
		"  q             quit",
		"",
		"MOUSE / TRACKPAD",
		"  two-finger scroll            zoom on the node under the pointer",
		"                               (a pinch never reaches a terminal app)",
		"  ctrl / option / shift +      scroll the grid",
		"    two-finger scroll          (the selection comes with it)",
		"  click / drag                 select a node",
		"",
		"NODE DETAILS (enter)",
		"  esc / backspace / q          back to the grid, exactly as you left it",
		"  ↑↓ jk pgup pgdn g G          scroll · wheel scrolls too",
		"  ctrl+r                       re-read live detail now",
		"",
	}
	if includeDemo {
		out = append(out,
			"DEMO KEYS (simulated cluster only)",
			"  +   scale up one node      -   drain a node      x   churn pods",
			"  b   submit 8 pods with no node (they pile up in the pending meter)",
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
