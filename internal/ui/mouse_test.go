package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func wheel(btn tea.MouseButton, x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: btn, Action: tea.MouseActionPress}
}

// flick is a burst of wheel notches, the way a trackpad actually delivers them.
func flick(m *Model, btn tea.MouseButton, notches int) {
	for i := 0; i < notches; i++ {
		m.handleMouse(wheel(btn, 10, 10))
	}
}

// --- canvas helpers ---
//
// The grid is a fixed canvas with the viewport panned over it, so a card's
// screen position is its canvas position minus the pan. Tests ask for it the
// same way the renderer works it out.

func gridTop(m *Model) int {
	f := m.layoutFrame()
	return f.header + f.legend
}

// cardXY is the screen position of the top-left cell of card idx.
func cardXY(m *Model, idx int) (int, int) {
	row, col := idx/max(1, m.lay.cols), idx%max(1, m.lay.cols)
	return col*m.lay.stepX - m.panX, gridTop(m) + row*m.lay.stepY - m.panY
}

// cardCentre is a point comfortably inside card idx.
func cardCentre(m *Model, idx int) (int, int) {
	x, y := cardXY(m, idx)
	return x + m.lay.boxW/2, y + m.lay.boxH/2
}

// visibleCards lists the cards with any part of them on screen.
func visibleCards(m *Model) []int {
	var out []int
	h := m.gridHeight()
	for idx := range m.vis {
		x, y := cardXY(m, idx)
		y -= gridTop(m)
		if x+m.lay.boxW > 0 && x < m.w && y+m.lay.boxH > 0 && y < h {
			out = append(out, idx)
		}
	}
	return out
}

// panIsFree reports whether the viewport can move on both axes, i.e. that the
// canvas is bigger than the window and the pan is not against an edge. Where it
// is not free, a card cannot be held still and must not be asserted about.
func panIsFree(m *Model) bool {
	h := m.gridHeight()
	return m.lay.gridW > m.w && m.panX > 0 && m.panX < m.lay.gridW-m.w &&
		m.lay.gridH > h && m.panY > 0 && m.panY < m.lay.gridH-h
}

// flickAt is a burst of notches delivered at one pointer position.
func flickAt(m *Model, btn tea.MouseButton, notches, x, y int) {
	for i := 0; i < notches; i++ {
		m.handleMouse(wheel(btn, x, y))
	}
}

