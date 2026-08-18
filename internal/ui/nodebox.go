package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/mattn/go-runewidth"

	"github.com/oxidecomputer/k8s-node-viewer/internal/anim"
	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// Pod-cell glyphs. Phase is encoded in the glyph as well as the colour so the
// distinction survives a projector that crushes saturation, and so the view is
// still readable to someone who cannot separate the hues.
const (
	glyphRunning     = '█'
	glyphPending     = '▒'
	glyphTerminating = '▓'
	glyphFailed      = '╳'
)

// Pulse periods. Deleting is deliberately faster than draining: urgency should
// be legible without reading the label.
const (
	pulseProvisioning = 1400 * time.Millisecond
	pulseDraining     = 1100 * time.Millisecond
	pulseDeleting     = 550 * time.Millisecond
)

// borderSet is the six glyphs of a box border.
type borderSet struct {
	tl, tr, bl, br, h, v rune
}

var (
	borderSolid  = borderSet{'╭', '╮', '╰', '╯', '─', '│'}
	borderDashed = borderSet{'╭', '╮', '╰', '╯', '┄', '┊'}
	borderHeavy  = borderSet{'┏', '┓', '┗', '┛', '━', '┃'}
)

// boxCtx is everything the box renderer needs beyond the node itself. Passing a
// struct keeps the signature stable as the UI grows options.
type boxCtx struct {
	reg      *anim.Registry
	basis    model.Basis
	mode     Mode
	selected bool
	// dimPods greys pods that do not match the pod-level filter.
	dimPods bool
}

// renderNodeBox draws one node into exactly h lines of width w.
//
// Enter and exit are animated by changing the box's drawn height and by wiping
// the interior horizontally, rather than by moving the box. The grid slot stays
// fixed, so a node appearing or disappearing never reflows its neighbours —
// which matters a great deal when you are pointing at one of them on a screen.
func renderNodeBox(v visible, w, h int, ctx boxCtx) []string {
	n := v.node
	track := ctx.reg.Node(n.Name)

	cpuFrac, memFrac := n.Util(ctx.basis)
	track.Target(cpuFrac, memFrac, false)

	if n.Phase == model.PhaseGone {
		track.SetLeaving()
	}

	// Animated drawn height: grow in, collapse out.
	drawH := h
	if track.Enter < 1 {
		drawH = lerpInt(3, h, track.EnterEase())
	}
	if track.Leaving {
		drawH = lerpInt(h, 1, track.ExitEase())
	}
	drawH = clampInt(drawH, 1, h)

	reveal := 1.0
	if track.Enter < 1 {
		reveal = track.EnterEase()
	}
	fade := 0.0
	if track.Leaving {
		fade = track.ExitEase()
	}

	t := theme.Current
	c := newCanvas(w, drawH, t.Card, t.Fg)

	border, borderColor := borderStyle(n, track, ctx)
	cardBg := t.Card
	if fade > 0 {
		borderColor = t.Dimmed(borderColor, fade*0.85)
		cardBg = t.Dimmed(cardBg, fade)
		c.rect(0, 0, w, drawH, cardBg)
	}

	if drawH < 3 {
		// Mid-animation sliver: a single bar in the phase colour reads as the
		// box collapsing rather than as a rendering glitch.
		c.rect(0, 0, w, drawH, t.Bg)
		c.fillRect(0, 0, int(float64(w)*(1-fade)), drawH, '─', borderColor, t.Bg)
		return padLines(c.Lines(), w, h, t)
	}

	drawBorder(c, w, drawH, border, borderColor, cardBg)

	// A title strip gives the card a head, which is what makes one node
	// visually separable from the next. Short cards fall back to the name in
	// the top border rather than losing a whole content row to it.
	titleH := 0
	if drawH >= 8 {
		titleH = 1
		drawTitleBar(c, w, 1, v, borderColor, ctx, fade)
	} else {
		drawTitleInBorder(c, w, v, borderColor, ctx, fade)
	}

	padX := 0
	if w >= 26 {
		padX = 1
	}
	cx := 1 + padX
	cw := w - 2 - 2*padX
	cy := 1 + titleH
	ch := drawH - 2 - titleH

	if cw > 0 && ch > 0 {
		interior := newCanvas(cw, ch, cardBg, t.Fg)
		switch ctx.mode {
		case ModeNodes:
			// No hatching here: in this mode the interior is one big gauge with
			// large text, and any overlay makes the number harder to read from a
			// distance. The pulsing heavy border carries the drain signal.
			drawGauges(interior, v, track, ctx)
		default:
			drawPodsInterior(interior, v, track, ctx, borderColor, cardBg)
		}
		if fade > 0 {
			dimCanvas(interior, fade)
		}
		blit(c, interior, cx, cy, reveal)
	}

	drawFooter(c, w, drawH, v, borderColor, ctx, fade)
	return padLines(c.Lines(), w, h, t)
}

