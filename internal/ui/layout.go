package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// Box sizing bounds per mode. The ideal values are what the layout aims for;
// the minimums are the point below which a box stops being readable and it is
// better to scroll than to shrink further. The maximums stop three nodes on a
// wide screen from becoming three absurd billboards.
const (
	// Real gutters between cards. gapY used to be zero, which made vertically
	// adjacent boxes share a border line and read as one continuous grid rather
	// than as separate nodes.
	gapX = 2
	gapY = 1

	// cellAspect: a terminal cell is roughly twice as tall as it is wide, so a
	// visually square box needs about twice as many columns as rows.
	cellAspect = 0.5
	// targetAspect is the visual width:height a node card looks best at.
	targetAspect = 1.5

	podsMinW, podsIdealH, podsMinH, podsMaxW, podsMaxH      = 22, 10, 6, 104, 34
	nodesMinW, nodesIdealH, nodesMinH, nodesMaxW, nodesMaxH = 14, 8, 5, 64, 26
)

// Zoom sits on top of the automatic layout rather than replacing it.
//
// Level 0 is "fit": exactly the aspect-ratio search the grid has always done, so
// the picture you get back from `0` is always the one the layout would have
// chosen for you. The two ends of the range are defined by what they are for,
// not by a magic ratio: fully out is a card at the readable floor, fully in is a
// card filling the screen. The levels in between are geometric, so every step is
// the same proportional change and the range covers the same ground whatever the
// terminal size or cluster size — a fixed per-level ratio could not, and left
// "as far in as it goes" well short of one card on a small cluster.
//
// Nineteen levels is enough that a step reads as the cards growing rather than
// as the screen being replaced. The keyboard compensates with keyZoomStep so `z`
// still feels like a step rather than a nudge.
const (
	zoomMin, zoomMax = -6, 12
	// Hard floors. Zooming out must not be able to produce something that is no
	// longer a card: below this there is no room for a border, a name and a
	// meter, and dense mode is the honest answer instead.
	zoomFloorW, zoomFloorH = 12, 4
)

// zoomScale is how much bigger than the fitted card a level is, given what the
// window and the fitted card actually are.
func zoomScale(zoom, w, h, fitW, fitH int) float64 {
	switch {
	case zoom == 0 || fitW <= 0 || fitH <= 0:
		return 1
	case zoom > 0:
		// Fully in fills the window in whichever dimension binds first.
		full := math.Max(float64(w)/float64(fitW), float64(h)/float64(fitH))
		return math.Pow(math.Max(full, 1), float64(zoom)/float64(zoomMax))
	default:
		// Fully out is the smallest thing still worth calling a card.
		floor := math.Min(float64(zoomFloorW)/float64(fitW), float64(zoomFloorH)/float64(fitH))
		return math.Pow(math.Min(floor, 1), float64(zoom)/float64(zoomMin))
	}
}

// cardBounds is the per-mode card sizing the automatic layout aims for.
func cardBounds(mode Mode) (minW, idealH, minH, maxW, maxH int) {
	if mode == ModeNodes {
		return nodesMinW, nodesIdealH, nodesMinH, nodesMaxW, nodesMaxH
	}
	return podsMinW, podsIdealH, podsMinH, podsMaxW, podsMaxH
}

// zoomLabel is how the status bar names a level: "fit" at rest, otherwise the
// card's size relative to the fitted one. A multiplier survived the move to
// finer levels where a signed step count did not — "+9" says nothing once there
// are nineteen of them, but "4.4×" is the thing the user can actually see.
func zoomLabel(scale float64) string {
	if scale == 1 {
		return "fit"
	}
	return fmt.Sprintf("%.1f×", scale)
}