func scrollWheel(btn tea.MouseButton, x, y int) tea.MouseMsg {
	msg := wheel(btn, x, y)
	msg.Shift = true
	return msg
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

// The regression this whole file exists for: scrolling used to move the
// viewport and leave the selection behind, and the next snapshot — up to ten a
// second — ran ensureVisible and dragged the viewport back. The wheel looked
// dead on a live cluster while working perfectly in every test.
func TestWheelScrollSurvivesTheNextSnapshot(t *testing.T) {
	m := newTestModel(t, 120, 30, 60)
	if m.lay.pages <= m.visibleRows() {
		t.Fatalf("test needs a grid that scrolls: %d pages, %d visible", m.lay.pages, m.visibleRows())
	}

	for i := 0; i < 3; i++ {
		m.handleMouse(scrollWheel(tea.MouseButtonWheelDown, 10, 10))
	}
	scrolled := m.panY
	if scrolled == 0 {
		t.Fatal("three wheel-down notches did not scroll at all")
	}

	m.applySnapshot(testSnapshot(60))
	if m.panY != scrolled {
		t.Errorf("a snapshot reset the scroll from %d to %d", scrolled, m.panY)
	}
}

func TestWheelScrollsBothWaysAndStops(t *testing.T) {
	m := newTestModel(t, 120, 30, 60)

	for i := 0; i < 200; i++ {
		m.handleMouse(scrollWheel(tea.MouseButtonWheelDown, 10, 10))
	}
	if want := m.lay.gridH - m.gridHeight(); m.panY != want {
		t.Errorf("panned to %d at the bottom, want the last row flush, %d", m.panY, want)
	}
	for i := 0; i < 200; i++ {
		m.handleMouse(scrollWheel(tea.MouseButtonWheelUp, 10, 10))
	}
	if m.panY != 0 {
		t.Errorf("panned to %d at the top, want 0", m.panY)
	}
}

// A grid that already shows everything has nothing to scroll, and must not
// pretend otherwise by moving the selection around under the wheel.
func TestWheelDoesNothingWhenEverythingFits(t *testing.T) {
	m := newTestModel(t, 160, 44, 4)
	if m.lay.pages > m.visibleRows() {
		t.Skip("layout scrolls at this size; nothing to assert")
	}
	before, pan := m.cursor, m.panY
	m.handleMouse(scrollWheel(tea.MouseButtonWheelDown, 10, 10))
	if m.panY != pan || m.cursor != before {
		t.Errorf("wheel moved pan=%d cursor=%d on a grid that fits", m.panY, m.cursor)
	}
}

// A bare two-finger scroll zooms, because a pinch cannot reach a terminal
// program and this is the nearest gesture that can.
func TestPlainWheelZooms(t *testing.T) {
	m := newTestModel(t, 160, 44, 20)

	flick(m, tea.MouseButtonWheelUp, wheelNotchesPerZoom)
	if m.zoom != 1 {
		t.Errorf("wheel up: zoom is %d, want 1", m.zoom)
	}
	flick(m, tea.MouseButtonWheelDown, 2*wheelNotchesPerZoom)
	if m.zoom != -1 {
		t.Errorf("wheel down: zoom is %d, want -1", m.zoom)
	}
}

// A trackpad flick delivers a burst of notches, and the zoom range is ten levels
// end to end. One level per notch would make every flick slam into a limit.
func TestWheelNotchesAccumulateIntoOneZoomLevel(t *testing.T) {
	m := newTestModel(t, 160, 44, 20)
	for i := 1; i < wheelNotchesPerZoom; i++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, 10, 10))
		if m.zoom != 0 {
			t.Fatalf("notch %d of %d already zoomed to %d", i, wheelNotchesPerZoom, m.zoom)
		}
	}
	m.handleMouse(wheel(tea.MouseButtonWheelUp, 10, 10))
	if m.zoom != 1 {
		t.Errorf("after %d notches zoom is %d, want 1", wheelNotchesPerZoom, m.zoom)
	}
}

// Reversing a flick should respond within a notch or three, not first pay back
// everything banked in the other direction.
func TestReversingTheWheelResetsTheAccumulator(t *testing.T) {
	m := newTestModel(t, 160, 44, 20)
	flick(m, tea.MouseButtonWheelUp, wheelNotchesPerZoom-1) // banked, no level yet
	flick(m, tea.MouseButtonWheelDown, wheelNotchesPerZoom)
	if m.zoom != -1 {
		t.Errorf("zoom is %d after reversing, want -1", m.zoom)
	}
}

// One long flick at the end of the range must not fill the status line with a
// dozen identical complaints.
func TestWheelAtTheZoomLimitIsQuiet(t *testing.T) {
	m := newTestModel(t, 160, 44, 20)
	m.setZoom(zoomMax)
	m.notify("", false)
	flick(m, tea.MouseButtonWheelUp, 30)
	if m.zoom != zoomMax {
		t.Errorf("zoom moved past the limit to %d", m.zoom)
	}
	if m.msg != "" {
		t.Errorf("the wheel complained at the limit: %q", m.msg)
	}
}

// Dense mode has no card geometry to scale, so there the wheel keeps scrolling
// rather than rejecting every notch of a flick.
func TestWheelScrollsInDenseMode(t *testing.T) {
	m := newTestModel(t, 120, 30, 60)
	m.setMode(ModeDense)
	flick(m, tea.MouseButtonWheelDown, 3)
	if m.panY == 0 {
		t.Error("the wheel did not scroll the dense table")
	}
	if m.zoom != 0 {
		t.Errorf("the wheel zoomed in dense mode: %d", m.zoom)
	}
}

// Every modifier scrolls, because which one reaches the application depends on
// the terminal: several eat ctrl+wheel to resize their own font.
func TestEveryModifierWheelScrolls(t *testing.T) {
	for _, mod := range []string{"ctrl", "alt", "shift"} {
		m := newTestModel(t, 120, 30, 60)
		msg := wheel(tea.MouseButtonWheelDown, 10, 10)
		switch mod {
		case "ctrl":
			msg.Ctrl = true
		case "alt":
			msg.Alt = true
		case "shift":
			msg.Shift = true
		}
		m.handleMouse(msg)
		if m.panY == 0 {
			t.Errorf("%s+wheel did not scroll", mod)
		}
		if m.zoom != 0 {
			t.Errorf("%s+wheel zoomed to %d instead of scrolling", mod, m.zoom)
		}
	}
}