// borderStyle picks the border glyphs and colour for a phase, including its
// pulse. This is the one function to touch to restyle node states.
func borderStyle(n *model.Node, track *anim.Track, ctx boxCtx) (borderSet, lipgloss.Color) {
	t := theme.Current
	base := phaseEdge(n.Phase)
	set := borderSolid

	switch n.Phase {
	case model.PhaseProvisioning:
		set = borderDashed
		// Breathe toward the background: "not here yet".
		base = theme.Mix(t.Bg, base, 0.45+0.55*ctx.reg.Pulse(track, pulseProvisioning))
	case model.PhaseDraining:
		set = borderHeavy
		base = theme.Mix(base, t.Flash, 0.55*ctx.reg.Pulse(track, pulseDraining))
	case model.PhaseDeleting:
		set = borderHeavy
		base = theme.Mix(base, t.Flash, 0.7*ctx.reg.Pulse(track, pulseDeleting))
	case model.PhaseNotReady:
		set = borderHeavy
	}

	// A brand-new box flashes white as it grows, which is what draws the eye to
	// a scale-up event.
	if track.Enter < 1 {
		base = theme.Mix(t.Flash, base, track.EnterEase())
	}
	if ctx.selected {
		set = borderHeavy
		base = theme.Mix(base, t.Selected, 0.5)
	}
	return set, base
}

// phaseEdge is the border colour for a phase.
//
// Ready is deliberately muted toward the card body: on a healthy cluster every
// node is ready, and a screen full of saturated green rectangles is noise that
// hides the one node doing something. Everything that is not steady state keeps
// its full saturation, so the eye is drawn to it.
func phaseEdge(p model.Phase) lipgloss.Color {
	t := theme.Current
	base := t.PhaseColor(int(p))
	if p == model.PhaseReady {
		return theme.Mix(base, t.Card, 0.55)
	}
	return base
}

func drawBorder(c *canvas, w, h int, b borderSet, col, bg lipgloss.Color) {
	for x := 1; x < w-1; x++ {
		c.fillRect(x, 0, 1, 1, b.h, col, bg)
		c.fillRect(x, h-1, 1, 1, b.h, col, bg)
	}
	for y := 1; y < h-1; y++ {
		c.fillRect(0, y, 1, 1, b.v, col, bg)
		c.fillRect(w-1, y, 1, 1, b.v, col, bg)
	}
	c.fillRect(0, 0, 1, 1, b.tl, col, bg)
	c.fillRect(w-1, 0, 1, 1, b.tr, col, bg)
	c.fillRect(0, h-1, 1, 1, b.bl, col, bg)
	c.fillRect(w-1, h-1, 1, 1, b.br, col, bg)
}