// layout is the geometry of the card grid, as a fixed virtual canvas.
//
// The canvas has a column count and a card size; where the viewport sits on it
// is a pan, held by the Model, not part of the layout. Keeping the two apart is
// what makes zooming stop shuffling the deck: a card's (row, column) is a
// property of the list and the column count alone, so it does not move when the
// cards change size.
type layout struct {
	cols       int
	boxW, boxH int
	// stepX/stepY are the card pitch, gutter included: the distance from one
	// card's left edge to the next one's.
	stepX, stepY int
	// pages is how many card rows the whole list needs.
	pages int
	// gridW/gridH are the size of the whole canvas, most of which may be off
	// screen when zoomed in.
	gridW, gridH int
	// scale is the card size relative to the fitted card, for the status bar. It
	// is display only, and sameGeometry ignores it.
	scale float64
}

// sameGeometry reports whether two layouts draw the same picture. Zoom levels
// that resolve to the same whole-cell geometry are skipped when stepping, and
// the scale — which changes at every level by definition — must not count.
func (l layout) sameGeometry(o layout) bool {
	l.scale, o.scale = 0, 0
	return l == o
}

// rows is how many card rows fit in a viewport h rows tall. Partial rows do not
// count: it is used for paging and for "does everything fit".
func (l layout) rows(h int) int { return max(1, (h+gapY)/l.stepY) }

// fits reports whether the whole canvas is on screen at once.
func (l layout) fits(w, h int) bool { return l.gridW <= w && l.gridH <= h }

// computeLayout picks the card geometry for a zoom level.
//
// Zoom scales the *fitted* card and leaves the column count alone. Deriving the
// columns from the card size was tried first, and reflowing is exactly what made
// zooming so hard to aim: every step moved every card to a different row and
// column, so the card under the pointer changed as a side effect of the gesture
// and the whole grid appeared to jump. Holding the columns fixed turns a zoom
// into what a zoom is everywhere else — the same picture, larger — with the
// viewport panning over a canvas that may now be bigger than the screen.
//
// cols is the frozen column count to lay out with; zero means fit it to the
// viewport, which is what zoom level 0 always does.
func computeLayout(mode Mode, w, h, n, zoom, cols int) layout {
	if n <= 0 {
		return layout{cols: 1, boxW: w, boxH: h, stepX: max(1, w), stepY: max(1, h), pages: 1, scale: 1}
	}
	fit := fitLayout(mode, w, h, n)
	if zoom == 0 || cols <= 0 {
		cols = fit.cols
	}
	boxW, boxH := fit.boxW, fit.boxH
	scale := zoomScale(zoom, w, h, fit.boxW, fit.boxH)
	if zoom != 0 {
		// The card is clamped to the viewport, never beyond: one card filling the
		// screen is as far in as zooming goes, and a card taller than the screen
		// could never be read anyway.
		boxW = clampInt(int(float64(fit.boxW)*scale+0.5), min(zoomFloorW, w), max(1, w))
		boxH = clampInt(int(float64(fit.boxH)*scale+0.5), min(zoomFloorH, h), max(1, h))
	}
	l := newLayout(cols, boxW, boxH, n)
	l.scale = scale
	return l
}

func newLayout(cols, boxW, boxH, n int) layout {
	cols = max(1, cols)
	pages := (n + cols - 1) / cols
	return layout{
		scale: 1,
		cols:  cols,
		boxW:  boxW,
		boxH:  boxH,
		stepX: boxW + gapX,
		stepY: boxH + gapY,
		pages: pages,
		gridW: cols*boxW + gapX*(cols-1),
		gridH: pages*boxH + gapY*(pages-1),
	}
}