// Zooming with the pointer over a card should be about that card, the way a map
// zooms about the cursor rather than about the middle.
func TestWheelZoomsOnTheNodeUnderThePointer(t *testing.T) {
	m := newTestModel(t, 160, 44, 20)
	x, y := cardCentre(m, 1) // second column, first row
	want, ok := m.hitTest(x, y)
	if !ok {
		t.Fatalf("no card at %d,%d", x, y)
	}

	for i := 0; i < wheelNotchesPerZoom; i++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))
	}
	if m.cursor != want {
		t.Errorf("zoom selected node %d, want the one under the pointer, %d", m.cursor, want)
	}
}

// Replays what an Apple trackpad actually sends. A plain vertical two-finger
// scroll emits a horizontal event for very nearly every vertical one — 99
// against 100 in the session that produced this test. Those must move nothing.
//
// They used to move the selection one card each, which walked it to the end of
// the list while ensureVisible panned the grid along behind it. In a two-column
// grid the end of the list is the bottom-left card, which is where every zoom
// ended up regardless of where it was aimed.
func TestTrackpadHorizontalNoiseMovesNothing(t *testing.T) {
	m := newTestModel(t, 212, 78, 3) // the reporter's terminal and cluster
	x, y := cardCentre(m, 1)
	want := m.vis[1].node.Name
	cursor := m.cursorName

	for i := 0; i < 40; i++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))
		// The horizontal component of the same flick.
		side := tea.MouseButtonWheelRight
		if i%3 == 0 {
			side = tea.MouseButtonWheelLeft
		}
		pan := m.panX
		m.handleMouse(wheel(side, x, y))
		if m.panX != pan {
			t.Fatalf("notch %d: the horizontal component of a flick panned the view", i)
		}
		if m.anchorName != want {
			t.Fatalf("notch %d: the zoom anchor moved to %q, want %q", i, m.anchorName, want)
		}
		_ = cursor
	}
	if m.vis[m.anchorIndex()].node.Name != want {
		t.Errorf("zoomed onto %q, want the card that was aimed at, %q",
			m.vis[m.anchorIndex()].node.Name, want)
	}
	// The card dominating the screen is the one aimed at, whole and unclipped —
	// not whichever card the list happened to end on. Slivers of a neighbour are
	// allowed: the canvas pans by cells, not by whole cards.
	idx := m.anchorIndex()
	cx, cy := cardXY(m, idx)
	cy -= gridTop(m)
	if cx < 0 || cy < 0 || cx+m.lay.boxW > m.w || cy+m.lay.boxH > m.gridHeight() {
		t.Errorf("the aimed card is clipped at (%d,%d) %dx%d in %dx%d",
			cx, cy, m.lay.boxW, m.lay.boxH, m.w, m.gridHeight())
	}
	if area := float64(m.lay.boxW*m.lay.boxH) / float64(m.w*m.gridHeight()); area < 0.6 {
		t.Errorf("the aimed card has %.0f%% of the screen after 10 levels in", area*100)
	}
}

// Horizontal scrolling on its own is a real gesture, and a zoomed-in canvas is
// wider than the window, so it pans.
func TestHorizontalWheelPansWhenItIsTheGesture(t *testing.T) {
	m := newTestModel(t, 160, 44, 40)
	m.setZoom(6)
	if m.lay.gridW <= m.w {
		t.Skip("canvas is not wider than the window at this zoom")
	}
	m.wheelAt = time.Now().Add(-time.Hour) // no vertical gesture in flight
	before, sel := m.panX, m.cursorName

	for i := 0; i < panNotchesPerCard; i++ {
		m.handleMouse(wheel(tea.MouseButtonWheelRight, 80, 20))
	}
	if m.panX == before {
		t.Error("a deliberate horizontal scroll did not pan")
	}
	if m.cursorName != sel {
		t.Errorf("panning moved the selection to %q, want %q", m.cursorName, sel)
	}
}