// drawTitleBar fills a header strip with the node name. The strip is tinted
// toward the node's phase colour, so a card is identifiable by its head as well
// as by its outline.
func drawTitleBar(c *canvas, w, y int, v visible, col lipgloss.Color, ctx boxCtx, fade float64) {
	t := theme.Current
	n := v.node

	bg := theme.Mix(t.Title, col, 0.22)
	if ctx.selected {
		bg = theme.Mix(t.Title, t.Accent, 0.5)
	}
	if fade > 0 {
		bg = t.Dimmed(bg, fade)
	}
	c.rect(1, y, w-2, 1, bg)

	fg := contrastOn(bg)
	badge := phaseBadge(n)
	avail := w - 4
	if badge != "" && avail > runewidth.StringWidth(badge)+8 {
		avail -= runewidth.StringWidth(badge) + 2
	} else {
		badge = ""
	}
	c.text(2, y, shorten(n.Name, avail), fg, true)
	if badge != "" {
		c.textRight(0, y, w-2, badge+" ", theme.Mix(fg, col, 0.45), true)
	}
}

// drawTitleInBorder is the fallback for cards too short to spare a row.
func drawTitleInBorder(c *canvas, w int, v visible, col lipgloss.Color, ctx boxCtx, fade float64) {
	t := theme.Current
	n := v.node
	badge := phaseBadge(n)
	avail := w - 4
	if badge != "" && avail > runewidth.StringWidth(badge)+6 {
		avail -= runewidth.StringWidth(badge) + 1
	} else {
		badge = ""
	}

	nameColor := t.Fg
	if fade > 0 {
		nameColor = t.Dimmed(nameColor, fade)
	}
	if ctx.selected {
		nameColor = t.Selected
	}
	c.text(1, 0, " "+shorten(n.Name, avail)+" ", nameColor, true)
	if badge != "" {
		c.textRight(0, 0, w-2, " "+badge+" ", col, true)
	}
}

func phaseBadge(n *model.Node) string {
	switch n.Phase {
	case model.PhaseProvisioning:
		return "◇ new"
	case model.PhaseDraining:
		return "▼ drain"
	case model.PhaseDeleting:
		return "✕ term"
	case model.PhaseCordoned:
		return "⏸ cord"
	case model.PhaseNotReady:
		return "! down"
	case model.PhaseGone:
		return "✕"
	default:
		if n.CapacityType == "spot" {
			return "spot"
		}
		return ""
	}
}

// drawFooter writes the bottom-border summary: instance type, pod count, age.
func drawFooter(c *canvas, w, h int, v visible, col lipgloss.Color, ctx boxCtx, fade float64) {
	t := theme.Current
	n := v.node
	dim := t.Dim
	if fade > 0 {
		dim = t.Dimmed(dim, fade)
	}

	right := fmt.Sprintf("%dp", len(v.pods))
	if ctx.dimPods && v.matchCount != len(v.pods) {
		right = fmt.Sprintf("%d/%dp", v.matchCount, len(v.pods))
	}
	if !n.Created.IsZero() && w >= 24 {
		right += " " + model.HumanAge(time.Since(n.Created))
	}
	c.textRight(0, h-1, w-2, " "+right+" ", dim, false)

	// Whatever the right-hand summary did not use goes to the identity on the
	// left, computed rather than guessed so a narrow box shows a whole instance
	// type instead of an ellipsis.
	// Leave a clear gap between the two footer texts; they meet in the middle of
	// the bottom border and touching reads as corruption.
	avail := w - 8 - runewidth.StringWidth(right)
	left := n.InstanceType
	if n.NodePool != "" && avail >= runewidth.StringWidth(left)+len(n.NodePool)+3 {
		left = n.NodePool + " · " + left
	}
	if left != "" && avail > 2 {
		c.text(2, h-1, " "+shorten(left, avail)+" ", dim, false)
	}
}

