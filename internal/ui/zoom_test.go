package ui

import (
	"strings"
	"testing"
)

// Zoom is the one piece of view state that changes card geometry, so it is the
// one most able to break the frame-rigidity invariant. These tests hold it to
// the same standard as the rest of the layout: monotonic, bounded, and exactly
// reversible back to "fit".

func TestZoomIsMonotonic(t *testing.T) {
	sizes := []struct{ w, h int }{{200, 60}, {120, 40}, {80, 24}}
	for _, mode := range []Mode{ModePods, ModeNodes} {
		for _, size := range sizes {
			for _, n := range []int{1, 3, 14, 60} {
				prevCols, prevArea := 1<<30, 0
				for z := zoomMin; z <= zoomMax; z++ {
					l := computeLayout(mode, size.w, size.h, n, z, 0)
					area := l.boxW * l.boxH
					if l.cols > prevCols {
						t.Errorf("mode=%v %dx%d n=%d zoom=%d: cols went up to %d from %d zooming in",
							mode, size.w, size.h, n, z, l.cols, prevCols)
					}
					if area < prevArea {
						t.Errorf("mode=%v %dx%d n=%d zoom=%d: card shrank to %dx%d zooming in",
							mode, size.w, size.h, n, z, l.boxW, l.boxH)
					}
					prevCols, prevArea = l.cols, area
				}
			}
		}
	}
}

func TestZoomStaysInsideTheViewport(t *testing.T) {
	for _, mode := range []Mode{ModePods, ModeNodes} {
		for _, size := range []struct{ w, h int }{{200, 60}, {100, 30}, {40, 14}} {
			for _, n := range []int{1, 7, 60} {
				for z := zoomMin; z <= zoomMax; z++ {
					l := computeLayout(mode, size.w, size.h, n, z, 0)
					if l.boxW > size.w || l.boxH > size.h {
						t.Errorf("mode=%v %dx%d n=%d zoom=%d: card %dx%d overflows the viewport",
							mode, size.w, size.h, n, z, l.boxW, l.boxH)
					}
					if l.cols < 1 || l.pages < 1 || l.boxW < 1 || l.boxH < 1 {
						t.Errorf("mode=%v %dx%d n=%d zoom=%d: degenerate layout %+v",
							mode, size.w, size.h, n, z, l)
					}
					if l.gridW != l.cols*l.boxW+gapX*(l.cols-1) || l.stepX != l.boxW+gapX {
						t.Errorf("mode=%v %dx%d n=%d zoom=%d: canvas %+v is not self-consistent",
							mode, size.w, size.h, n, z, l)
					}
				}
			}
		}
	}
}

// Zooming all the way in is what you do when someone asks about one node, so it
// has to actually get you there rather than stopping at a comfortable grid. The
// canvas keeps its columns; what changes is how much of it the window shows.
func TestFullZoomInShowsOneNode(t *testing.T) {
	for _, mode := range []Mode{ModePods, ModeNodes} {
		m := newTestModel(t, 200, 60, 60)
		m.setMode(mode)
		m.setZoom(zoomMax)

		// One card dominates the screen. Neighbours may still show a sliver: the
		// canvas is panned to whole cells, not snapped to card boundaries, which
		// is precisely what stops zooming from shunting the grid about.
		area := float64(m.lay.boxW*m.lay.boxH) / float64(m.w*m.gridHeight())
		if area < 0.75 {
			t.Errorf("mode=%v: max zoom gives the card %.0f%% of the screen, want most of it",
				mode, area*100)
		}
		if on := visibleCards(m); len(on) > 2 {
			t.Errorf("mode=%v: max zoom shows %d cards, want essentially one", mode, len(on))
		}
	}
}