// layoutFrame sheds the legend and then the header as the terminal shrinks, so
// a hit test with those heights hard-coded puts every click a card too high.
func TestClickHitsTheRightCardWhenChromeIsShed(t *testing.T) {
	for _, size := range []struct{ w, h int }{{160, 44}, {120, 13}, {100, 9}} {
		m := newTestModel(t, size.w, size.h, 12)
		if m.layoutFrame().grid <= 0 {
			continue
		}
		x, y := cardCentre(m, 0)
		idx, ok := m.hitTest(x, y)
		if !ok {
			t.Errorf("%dx%d: no card at the first card's centre (%d,%d)", size.w, size.h, x, y)
			continue
		}
		if idx != 0 {
			t.Errorf("%dx%d: click on the first card hit %d", size.w, size.h, idx)
		}
	}
}

func TestClickOutsideTheGridSelectsNothing(t *testing.T) {
	m := newTestModel(t, 160, 44, 12)
	before := m.cursor
	m.handleMouse(click(10, 0))    // header
	m.handleMouse(click(10, 44-1)) // status line
	if m.cursor != before {
		t.Errorf("a click on the chrome moved the selection to %d", m.cursor)
	}
}

// The help overlay outgrew the screen when zoom added its keys and its command.
// Every line has to be reachable at any terminal size, or the overlay is lying
// about what the program can do.
func TestHelpOverlayCanReachEveryLine(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 44}, {120, 30}, {100, 20}, {80, 12}} {
		for _, demo := range []bool{false, true} {
			m := newTestModel(t, size.w, size.h, 8)
			if demo {
				m.demo = stubDemo{}
			}
			m.showHelp = true

			lines := helpLines(demo)
			last := lines[len(lines)-1]
			seen := false
			for step := 0; step <= m.helpMaxScroll(); step++ {
				m.helpScroll = step
				if strings.Contains(m.renderHelp(size.w, size.h), truncHead(last, min(size.w-4, 120)-4)) {
					seen = true
					break
				}
			}
			if !seen {
				t.Errorf("%dx%d demo=%v: the last help line is unreachable in %d scroll steps",
					size.w, size.h, demo, m.helpMaxScroll())
			}
		}
	}
}

func TestHelpScrollKeysDoNotCloseTheOverlay(t *testing.T) {
	m := newTestModel(t, 120, 30, 8)
	m.showHelp = true
	for _, key := range []string{"down", "j", "pgdown", "G"} {
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if !m.showHelp {
		t.Fatal("a scroll key closed the help overlay")
	}
	if m.helpMaxScroll() > 0 && m.helpScroll == 0 {
		t.Error("the overlay did not scroll")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.showHelp || m.helpScroll != 0 {
		t.Errorf("esc left showHelp=%v helpScroll=%d", m.showHelp, m.helpScroll)
	}
}

// stubDemo turns on the demo-only rows of the help without a simulated cluster.
type stubDemo struct{}

func (stubDemo) ScaleUp(int) {}
func (stubDemo) DrainOne()   {}
func (stubDemo) Churn()      {}

// The complaint this fixes, in two parts: two-finger scroll zoomed *and* the
// grid rearranged underneath, so the card you aimed at was not the card you got.
// The column count is now frozen while zoomed, which is what lets a zoom keep a
// specific card exactly where it is.

// The headline property. Point at a card, keep flicking, and that card stays
// under the pointer the whole way — not merely selected, but physically there.
func TestZoomKeepsTheCardUnderThePointer(t *testing.T) {
	for _, idx := range []int{0, 7, 13} {
		m := newTestModel(t, 160, 44, 60)
		x, y := cardCentre(m, idx)
		want := m.vis[idx].node.Name

		for notch := 0; notch < 60 && m.zoom < zoomMax; notch++ {
			m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))

			at, ok := m.hitTest(x, y)
			if !ok {
				t.Fatalf("card %d, notch %d: the pointer is no longer on any card", idx, notch)
			}
			if got := m.vis[at].node.Name; got != want {
				t.Fatalf("card %d, notch %d: %q is under the pointer now, want %q",
					idx, notch, got, want)
			}
			if m.cursorName != want {
				t.Fatalf("card %d, notch %d: the selection moved to %q", idx, notch, m.cursorName)
			}
		}
	}
}

// Zooming out keeps it under the pointer too, all the way to the far end.
func TestZoomingOutKeepsTheCardUnderThePointer(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	x, y := cardCentre(m, 9)
	want := m.vis[9].node.Name

	for notch := 0; notch < 60 && m.zoom > zoomMin; notch++ {
		m.handleMouse(wheel(tea.MouseButtonWheelDown, x, y))
		at, ok := m.hitTest(x, y)
		if !ok {
			continue // zoomed far enough out that the pointer is in a gutter
		}
		if got := m.vis[at].node.Name; got != want {
			t.Fatalf("notch %d: %q is under the pointer now, want %q", notch, got, want)
		}
	}
}