// drawPodsInterior lays out the pod cells above two utilisation meters.
func drawPodsInterior(c *canvas, v visible, track *anim.Track, ctx boxCtx, accent, cardBg lipgloss.Color) {
	t := theme.Current
	// Meters are the thing you cannot do without, so they get their rows first
	// and pods take what is left.
	meterRows := min(2, c.h)
	podRows := c.h - meterRows
	if podRows > 1 {
		podRows-- // a blank line separating the cells from the meters
	}
	if podRows > 0 {
		// The pod area is an inset well: unallocated capacity is visibly a hole
		// in the card rather than page background showing through, which is what
		// makes a nearly-empty node still read as a node.
		well := t.Well
		if cardBg != t.Card {
			well = theme.Mix(t.Well, cardBg, 0.5)
		}
		c.rect(0, 0, c.w, podRows, well)
		drawPodCells(c, 0, 0, c.w, podRows, v, ctx, well)
		if v.node.Phase == model.PhaseDraining || v.node.Phase == model.PhaseDeleting {
			// Hatch only the free capacity, and only in the pod area: the stripes
			// read as "this space is being given up" while leaving the pods that
			// are still running crisp, and leaving the meters untouched.
			hatch(c, 0, 0, c.w, podRows, accent, well)
		}
	}
	base := c.h - meterRows
	if meterRows >= 1 {
		drawMeter(c, base, c.w, "cpu", track.CPU, v.node.Requests.CPUMilli, v.node.Allocatable.CPUMilli, false)
	}
	if meterRows >= 2 {
		drawMeter(c, base+1, c.w, "mem", track.Mem, v.node.Requests.MemBytes, v.node.Allocatable.MemBytes, true)
	}
}

// drawMeter renders one labelled horizontal gauge. The label and percentage are
// drawn after the fill with per-cell contrast, so they stay readable whether
// they land on the filled or empty part.
func drawMeter(c *canvas, y, w int, label string, frac float64, used, total int64, isMem bool) {
	t := theme.Current
	on, off := t.Util(frac), t.Empty
	// Fill, then text (so contrast is picked against the final background), then
	// the sub-cell tip last (so it can only land on a blank cell).
	c.hMeter(0, y, w, frac, on, off)

	// Label flush left: with a gap, a nearly-empty meter drew its sub-cell tip
	// immediately beside the label and read as "▊cpu".
	c.textContrast(0, y, label, true)
	if w >= 22 {
		var amount string
		if isMem {
			amount = fmt.Sprintf("%s/%s", model.HumanMem(used), model.HumanMem(total))
		} else {
			amount = fmt.Sprintf("%s/%s", model.HumanCPU(used), model.HumanCPU(total))
		}
		c.textContrastCenter(0, y, w, amount, false)
	}
	c.textContrastRight(0, y, w-1, fmt.Sprintf("%3.0f%%", clamp01(frac)*100), true)

	c.hMeterTip(0, y, w, frac, on, off)
}

// drawGauges is the node-only mode: two tall gauges filling from the bottom,
// with the percentage in large text. This is the view that reads from the back
// of a room.
func drawGauges(c *canvas, v visible, track *anim.Track, ctx boxCtx) {
	t := theme.Current
	if c.w < 5 {
		drawMeter(c, 0, c.w, "cpu", track.CPU, 0, 0, false)
		return
	}
	half := (c.w - 1) / 2
	cpuOn, memOn := t.Util(track.CPU), t.Util(track.Mem)
	c.vMeter(0, 0, half, c.h, track.CPU, cpuOn, t.Empty)
	c.vMeter(c.w-half, 0, half, c.h, track.Mem, memOn, t.Empty)

	mid := c.h / 2
	c.textContrastCenter(0, mid, half, fmt.Sprintf("%.0f%%", track.CPU*100), true)
	c.textContrastCenter(c.w-half, mid, half, fmt.Sprintf("%.0f%%", track.Mem*100), true)
	if c.h >= 3 {
		c.textContrastCenter(0, mid+1, half, "cpu", false)
		c.textContrastCenter(c.w-half, mid+1, half, "mem", false)
	}
	// Pod count on the top row: in this mode it is the only thing said about
	// pods, so it should not be buried in the footer.
	if c.h >= 4 {
		label := "pods"
		if len(v.pods) == 1 {
			label = "pod"
		}
		c.textContrastCenter(0, 0, c.w, fmt.Sprintf("%d %s", len(v.pods), label), false)
	}

	// Tips last, so a half-filled row cannot land across a label.
	c.vMeterTip(0, 0, half, c.h, track.CPU, cpuOn, t.Empty)
	c.vMeterTip(c.w-half, 0, half, c.h, track.Mem, memOn, t.Empty)
}

