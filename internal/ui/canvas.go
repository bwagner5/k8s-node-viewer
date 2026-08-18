package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// canvas is a fixed-size grid of styled cells.
//
// Compositing into a cell grid — rather than assembling styled substrings —
// is what makes the node box readable: a utilisation fill, a row of pod cells
// and a text label can be drawn independently, in any order, and overlap
// correctly. Rendering coalesces runs of identical style, so a box that is
// mostly one colour costs about as many escape sequences as a plain string.
type canvas struct {
	w, h  int
	cells []cell
}

type cell struct {
	r    rune
	fg   lipgloss.Color
	bg   lipgloss.Color
	bold bool
}

func newCanvas(w, h int, bg, fg lipgloss.Color) *canvas {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	c := &canvas{w: w, h: h, cells: make([]cell, w*h)}
	blank := cell{r: ' ', fg: fg, bg: bg}
	for i := range c.cells {
		c.cells[i] = blank
	}
	return c
}

func (c *canvas) inBounds(x, y int) bool { return x >= 0 && y >= 0 && x < c.w && y < c.h }

func (c *canvas) at(x, y int) *cell { return &c.cells[y*c.w+x] }

// rect fills a background rectangle, leaving glyphs and foregrounds intact so a
// fill drawn after text still reads.
func (c *canvas) rect(x, y, w, h int, bg lipgloss.Color) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			if c.inBounds(x+dx, y+dy) {
				c.at(x+dx, y+dy).bg = bg
			}
		}
	}
}

// fillRect paints both glyph and colours — used for pod cells and solid blocks.
func (c *canvas) fillRect(x, y, w, h int, r rune, fg, bg lipgloss.Color) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			if c.inBounds(x+dx, y+dy) {
				*c.at(x+dx, y+dy) = cell{r: r, fg: fg, bg: bg}
			}
		}
	}
}

// text draws s at (x,y), preserving each cell's existing background so labels
// can sit on top of a fill. Wide runes consume two cells; the trailing cell is
// blanked to keep column alignment exact.
func (c *canvas) text(x, y int, s string, fg lipgloss.Color, bold bool) {
	if y < 0 || y >= c.h {
		return
	}
	for _, r := range s {
		if x >= c.w {
			return
		}
		w := runewidth.RuneWidth(r)
		if w == 0 {
			continue // combining marks would desynchronise the grid
		}
		if x >= 0 {
			cur := c.at(x, y)
			cur.r, cur.fg, cur.bold = r, fg, bold
			if w == 2 && c.inBounds(x+1, y) {
				next := c.at(x+1, y)
				next.r, next.fg, next.bold = 0, fg, bold // 0 = skipped continuation cell
			}
		}
		x += w
	}
}

// textRight right-aligns s so it ends at column x+w-1.
func (c *canvas) textRight(x, y, w int, s string, fg lipgloss.Color, bold bool) {
	c.text(x+w-runewidth.StringWidth(s), y, s, fg, bold)
}

// textCenter centres s in the span [x, x+w).
func (c *canvas) textCenter(x, y, w int, s string, fg lipgloss.Color, bold bool) {
	c.text(x+max(0, (w-runewidth.StringWidth(s))/2), y, s, fg, bold)
}

// hMeter paints a horizontal fill across [x, x+w) at row y: full cells of
// background `on`, then a half-block to give sub-cell resolution so a slowly
// filling meter animates smoothly instead of stepping.
func (c *canvas) hMeter(x, y, w int, frac float64, on, off lipgloss.Color) {
	// Clip rather than trust the caller: layouts on narrow terminals routinely
	// compute column positions that fall off the canvas, and a meter running
	// off the edge should draw nothing, not panic.
	if x < 0 {
		w, x = w+x, 0
	}
	if w <= 0 || x >= c.w || y < 0 || y >= c.h {
		return
	}
	w = min(w, c.w-x)
	exact := clamp01(frac) * float64(w)
	full := int(exact)
	c.rect(x, y, full, 1, on)
	c.rect(x+full, y, w-full, 1, off)
}