// No card may change its place in the grid because of a zoom. This is the
// "cards jumping around" complaint, stated as a property: (row, column) is a
// function of the list and the column count, and zoom does not touch either.
func TestZoomNeverRearrangesTheCards(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	cols := m.lay.cols
	places := make(map[string][2]int, len(m.vis))
	for i, v := range m.vis {
		places[v.node.Name] = [2]int{i / cols, i % cols}
	}

	x, y := cardCentre(m, 8)
	for notch := 0; notch < 40; notch++ {
		if notch == 20 { // and back out again
			x, y = cardCentre(m, m.cursor)
		}
		dir := tea.MouseButtonWheelUp
		if notch >= 20 {
			dir = tea.MouseButtonWheelDown
		}
		m.handleMouse(wheel(dir, x, y))

		if m.lay.cols != cols {
			t.Fatalf("notch %d: the grid re-columned from %d to %d", notch, cols, m.lay.cols)
		}
		for i, v := range m.vis {
			if got := ([2]int{i / m.lay.cols, i % m.lay.cols}); got != places[v.node.Name] {
				t.Fatalf("notch %d: %s moved from %v to %v", notch, v.node.Name,
					places[v.node.Name], got)
			}
		}
	}
}

func TestWheelZoomDoesNotScrollTheGridAround(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	x, y := cardCentre(m, 6)

	m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))
	anchor := m.cursorName
	if anchor == "" {
		t.Fatal("the first notch did not anchor on a node")
	}

	for notch := 1; notch < 24; notch++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))

		if m.cursorName != anchor {
			t.Fatalf("notch %d: the anchor moved from %q to %q mid-flick",
				notch, anchor, m.cursorName)
		}
		on := false
		for _, idx := range visibleCards(m) {
			if idx == m.cursor {
				on = true
			}
		}
		if !on {
			t.Fatalf("notch %d: the anchor panned off screen", notch)
		}
	}
}

// A separate flick, pointing somewhere else, should anchor somewhere else.
func TestANewFlickAnchorsAgain(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	firstX, firstY := cardCentre(m, 0)
	secondX, secondY := cardCentre(m, 1)

	want, ok := m.hitTest(secondX, secondY)
	if !ok {
		t.Fatalf("no card at %d,%d", secondX, secondY)
	}

	m.handleMouse(wheel(tea.MouseButtonWheelUp, firstX, firstY))
	m.wheelAt = time.Now().Add(-time.Second) // the flick ended
	m.handleMouse(wheel(tea.MouseButtonWheelUp, secondX, secondY))

	if m.cursor != want {
		t.Errorf("the second flick anchored on %d, want the card under it, %d", m.cursor, want)
	}
}

// Keyboard zoom has no pointer, so it holds the selected card's own corner
// still: the card grows in place instead of sliding across the screen.
func TestKeyboardZoomHoldsTheSelectedCardInPlace(t *testing.T) {
	for _, n := range []int{60, 120} {
		for _, cur := range []int{20, 50, 90} {
			if cur >= n {
				continue
			}
			for _, dir := range []int{1, -1} {
				m := newTestModel(t, 200, 60, n)
				m.setCursor(cur)

				x0, y0 := cardXY(m, m.cursor)
				free := panIsFree(m)
				m.zoomBy(dir * keyZoomStep)
				x1, y1 := cardXY(m, m.cursor)

				// Exact, wherever the pan is free to be exact. At the edges of the
				// canvas it is not: the viewport cannot hang past the last row to
				// hold a card still, so the card moves and empty space does not.
				if free && panIsFree(m) && (x1 != x0 || y1 != y0) {
					t.Errorf("n=%d cur=%d dir=%+d: the card moved from (%d,%d) to (%d,%d)",
						n, cur, dir, x0, y0, x1, y1)
				}
				on := false
				for _, idx := range visibleCards(m) {
					if idx == m.cursor {
						on = true
					}
				}
				if !on {
					t.Errorf("n=%d cur=%d dir=%+d: the card left the screen", n, cur, dir)
				}
			}
		}
	}
}