// drawPodCells packs pods into the cell area, each pod sized by its CPU request
// relative to the node's allocatable CPU. That proportionality is the whole idea
// borrowed from kube-ops-view: a big pod looks big.
func drawPodCells(c *canvas, x, y, w, h int, v visible, ctx boxCtx, bg lipgloss.Color) {
	t := theme.Current
	total := w * h
	if total <= 0 || len(v.pods) == 0 {
		return
	}

	sizes, overflow := podCellSizes(v, total)
	pos := 0
	for i, p := range v.pods {
		size := sizes[i]
		if size == 0 {
			break // ran out of room; the footer count still reports the truth
		}
		col := podColor(p, v.matched[i], ctx)
		glyph := podGlyph(p)
		for k := 0; k < size && pos < total; k++ {
			cx, cy := x+pos%w, y+pos/w
			c.fillRect(cx, cy, 1, 1, glyph, col, bg)
			pos++
		}
	}
	// An overflow marker is honest about not having drawn everything.
	if overflow {
		c.text(x+w-1, y+h-1, "+", t.Warn, true)
	}
}

// podCellSizes allocates cells proportionally, guaranteeing every pod at least
// one cell until space runs out. Sorting is already stable in the store, so a
// pod keeps its position between frames. The bool reports that at least one pod
// could not be drawn.
func podCellSizes(v visible, total int) ([]int, bool) {
	alloc := v.node.Allocatable.CPUMilli
	sizes := make([]int, len(v.pods))
	remaining := total
	for i, p := range v.pods {
		if remaining <= 0 {
			return sizes, true
		}
		size := 1
		if alloc > 0 {
			size = int(float64(p.Requests.CPUMilli)/float64(alloc)*float64(total) + 0.5)
		}
		size = clampInt(size, 1, remaining)
		sizes[i] = size
		remaining -= size
	}
	return sizes, false
}

// podColor maps a pod to its cell colour. Pods are coloured by state and only
// by state: a colour with a fixed, stated meaning beats a hash of a workload
// name that nobody can decode, and it keeps the legend to a single row.
func podColor(p *model.Pod, matched bool, ctx boxCtx) lipgloss.Color {
	t := theme.Current
	if ctx.dimPods && !matched {
		return t.PodDim
	}
	switch p.Phase {
	case model.PodFailed:
		return t.PodFailed
	case model.PodPending:
		return t.PodPending
	case model.PodTerminating:
		// Pulse toward the deleting red so an eviction is visible even in a box
		// you are not looking directly at.
		return theme.Mix(t.PodTerminating, t.PhaseColor(int(model.PhaseDeleting)),
			0.35*ctx.reg.Pulse(ctx.reg.Pod(p.Key()), pulseDeleting))
	}
	// New pods flash bright and settle into the running colour.
	if track := ctx.reg.Pod(p.Key()); track.Enter < 1 {
		return theme.Mix(t.Flash, t.PodRunning, track.EnterEase())
	}
	return t.PodRunning
}

// stateColor is the legend's view of the same mapping, without the animation.
func stateColor(state string) lipgloss.Color {
	t := theme.Current
	switch state {
	case "running":
		return t.PodRunning
	case "pending":
		return t.PodPending
	case "terminating":
		return t.PodTerminating
	case "failed":
		return t.PodFailed
	default:
		return t.PodDim
	}
}

func podGlyph(p *model.Pod) rune {
	switch p.Phase {
	case model.PodPending:
		return glyphPending
	case model.PodTerminating:
		return glyphTerminating
	case model.PodFailed:
		return glyphFailed
	default:
		return glyphRunning
	}
}

// hatch overlays a sparse diagonal pattern over the empty cells of a region,
// marking a node as being taken out of service independently of colour. Occupied
// cells are left alone so pod colours stay readable.
func hatch(c *canvas, x0, y0, w, h int, col, bg lipgloss.Color) {
	// Sparse: at step 3 a mostly-empty draining node became a solid field of
	// slashes with no space left to read as space.
	const step = 5
	shade := theme.Mix(col, bg, 0.6)
	for y := y0; y < y0+h && y < c.h; y++ {
		for x := x0 + (y*2)%step; x < x0+w && x < c.w; x += step {
			if cl := c.at(x, y); cl.r == ' ' {
				cl.r, cl.fg = '╱', shade
			}
		}
	}
}