// fitLayout is the automatic layout: it scores each column count by how close
// the resulting card is to a pleasing aspect ratio, strongly preferring layouts
// that show every node at once and mildly preferring ones with no ragged last
// row. An earlier version scored purely on width closeness to an ideal, which
// made every candidate tie whenever the width clamp kicked in — so a three-node
// cluster picked a single column and stacked three small boxes in the corner of
// a mostly empty screen.
func fitLayout(mode Mode, w, h, n int) layout {
	minW, idealH, minH, maxW, maxH := cardBounds(mode)

	best := buildLayout(1, w, h, n, idealH, minH, maxW, maxH)
	bestScore := math.Inf(-1)
	for cols := 1; cols <= n; cols++ {
		if (w-gapX*(cols-1))/cols < minW {
			break
		}
		l := buildLayout(cols, w, h, n, idealH, minH, maxW, maxH)

		aspect := (float64(l.boxW) * cellAspect) / float64(l.boxH)
		score := -math.Abs(aspect - targetAspect)
		if l.rows(h) >= l.pages {
			score += 1.5 // showing everything at once beats a prettier card
		}
		if n%cols == 0 {
			score += 0.25 // no ragged last row
		}
		if score > bestScore {
			bestScore, best = score, l
		}
	}
	return best
}

// buildLayout resolves box size, visible rows and page count for a fixed column
// count.
func buildLayout(cols, w, h, n, idealH, minH, maxW, maxH int) layout {
	boxW := clampInt(min((w-gapX*(cols-1))/cols, maxW), 1, max(1, w))
	totalRows := (n + cols - 1) / cols

	boxH := (h - gapY*(totalRows-1)) / totalRows
	if boxH < minH {
		// Cannot fit every row; fall back to a comfortable height and scroll.
		boxH = idealH
	}
	// The viewport clamp is last: a zoomed-in card must stop growing at the edge
	// of the screen rather than be sliced off by the row budget.
	boxH = clampInt(clampInt(boxH, minH, maxH), 1, max(1, h))
	_ = totalRows

	return newLayout(cols, boxW, boxH, n)
}

// renderGrid draws the window of the canvas at (panX, panY).
//
// pan is in cells, not cards, and may put a partial card against any edge. That
// is the point: a zoom that keeps one card under the pointer will in general
// leave halves of its neighbours showing, and a renderer that could only draw
// whole cards would have to move the grid to a whole-card boundary — which is
// the jumping this replaced.
func renderGrid(vs []visible, l layout, w, h int, ctx boxCtx, panX, panY, cursor int) string {
	t := theme.Current
	if len(vs) == 0 {
		return emptyState(w, h)
	}
	blankLine := lipgloss.NewStyle().Background(t.Bg).Render(spaces(w))

	out := make([]string, 0, h)
	row, cells := -1, [][]string(nil)

	for y := 0; y < h; y++ {
		vy := y + panY // the canvas row this screen row shows
		if vy < 0 {
			out = append(out, blankLine)
			continue
		}
		r, line := vy/l.stepY, vy%l.stepY
		if r >= l.pages || line >= l.boxH {
			out = append(out, blankLine) // past the end, or a gutter row
			continue
		}
		if r != row {
			// Render this card row once, not once per line of it.
			row, cells = r, renderCardRow(vs, l, r, ctx, cursor)
		}
		out = append(out, composeRow(cells, l, w, panX, line))
	}
	return strings.Join(out, "\n")
}

// renderCardRow renders every card in one canvas row, including ones currently
// off screen. Cards are cheap and the alternative is threading clipping through
// the box renderer.
func renderCardRow(vs []visible, l layout, row int, ctx boxCtx, cursor int) [][]string {
	cells := make([][]string, 0, l.cols)
	for col := 0; col < l.cols; col++ {
		idx := row*l.cols + col
		if idx >= len(vs) {
			break
		}
		c := ctx
		c.selected = idx == cursor
		cells = append(cells, renderNodeBox(vs[idx], l.boxW, l.boxH, c))
	}
	return cells
}

// composeRow assembles one screen line from the cards of a canvas row, clipping
// whatever hangs off either edge.
func composeRow(cells [][]string, l layout, w, panX, line int) string {
	t := theme.Current
	var b strings.Builder
	bg := lipgloss.NewStyle().Background(t.Bg)
	x := 0 // how much of the screen line is written

	for col, cell := range cells {
		if line >= len(cell) {
			continue
		}
		x0 := col*l.stepX - panX
		if x0 >= w {
			break
		}
		if x0+l.boxW <= 0 {
			continue
		}
		// The visible slice of this card, in the card's own columns.
		from, to := max(0, -x0), min(l.boxW, w-x0)
		if to <= from {
			continue
		}
		if at := x0 + from; at > x {
			b.WriteString(bg.Render(spaces(at - x)))
			x = at
		}
		// ANSI-aware: by now the card line is mostly escape sequences.
		b.WriteString(ansi.Cut(cell[line], from, to))
		x += to - from
	}
	if x < w {
		b.WriteString(bg.Render(spaces(w - x)))
	}
	return padRow(b.String(), w)
}