// The point of anchoring: keep flicking and you land on the card you aimed at,
// whatever the grid did on the way.
func TestZoomingAllTheWayInLandsOnTheCardUnderThePointer(t *testing.T) {
	for _, idx := range []int{0, 7, 13} {
		m := newTestModel(t, 160, 44, 60)
		x, y := cardCentre(m, idx)
		name := m.vis[idx].node.Name

		for i := 0; i < 200 && m.zoom < zoomMax; i++ {
			// Several separate flicks with pauses between them, which is how a
			// long zoom is actually performed.
			if i%(2*wheelNotchesPerZoom) == 0 {
				m.wheelAt = time.Now().Add(-time.Hour)
			}
			m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))
		}
		if m.cursorName != name {
			t.Errorf("card %d: zoomed all the way in on %q, want %q", idx, m.cursorName, name)
		}
		// The anchored card is whole on screen and owns most of it. Slivers of a
		// neighbour may remain: the canvas pans by cells, not by whole cards.
		cx, cy := cardXY(m, m.cursor)
		x, y = cx, cy-gridTop(m)
		if x < 0 || y < 0 || x+m.lay.boxW > m.w || y+m.lay.boxH > m.gridHeight() {
			t.Errorf("card %d: fully zoomed in, the card is clipped at (%d,%d) %dx%d in %dx%d",
				idx, x, y, m.lay.boxW, m.lay.boxH, m.w, m.gridHeight())
		}
		if area := float64(m.lay.boxW*m.lay.boxH) / float64(m.w*m.gridHeight()); area < 0.75 {
			t.Errorf("card %d: fully zoomed in it has %.0f%% of the screen", idx, area*100)
		}
	}
}

// composeRow is the clipping renderer: cards are drawn at whole-cell offsets
// that the hit test inverts. If the two disagree by even one column, pointing at
// a card selects its neighbour — so the alignment is asserted directly, against
// arithmetic done a different way.
func TestComposeRowPlacesCardsExactly(t *testing.T) {
	l := newLayout(4, 10, 3, 12) // 4 columns of 10-wide cards, pitch 12
	cells := [][]string{
		{strings.Repeat("A", 10)}, {strings.Repeat("B", 10)},
		{strings.Repeat("C", 10)}, {strings.Repeat("D", 10)},
	}
	for _, panX := range []int{0, -3, 5, 12, 17, 30, -40} {
		want := []rune(strings.Repeat(" ", 30))
		for col, letter := range []rune{'A', 'B', 'C', 'D'} {
			for i := 0; i < l.boxW; i++ {
				if x := col*l.stepX + i - panX; x >= 0 && x < 30 {
					want[x] = letter
				}
			}
		}
		if got := composeRow(cells, l, 30, panX, 0); got != string(want) {
			t.Errorf("panX=%d:\n got %q\nwant %q", panX, got, string(want))
		}
	}
}

// Every point on the grid hits the card the layout says is there. This is the
// hit test against the geometry; TestComposeRowPlacesCardsExactly is the
// geometry against the pixels.
func TestHitTestAgreesWithTheLayoutEverywhere(t *testing.T) {
	for _, z := range []int{0, 3, 6, -3} {
		m := newTestModel(t, 160, 44, 40)
		m.setZoom(z)
		for y := gridTop(m); y < gridTop(m)+m.gridHeight(); y++ {
			for x := 0; x < m.w; x++ {
				idx, ok := m.hitTest(x, y)
				if !ok {
					continue
				}
				x0, y0 := cardXY(m, idx)
				if x < x0 || x >= x0+m.lay.boxW || y < y0 || y >= y0+m.lay.boxH {
					t.Fatalf("zoom=%d: (%d,%d) hit card %d, which is at (%d,%d) %dx%d",
						z, x, y, idx, x0, y0, m.lay.boxW, m.lay.boxH)
				}
			}
		}
	}
}