// Zooming out has to show strictly more of the fleet than the automatic layout,
// or the key is a lie on the very cluster size it exists for.
func TestFullZoomOutShowsMoreNodes(t *testing.T) {
	m := newTestModel(t, 200, 60, 60)
	fit := len(visibleCards(m))
	m.setZoom(zoomMin)
	if out := len(visibleCards(m)); out <= fit {
		t.Errorf("zoomed out shows %d nodes, fit shows %d — want more", out, fit)
	}
}

func TestZoomFitIsTheAutomaticLayout(t *testing.T) {
	m := newTestModel(t, 160, 48, 14)
	fit := m.lay

	for i := 0; i < 3; i++ {
		m.zoomBy(1)
	}
	if m.lay == fit {
		t.Fatalf("zooming in three levels did not change the layout: %+v", fit)
	}
	if _, err := m.Run("zoom fit"); err != nil {
		t.Fatalf(":zoom fit: %v", err)
	}
	if m.lay != fit {
		t.Errorf("after :zoom fit layout is %+v, want the automatic %+v", m.lay, fit)
	}
}

func TestZoomClampsAndReports(t *testing.T) {
	m := newTestModel(t, 160, 48, 14)
	for i := 0; i < zoomMax+5; i++ {
		m.zoomBy(1)
	}
	if m.zoom != zoomMax {
		t.Errorf("zoom ran to %d, want it clamped at %d", m.zoom, zoomMax)
	}
	if !m.msgErr || !strings.Contains(m.msg, "fully zoomed in") {
		t.Errorf("at the limit the status said %q (err=%v), want a 'fully zoomed in' notice", m.msg, m.msgErr)
	}
	for i := 0; i < zoomMax-zoomMin+5; i++ {
		m.zoomBy(-1)
	}
	if m.zoom != zoomMin {
		t.Errorf("zoom ran to %d, want it clamped at %d", m.zoom, zoomMin)
	}
}

// The selected node is the reason you zoomed in; losing it off the bottom of a
// now-much-shorter page would defeat the whole feature.
func TestZoomKeepsTheSelectionOnScreen(t *testing.T) {
	m := newTestModel(t, 160, 48, 60)
	m.setCursor(41)
	name := m.cursorName

	for z := 0; z < zoomMax; z++ {
		m.zoomBy(1)
		if m.cursorName != name {
			t.Fatalf("zoom %d moved the selection to %q, want %q", m.zoom, m.cursorName, name)
		}
		on := false
		for _, idx := range visibleCards(m) {
			if idx == m.cursor {
				on = true
			}
		}
		if !on {
			t.Fatalf("zoom %d: the selected card is off screen", m.zoom)
		}
	}
}

// Frame rigidity, the invariant every other renderer test exists to protect,
// must survive every zoom level and not just the automatic one.
func TestFrameGeometryAcrossZoom(t *testing.T) {
	for _, size := range []struct{ w, h int }{{200, 60}, {100, 30}, {40, 14}} {
		for _, mode := range []Mode{ModePods, ModeNodes} {
			for _, n := range []int{0, 1, 14, 60} {
				for z := zoomMin; z <= zoomMax; z++ {
					m := newTestModel(t, size.w, size.h, n)
					m.setMode(mode)
					m.setZoom(z)
					assertFrame(t, m, size.w, size.h, mode, n, "zoom "+zoomLabel(m.lay.scale))
				}
			}
		}
	}
}

func TestZoomCommandErrors(t *testing.T) {
	m := newTestModel(t, 160, 48, 14)
	if _, err := m.Run("zoom sideways"); err == nil {
		t.Error("a nonsense zoom argument was accepted")
	}
	if _, err := m.Run("zoom 99"); err == nil {
		t.Error("an out-of-range zoom level was accepted")
	}
	m.setMode(ModeDense)
	if _, err := m.Run("zoom in"); err == nil {
		t.Error("zoom was accepted in dense mode, which has no card geometry")
	}
}

// Smoothness is a property, not a taste: a zoom step that redraws nothing reads
// as a broken key, and one that jumps several-fold reads as the screen being
// replaced rather than scaled.