// renderDense is one row per node: a table for clusters with more nodes than
// boxes will fit. The meters are still real fills, just short ones.
func renderDense(vs []visible, w, h int, ctx boxCtx, scroll, cursor int) string {
	t := theme.Current
	if len(vs) == 0 {
		return emptyState(w, h)
	}
	c := newCanvas(w, h, t.Bg, t.Fg)

	cols := denseColumns(w)

	c.text(1, 0, "NODE", t.Dim, true)
	if cols.showType {
		c.text(cols.xType, 0, "TYPE", t.Dim, true)
	}
	if cols.showPool {
		c.text(cols.xPool, 0, "NODEPOOL", t.Dim, true)
	}
	c.text(cols.xCPU, 0, "CPU", t.Dim, true)
	c.text(cols.xMem, 0, "MEM", t.Dim, true)
	if cols.showCons {
		c.text(cols.xCons, 0, "CONS", t.Dim, true)
	}
	c.text(cols.xTail, 0, "PODS  AGE", t.Dim, true)
	if cols.showState {
		c.text(cols.xTail+10, 0, "STATE", t.Dim, true)
	}

	for i := 0; i+scroll < len(vs) && i+1 < h; i++ {
		v := vs[i+scroll]
		n := v.node
		y := i + 1
		track := ctx.reg.Node(n.Name)
		cpu, mem := n.Util()
		track.Target(cpu, mem, false)
		if removalReadyToCollapse(n, ctx.now) {
			track.SetLeaving()
		}

		phaseCol := t.PhaseColor(int(n.Phase))
		if n.Phase.Terminal() {
			phaseCol = theme.Mix(phaseCol, t.Flash, 0.5*ctx.reg.Pulse(track, pulseDraining))
		}
		if track.Flash > 0 {
			phaseCol = theme.Mix(phaseCol, t.Flash, 0.7*track.Flash)
		}
		fg := t.Fg
		if i+scroll == cursor {
			c.rect(0, y, w, 1, t.Panel)
			fg = t.Selected
		}
		if track.Leaving {
			fg = t.Dimmed(fg, track.ExitEase())
		}

		c.text(0, y, "▌", phaseCol, true)
		c.text(1, y, shorten(n.Name, cols.nameW), fg, i+scroll == cursor)
		if cols.showType {
			c.text(cols.xType, y, shorten(n.InstanceType, 14), t.Dim, false)
		}
		if cols.showPool {
			c.text(cols.xPool, y, shorten(n.NodePool, 12), t.Dim, false)
		}
		// Rails, not fills: one row per node means every meter has another meter
		// directly above and below it, and background fills at that density merge
		// into a single column of colour with no bars in it.
		c.hMeterRail(cols.xCPU, y, cols.meterW, track.CPU, t.Util(track.CPU), t.Empty)
		c.text(cols.xCPU+cols.meterW+1, y, fmt.Sprintf("%3.0f%%", track.CPU*100), t.Fg, false)
		c.hMeterRail(cols.xMem, y, cols.meterW, track.Mem, t.Util(track.Mem), t.Empty)
		c.text(cols.xMem+cols.meterW+1, y, fmt.Sprintf("%3.0f%%", track.Mem*100), t.Fg, false)
		if cols.showCons {
			// The column is one glyph wide, so it has to carry its meaning in colour
			// as well: a consolidatable node is one Karpenter may take away.
			c.text(cols.xCons+1, y, n.Consolidatable.Short(), consolidationColor(n.Consolidatable), true)
		}

		tail := fmt.Sprintf("%4d %4s", len(v.pods), model.HumanAge(ctx.now.Sub(n.Created)))
		if cols.showState {
			tail += " " + shorten(densePhaseState(n, ctx.now), denseStateWidth-1)
		}
		c.text(cols.xTail, y, tail, phaseCol, false)
	}
	return c.String()
}