// hMeterTip draws the sub-cell remainder of a meter as a partial block, giving
// a slowly filling bar smooth motion instead of whole-cell steps.
//
// Separate from hMeter, and meant to be called *after* any text on the row: it
// only writes into a still-blank cell, so a 3% meter no longer renders its tip
// on top of the label as "▊cpu". Callers that have no text can call it
// immediately.
func (c *canvas) hMeterTip(x, y, w int, frac float64, on, off lipgloss.Color) {
	if x < 0 {
		w, x = w+x, 0
	}
	if w <= 0 || x >= c.w || y < 0 || y >= c.h {
		return
	}
	w = min(w, c.w-x)
	exact := clamp01(frac) * float64(w)
	full := int(exact)
	rem := exact - float64(full)
	if rem < 0.25 || full >= w || !c.inBounds(x+full, y) {
		return
	}
	r := '▌'
	if rem >= 0.75 {
		r = '▊'
	}
	if cl := c.at(x+full, y); cl.r == ' ' {
		*cl = cell{r: r, fg: on, bg: off}
	}
}

// vMeter fills the rect from the bottom up. Used for the big node-only mode
// where the whole box is one utilisation gauge.
func (c *canvas) vMeter(x, y, w, h int, frac float64, on, off lipgloss.Color) {
	if x < 0 {
		w, x = w+x, 0
	}
	if y < 0 {
		h, y = h+y, 0
	}
	if h <= 0 || w <= 0 || x >= c.w || y >= c.h {
		return
	}
	w, h = min(w, c.w-x), min(h, c.h-y)
	exact := clamp01(frac) * float64(h)
	full := int(exact)
	c.rect(x, y, w, h-full, off)
	c.rect(x, y+h-full, w, full, on)
}

// vMeterTip draws the sub-cell remainder of a vertical gauge as a row of half
// blocks. Like hMeterTip it must run after any text on the gauge, or the
// half-row lands across the label and renders as "▄▄▄▄mem▄▄▄▄".
func (c *canvas) vMeterTip(x, y, w, h int, frac float64, on, off lipgloss.Color) {
	if x < 0 {
		w, x = w+x, 0
	}
	if y < 0 {
		h, y = h+y, 0
	}
	if h <= 0 || w <= 0 || x >= c.w || y >= c.h {
		return
	}
	w, h = min(w, c.w-x), min(h, c.h-y)
	exact := clamp01(frac) * float64(h)
	full := int(exact)
	if rem := exact - float64(full); rem < 0.3 || full >= h {
		return
	}
	row := y + h - full - 1
	for dx := 0; dx < w; dx++ {
		if !c.inBounds(x+dx, row) {
			continue
		}
		if cl := c.at(x+dx, row); cl.r == ' ' {
			*cl = cell{r: '▄', fg: on, bg: off}
		}
	}
}

// styleCache avoids rebuilding an identical lipgloss.Style for every run on
// every frame; at 20fps over a few hundred boxes that allocation is measurable.
var styleCache = map[cell]lipgloss.Style{}

// maxStyleCache bounds the cache: interpolated flash and pulse colours are
// effectively unbounded over a long session, so drop the whole map rather than
// grow forever. Rebuilding it costs one frame of allocations.
const maxStyleCache = 4096

func styleFor(key cell) lipgloss.Style {
	key.r = 0
	if s, ok := styleCache[key]; ok {
		return s
	}
	if len(styleCache) >= maxStyleCache {
		styleCache = make(map[cell]lipgloss.Style, maxStyleCache)
	}
	s := lipgloss.NewStyle().Foreground(key.fg).Background(key.bg).Bold(key.bold)
	styleCache[key] = s
	return s
}

// String renders the canvas, coalescing adjacent cells that share a style.
func (c *canvas) String() string {
	var out strings.Builder
	var run strings.Builder
	for y := 0; y < c.h; y++ {
		if y > 0 {
			out.WriteByte('\n')
		}
		var cur cell
		started := false
		flush := func() {
			if started && run.Len() > 0 {
				out.WriteString(styleFor(cur).Render(run.String()))
			}
			run.Reset()
		}
		for x := 0; x < c.w; x++ {
			cl := *c.at(x, y)
			if cl.r == 0 {
				continue // continuation cell of a wide rune
			}
			if !started || cl.fg != cur.fg || cl.bg != cur.bg || cl.bold != cur.bold {
				flush()
				cur, started = cl, true
			}
			run.WriteRune(cl.r)
		}
		flush()
	}
	return out.String()
}

// Lines returns the rendered rows individually, for callers that need to place
// the canvas inside another layout.
func (c *canvas) Lines() []string { return strings.Split(c.String(), "\n") }

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