func TestSteppedZoomAlwaysRedrawsSomething(t *testing.T) {
	// Card sizes are whole cells and clamped by the viewport, so identical
	// consecutive levels are common — with a single node on a small terminal most
	// of the range resolves to the same grid. Cover the shapes where that bites.
	sizes := []struct{ w, h int }{{200, 60}, {160, 44}, {120, 30}, {80, 24}}
	steps := []int{1, keyZoomStep} // the wheel steps one level, a keypress more

	for _, size := range sizes {
		for _, n := range []int{1, 3, 24, 60} {
			for _, step := range steps {
				for _, dir := range []int{1, -1} {
					m := newTestModel(t, size.w, size.h, n)
					if m.layoutFrame().grid <= 0 {
						continue
					}
					for i := 0; i < 60; i++ {
						before, beforeZoom := m.lay, m.zoom
						m.zoomBy(dir * step)
						if !m.lay.sameGeometry(before) {
							continue
						}
						// Standing still is only allowed at the end of the range,
						// and it has to say so rather than pretend it worked.
						if !m.msgErr || !strings.Contains(m.msg, "fully zoomed") {
							t.Fatalf("%dx%d n=%d step=%d dir=%d: level %d→%d redrew nothing and said %q",
								size.w, size.h, n, step, dir, beforeZoom, m.zoom, m.msg)
						}
						// And it has to be a real limit, not a level that happens
						// to resolve to the same grid as the one before it: those
						// are supposed to be skipped, not reported as the end.
						if m.zoom != zoomMin && m.zoom != zoomMax {
							t.Fatalf("%dx%d n=%d step=%d dir=%d: gave up at level %d, short of the range",
								size.w, size.h, n, step, dir, m.zoom)
						}
						break
					}
				}
			}
		}
	}
}

// One trackpad flick is a dozen notches or so. It should move a level or three,
// not cross most of the range.
func TestOneFlickIsAModestChangeOfScale(t *testing.T) {
	const flickNotches = 12
	m := newTestModel(t, 160, 44, 24)
	fit := m.lay

	for i := 0; i < flickNotches; i++ {
		m.zoomByWheel(1, 0, 0)
	}
	if got := float64(m.lay.boxW) / float64(fit.boxW); got > 2 {
		t.Errorf("one flick in scaled the card %.2f×, want no more than 2×", got)
	}
	if m.lay == fit {
		t.Error("one flick in did not change the layout at all")
	}
}

// The keyboard wants a coarser grain than the wheel: reaching either end during
// a talk must not be a dozen presses.
func TestKeyboardZoomReachesTheEndsQuickly(t *testing.T) {
	for _, dir := range []int{1, -1} {
		m := newTestModel(t, 160, 44, 24)
		presses := 0
		for m.zoom != zoomMax && m.zoom != zoomMin && presses < 40 {
			m.zoomBy(dir * keyZoomStep)
			presses++
		}
		if presses > 7 {
			t.Errorf("dir=%d: %d presses to reach level %d, want at most 7", dir, presses, m.zoom)
		}
	}
}

func TestZoomLabelReadsAsAMultiplier(t *testing.T) {
	if got := zoomLabel(1); got != "fit" {
		t.Errorf("zoomLabel(1) = %q, want fit", got)
	}
	m := newTestModel(t, 160, 44, 20)
	for _, z := range []int{zoomMin, -1, 1, zoomMax} {
		m.setZoom(z)
		got := zoomLabel(m.lay.scale)
		if !strings.HasSuffix(got, "×") {
			t.Errorf("zoom %d is labelled %q, want a multiplier", z, got)
		}
	}
	m.setZoom(1)
	in := zoomLabel(m.lay.scale)
	m.setZoom(-1)
	if in == zoomLabel(m.lay.scale) {
		t.Error("zooming in and out are labelled the same")
	}
}