// consolidationColor is the CONS column's palette: warn for a node Karpenter is
// willing to remove (it is about to change under you), quiet for one it will not,
// dim for a cluster that has not said.
func consolidationColor(c model.Consolidation) lipgloss.Color {
	t := theme.Current
	switch c {
	case model.ConsolidationYes:
		return t.Warn
	case model.ConsolidationNo:
		return t.Ok
	default:
		return t.Dim
	}
}

// denseCols is the resolved column geometry for one terminal width.
type denseCols struct {
	nameW, meterW                           int
	xType, xPool, xCPU, xMem, xCons, xTail  int
	showType, showPool, showState, showCons bool
}

const denseStateWidth = 20

// denseColumns lays out the dense table by dropping the least useful columns
// first as width shrinks, rather than letting everything overlap. The node name
// and the two meters are never dropped: they are the reason to be in this mode.
func denseColumns(w int) denseCols {
	const (
		minName  = 12
		typeW    = 15
		poolW    = 13
		pctW     = 6
		podsAgeW = 10
		stateW   = denseStateWidth
		consW    = 5
	)
	d := denseCols{showType: true, showPool: true, showState: true, showCons: true}
	d.meterW = clampInt((w-75)/2, 5, 20)

	// Give up columns in increasing order of value until the name fits.
	for {
		fixed := 1 + 2*(d.meterW+pctW) + podsAgeW
		if d.showType {
			fixed += typeW
		}
		if d.showPool {
			fixed += poolW
		}
		if d.showState {
			fixed += stateW
		}
		if d.showCons {
			fixed += consW
		}
		d.nameW = w - fixed
		if d.nameW >= minName {
			break
		}
		switch {
		case d.showPool:
			d.showPool = false
		case d.showType:
			d.showType = false
		case d.showCons:
			d.showCons = false
		case d.meterW > 5:
			d.meterW--
		case d.showState:
			// State is the reason to look at a live demo. It disappears only after
			// every secondary descriptor and all optional meter width are gone.
			d.showState = false
		default:
			d.nameW = max(4, d.nameW)
			goto done
		}
	}
done:
	// A very wide terminal should widen the meters, not the name column: node
	// names top out around 40 characters and the meters are the data.
	const maxName = 40
	if surplus := d.nameW - maxName; surplus > 0 {
		d.nameW = maxName
		d.meterW = min(d.meterW+surplus/2, 34)
	}

	x := 1 + d.nameW + 1
	if d.showType {
		d.xType = x
		x += typeW
	}
	if d.showPool {
		d.xPool = x
		x += poolW
	}
	d.xCPU = x
	x += d.meterW + pctW
	d.xMem = x
	x += d.meterW + pctW
	if d.showCons {
		d.xCons = x
		x += consW
	}
	d.xTail = x
	return d
}

func emptyState(w, h int) string {
	t := theme.Current
	c := newCanvas(w, h, t.Bg, t.Fg)
	msg := "no nodes match the current filter"
	c.textCenter(0, h/2, w, msg, t.Dim, true)
	c.textCenter(0, h/2+1, w, "press  \\  to clear filters, or  ?  for help", t.Dim, false)
	return c.String()
}

// padRow pads or truncates a pre-styled row to exactly w visible columns.
// Truncation must be ANSI-aware: rows are already full of escape sequences by
// the time they get here.
func padRow(s string, w int) string {
	width := lipgloss.Width(s)
	if width == w {
		return s
	}
	if width < w {
		return s + lipgloss.NewStyle().Background(theme.Current.Bg).Render(spaces(w-width))
	}
	return ansi.Truncate(s, w, "")
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