// blit copies src into dst at (x,y), revealing only the leftmost `reveal`
// fraction of columns. The wipe is what makes a new box look like it is being
// drawn rather than pasted.
func blit(dst, src *canvas, x, y int, reveal float64) {
	cols := int(float64(src.w)*clamp01(reveal) + 0.5)
	for sy := 0; sy < src.h; sy++ {
		for sx := 0; sx < cols; sx++ {
			if dst.inBounds(x+sx, y+sy) {
				*dst.at(x+sx, y+sy) = *src.at(sx, sy)
			}
		}
	}
}

func dimCanvas(c *canvas, f float64) {
	t := theme.Current
	for i := range c.cells {
		cl := &c.cells[i]
		cl.fg = t.Dimmed(cl.fg, f)
		cl.bg = t.Dimmed(cl.bg, f)
	}
}

// padLines centres the drawn box vertically in its h-row slot, so a growing or
// collapsing box expands from and contracts toward its middle.
func padLines(lines []string, w, h int, t theme.Theme) []string {
	if len(lines) >= h {
		return lines[:h]
	}
	blank := lipgloss.NewStyle().Background(t.Bg).Render(spaces(w))
	top := (h - len(lines)) / 2
	out := make([]string, 0, h)
	for i := 0; i < top; i++ {
		out = append(out, blank)
	}
	out = append(out, lines...)
	for len(out) < h {
		out = append(out, blank)
	}
	return out
}

// --- contrast-aware text ---

// textContrast draws text picking a foreground per cell from that cell's
// background luminance. It is what lets a label sit across the boundary of a
// fill and stay legible on both sides.
func (c *canvas) textContrast(x, y int, s string, bold bool) {
	for _, r := range s {
		if x >= c.w {
			return
		}
		if c.inBounds(x, y) {
			cl := c.at(x, y)
			cl.r, cl.fg, cl.bold = r, contrastOn(cl.bg), bold
		}
		x += max(1, runewidth.RuneWidth(r))
	}
}

func (c *canvas) textContrastRight(x, y, w int, s string, bold bool) {
	c.textContrast(x+w-runewidth.StringWidth(s), y, s, bold)
}

func (c *canvas) textContrastCenter(x, y, w int, s string, bold bool) {
	if runewidth.StringWidth(s) > w {
		return
	}
	c.textContrast(x+(w-runewidth.StringWidth(s))/2, y, s, bold)
}

var contrastCache = map[lipgloss.Color]lipgloss.Color{}

// contrastOn returns near-black or near-white, whichever reads better on bg.
// The 0.45 threshold is biased toward white text because the mid-ramp yellows
// are perceptually much brighter than their luminance suggests on a projector.
func contrastOn(bg lipgloss.Color) lipgloss.Color {
	if c, ok := contrastCache[bg]; ok {
		return c
	}
	out := lipgloss.Color("#ffffff")
	if col, err := colorful.Hex(string(bg)); err == nil {
		if _, _, l := col.Hcl(); l > 0.45 {
			out = lipgloss.Color("#07090d")
		}
	}
	if len(contrastCache) > 2048 {
		contrastCache = map[lipgloss.Color]lipgloss.Color{}
	}
	contrastCache[bg] = out
	return out
}

// --- small helpers ---

func lerpInt(a, b int, f float64) int {
	return a + int(float64(b-a)*clamp01(f)+0.5)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sumInts(v []int) int {
	total := 0
	for _, x := range v {
		total += x
	}
	return total
}

// shorten trims a name to fit, keeping the tail: node names differ in their
// suffix far more often than their prefix.
func shorten(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	r := []rune(s)
	for len(r) > 0 && runewidth.StringWidth(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}

// truncHead keeps the beginning of a string, marking loss with an ellipsis.
func truncHead(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w, "…")
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return runewidth.FillRight("", n)
}