// A coordinate the grid does not contain must not be treated as an aim. Mapping
// it to the nearest card means mapping it to an edge, and an edge in both axes
// is a corner that every further notch drives deeper into.
func TestOffGridPointerZoomsAboutTheCentreInstead(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	m.setCursor(25)
	want := m.cursorName

	f := m.layoutFrame()
	for _, p := range [][2]int{{80, 0}, {80, f.header - 1}, {80, 43}, {80, 999}, {80, -5}} {
		if _, ok := m.nearestCard(p[0], p[1]); ok {
			t.Errorf("(%d,%d) is off the grid but was accepted as an aim", p[0], p[1])
		}
	}

	for i := 0; i < 4*wheelNotchesPerZoom; i++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, 80, 0)) // in the header
	}
	if m.cursorName != want {
		t.Errorf("an off-grid wheel moved the selection to %q, want %q", m.cursorName, want)
	}
	if m.zoom == 0 {
		t.Error("an off-grid wheel did not zoom at all")
	}
	on := false
	for _, idx := range visibleCards(m) {
		if idx == m.cursor {
			on = true
		}
	}
	if !on {
		t.Error("zooming about the centre lost the selected card")
	}
}

// Gutters and the margins beside a zoomed-out canvas are still aimed at
// something: refusing to re-anchor there left the zoom pinned to a card the
// pointer had long since left.
func TestGuttersAndMarginsStillAim(t *testing.T) {
	m := newTestModel(t, 160, 44, 40)
	m.setZoom(-4)
	if m.lay.gridW >= m.w {
		t.Skip("no margin at this size")
	}
	f := m.layoutFrame()
	margin := 1 // hard against the left edge, outside the canvas
	y := f.header + f.legend + m.lay.boxH/2

	if _, ok := m.hitTest(margin, y); ok {
		t.Fatal("the margin is inside the canvas; the test proves nothing")
	}
	idx, ok := m.nearestCard(margin, y)
	if !ok {
		t.Fatal("a pointer in the margin aimed at nothing")
	}
	if idx%m.lay.cols != 0 {
		t.Errorf("the left margin aimed at column %d, want the leftmost", idx%m.lay.cols)
	}
}

// The zoom anchor is not the selection, and this is why: a terminal in
// alternate-scroll mode sends arrow keys alongside the wheel, so every notch
// would also step the selection down a row. A zoom that followed the selection
// would march to the bottom of the grid while the user held the pointer still —
// and land on the bottom-left card no matter where they aimed.
func TestZoomIgnoresSelectionMovement(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	x, y := cardCentre(m, 8)
	want := m.vis[8].node.Name

	for notch := 0; notch < 6*wheelNotchesPerZoom; notch++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))
		// Whatever else the terminal is sending, interleaved. Arrow keys move the
		// selection and scroll it into view — that is what arrow keys are for, and
		// the viewport moving is not something the zoom can veto. What it can do
		// is refuse to be aimed by them.
		m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
		m.handleKey(tea.KeyMsg{Type: tea.KeyRight})

		if m.anchorName != want {
			t.Fatalf("notch %d: arrow keys dragged the zoom anchor to %q, want %q",
				notch, m.anchorName, want)
		}
	}
	// And the card the zoom has been growing all along is the one aimed at, not
	// wherever the selection wandered off to.
	if m.vis[m.anchorIndex()].node.Name != want {
		t.Fatalf("the zoom ended on %q, want %q", m.vis[m.anchorIndex()].node.Name, want)
	}
	// And once the interference stops, the next level re-seats it under the
	// pointer — a notch that applies no level moves nothing, by design.
	for i := 0; i < wheelNotchesPerZoom; i++ {
		m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y))
	}
	at, ok := m.hitTest(x, y)
	if !ok || m.vis[at].node.Name != want {
		got := "nothing"
		if ok {
			got = m.vis[at].node.Name
		}
		t.Errorf("after the interference stopped, %q is under the pointer, want %q", got, want)
	}
}

// Moving the selection with the keyboard and then zooming with the keyboard
// zooms about the selection: the keyboard has no pointer, so the selection is
// the only aim it has.
func TestKeyboardZoomFollowsTheSelection(t *testing.T) {
	m := newTestModel(t, 160, 44, 60)
	x, y := cardCentre(m, 8)
	m.handleMouse(wheel(tea.MouseButtonWheelUp, x, y)) // aim the mouse somewhere

	m.setCursor(30)
	want := m.cursorName
	m.zoomBy(keyZoomStep)
	if m.cursorName != want {
		t.Errorf("keyboard zoom moved the selection to %q, want %q", m.cursorName, want)
	}
	on := false
	for _, idx := range visibleCards(m) {
		if idx == m.cursor {
			on = true
		}
	}
	if !on {
		t.Error("keyboard zoom did not keep the selected card on screen")
	}
}
