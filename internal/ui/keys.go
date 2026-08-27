package ui

import (
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// handleKey routes a keypress. The command bar, when open, swallows everything
// except Esc — a half-typed command must never be interpreted as a shortcut.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.bar.active {
		return m.handleBarKey(msg)
	}
	// Playback controls are genuinely global: a presenter must be able to pause
	// or catch up without first closing the detail pane or help overlay.
	switch msg.String() {
	case "p":
		m.togglePause()
		return nil
	case "[":
		cmd, moved := m.rewindPlayback(keyRewindStep)
		if moved == 0 {
			m.notify("no rewind history yet", true)
		} else {
			m.showSeekOverlay(moved)
			m.notify(fmt.Sprintf("rewound %s · %s", moved.Round(time.Millisecond),
				playbackStatus(m.playback, time.Now())), false)
		}
		return cmd
	case "r":
		return m.goRealtime("")
	}
	if m.detail != nil {
		return m.handleDetailKey(msg)
	}
	if m.showHelp {
		// The scroll keys scroll; any other key closes, which is what everyone
		// tries first.
		switch msg.String() {
		case "up", "k":
			m.helpScroll = max(0, m.helpScroll-1)
		case "down", "j":
			m.helpScroll = clampInt(m.helpScroll+1, 0, m.helpMaxScroll())
		case "pgup", "ctrl+b":
			m.helpScroll = max(0, m.helpScroll-m.helpViewRows())
		case "pgdown", "ctrl+f", " ":
			m.helpScroll = clampInt(m.helpScroll+m.helpViewRows(), 0, m.helpMaxScroll())
		case "g", "home":
			m.helpScroll = 0
		case "G", "end":
			m.helpScroll = m.helpMaxScroll()
		default:
			m.showHelp, m.helpScroll = false, 0
		}
		return nil
	}
	return m.handleGlobalKey(msg)
}

func (m *Model) handleBarKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.bar.close()
		m.derive()
		return nil

	case tea.KeyTab:
		// Tab always completes and never runs, so it is safe to lean on.
		m.bar.accept()
		m.bar.refresh(m)
		return nil

	case tea.KeyUp, tea.KeyCtrlP:
		m.bar.move(-1)
		return nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.bar.move(1)
		return nil

	case tea.KeyBackspace:
		m.bar.backspace()
		m.bar.refresh(m)
		return nil

	case tea.KeyCtrlU:
		m.bar.input = ""
		m.bar.refresh(m)
		return nil

	case tea.KeyEnter:
		line := m.bar.input
		if m.bar.hlActive {
			// The user arrowed into the list, so Enter means "take that one".
			if m.bar.accept() {
				line = m.bar.input
			} else {
				m.bar.refresh(m)
				return nil
			}
		}
		return m.execute(line)

	case tea.KeySpace:
		m.bar.insert(" ")
		m.bar.refresh(m)
		return nil

	case tea.KeyRunes:
		m.bar.insert(string(msg.Runes))
		m.bar.refresh(m)
		return nil
	}
	return nil
}

// execute runs a command line and decides whether the bar stays open. A command
// that needs an argument turns into a picker instead of an error, which is what
// makes ":nodepool" followed by Enter feel right.
func (m *Model) execute(line string) tea.Cmd {
	m.pendingCmd = nil
	msg, err := m.Run(line)
	switch {
	case errors.Is(err, errNeedsArg):
		if cmd, ok := lookup(trimCommandName(line)); ok {
			m.bar.pick(cmd)
			m.bar.refresh(m)
			return nil
		}
		m.bar.close()
	case err != nil:
		m.notify(err.Error(), true)
		m.bar.close()
	default:
		if msg != "" {
			m.notify(msg, false)
		}
		m.bar.close()
	}
	m.derive()
	cmd := m.pendingCmd
	m.pendingCmd = nil
	return cmd
}

func trimCommandName(line string) string {
	name, _, _ := cutSpace(line)
	return name
}

func cutSpace(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func (m *Model) handleGlobalKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true

	case ":":
		m.bar.open("")
		m.bar.refresh(m)
		m.derive()

	case "/":
		// Prefilled node-name filter, the k9s reflex.
		if cmd, ok := lookup("node"); ok {
			m.bar.pick(cmd)
			m.bar.refresh(m)
			m.derive()
		}

	case "enter":
		// The one drill-down: everything the grid cannot draw about the selected
		// node, and its events in the order they happened.
		return m.openDetail()

	case "?":
		m.showHelp = true

	case "\\":
		m.filter.Clear()
		m.notify("filters cleared", false)
		m.derive()

	case "v":
		m.setMode(Mode((int(m.mode) + 1) % len(modeNames)))
		m.notify("mode: "+m.mode.String(), false)

	case "d":
		if m.mode == ModeDense {
			m.setMode(ModePods)
		} else {
			m.setMode(ModeDense)
		}
		m.notify("mode: "+m.mode.String(), false)

	case "s":
		m.sortKey = SortKey((int(m.sortKey) + 1) % len(sortNames))
		m.notify("sort: "+m.sortKey.String(), false)
		m.derive()

	case "S":
		m.sortDesc = !m.sortDesc
		m.notify("sort: "+m.sortKey.String()+dirArrow(m.sortDesc), false)
		m.derive()

	case "l":
		m.showLegend = !m.showLegend
		m.derive()

	// Zoom. z/Z mirrors s/S — lower case steps, upper case is its opposite — and
	// leaves +/- to the demo controls, which are muscle memory during a talk.
	case "z":
		m.zoomBy(keyZoomStep)
	case "Z":
		m.zoomBy(-keyZoomStep)
	case "0":
		if m.setZoom(0) {
			m.notify("zoom: fit", false)
		}

	case "left", "h":
		m.moveCursor(-1)
	case "right", "n":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-max(1, m.lay.cols))
	case "down", "j":
		m.moveCursor(max(1, m.lay.cols))

	case "pgup", "ctrl+b":
		m.moveCursor(-m.visibleRows() * max(1, m.lay.cols))
	case "pgdown", "ctrl+f", " ":
		m.moveCursor(m.visibleRows() * max(1, m.lay.cols))

	case "g", "home":
		m.setCursor(0)
	case "G", "end":
		m.setCursor(len(m.vis) - 1)

	// Demo controls. Deliberately single keys: during a talk you should be able
	// to trigger a scale-up without looking at the keyboard.
	case "+", "=":
		if m.demo != nil {
			m.demo.ScaleUp(1)
			m.notify("provisioning a node", false)
		}
	case "-", "_":
		if m.demo != nil {
			m.demo.DrainOne()
			m.notify("draining a node", false)
		}
	case "x":
		if m.demo != nil {
			m.demo.Churn()
			m.notify("churning pods", false)
		}
	case "b":
		if m.demo != nil {
			m.demo.Burst(8)
			m.notify("submitting 8 pods", false)
		}
	}
	return nil
}

func dirArrow(desc bool) string {
	if desc {
		return "↓"
	}
	return "↑"
}

func (m *Model) moveCursor(delta int) { m.setCursor(m.cursor + delta) }

func (m *Model) setCursor(idx int) {
	if len(m.vis) == 0 {
		return
	}
	m.cursor = clampInt(idx, 0, len(m.vis)-1)
	m.cursorName = m.vis[m.cursor].node.Name
	m.ensureVisible()
}

// wheelScrollRows is how far one wheel notch moves the grid. A trackpad emits a
// flurry of notches per flick, so one row a notch is already fast; anything more
// overshoots a two-row grid entirely.
const wheelScrollRows = 1

// handleMouse supports click-to-select, wheel zoom and modifier+wheel scrolling.
// Pointing at a node with the mouse is the fastest way to answer "what is that
// one?" mid-demo.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	// MouseMsg is a defined type over MouseEvent and so carries none of its
	// methods; the conversion is how you get at IsWheel.
	isWheel := tea.MouseEvent(msg).IsWheel()
	if debugMouse {
		defer m.recordMouse(msg)
	}

	// The detail pane replaces the grid, so the wheel scrolls the text. Zooming a
	// grid that is not on screen is the one thing it must not do.
	if m.detail != nil {
		if isWheel {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.scrollDetail(-detailWheelRows)
			case tea.MouseButtonWheelDown:
				m.scrollDetail(detailWheelRows)
			}
		}
		return nil
	}

	// The help overlay covers the grid, so while it is up the wheel belongs to
	// it. Scrolling something you cannot see is the worse of the two options.
	if m.showHelp {
		if isWheel {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.helpScroll = max(0, m.helpScroll-1)
			case tea.MouseButtonWheelDown:
				m.helpScroll = clampInt(m.helpScroll+1, 0, m.helpMaxScroll())
			}
		}
		return nil
	}

	if !isWheel {
		// Press or drag, both by button rather than by the deprecated Type: with
		// cell motion enabled a held left button arrives as motion, and dragging
		// the selection across the grid is worth having.
		if msg.Button == tea.MouseButtonLeft &&
			(msg.Action == tea.MouseActionPress || msg.Action == tea.MouseActionMotion) {
			if idx, ok := m.hitTest(msg.X, msg.Y); ok {
				// Clicking is aiming too, so it moves the zoom anchor as well as
				// the selection.
				m.setAnchor(idx)
				m.anchorX, m.anchorY = msg.X, msg.Y
			}
		}
		return nil
	}

	// A bare two-finger scroll zooms. A pinch cannot reach a terminal program —
	// macOS delivers the magnify gesture to the emulator, which has no escape
	// sequence to forward it with — so the plain wheel is the closest thing to
	// the gesture people actually reach for on a trackpad. Holding any modifier
	// scrolls the grid instead; all three are accepted because which one survives
	// depends on the emulator, several of which eat ctrl+wheel for font sizing.
	//
	// Dense mode is the exception: it has no card geometry to scale, so there the
	// wheel keeps scrolling rather than rejecting every notch of a flick.
	scrolling := msg.Ctrl || msg.Alt || msg.Shift || m.mode == ModeDense

	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if scrolling {
			m.scrollBy(-wheelScrollRows)
		} else {
			m.zoomByWheel(1, msg.X, msg.Y)
		}
	case tea.MouseButtonWheelDown:
		if scrolling {
			m.scrollBy(wheelScrollRows)
		} else {
			m.zoomByWheel(-1, msg.X, msg.Y)
		}
	case tea.MouseButtonWheelLeft:
		m.panByWheel(-1)
	case tea.MouseButtonWheelRight:
		m.panByWheel(1)
	}
	return nil
}

// panNotchesPerCard is how many horizontal notches move the view one card.
//
// Horizontal wheel events are not a considered gesture on a trackpad: a plain
// two-finger *vertical* scroll emits them constantly — in one real session,
// ninety-nine horizontal events against a hundred vertical ones. Anything they
// drive has to be inert enough to survive that, which is why they are ignored
// during a zoom and accumulated even when they are not.
const panNotchesPerCard = 2

// panByWheel pans the view sideways, which is what a zoomed-in canvas needs and
// what the horizontal wheel is for.
//
// It used to move the *selection* by one card a notch. That is what made zooming
// unusable: every flick dragged the selection along the list, ensureVisible
// panned the grid to chase it, and after a dozen notches the selection had run
// to the end of the list — the bottom-left card in a two-column grid — with the
// view following it there. The gesture looked like "zoom always ends up in the
// corner", and the corner was wherever the list ran out.
func (m *Model) panByWheel(dir int) {
	// A vertical gesture owns the wheel while it lasts. The horizontal component
	// of a vertical flick is noise, and must not move anything.
	if time.Since(m.wheelAt) < wheelGestureGap {
		return
	}
	if m.panAccum*dir < 0 {
		m.panAccum = 0
	}
	m.panAccum += dir
	if abs(m.panAccum) < panNotchesPerCard {
		return
	}
	m.panAccum = 0
	m.panX += dir * m.lay.stepX
	m.clampPan()
}

// wheelNotchesPerZoom is how many wheel notches make one zoom level.
//
// A trackpad flick emits a burst of notches — a dozen is ordinary — so this and
// zoomStep together set how far one flick travels. At three notches per level
// and 1.4× a level, a flick was a four-fold change of scale and the grid lurched;
// at four notches and 1.18×, the same flick is about 1.6× and reads as a zoom.
const wheelNotchesPerZoom = 4

// wheelGestureGap is the silence that separates one flick from the next. Notches
// closer together than this are one gesture, and their notch count is banked
// together; it says nothing about which card the zoom is aimed at.
const wheelGestureGap = 300 * time.Millisecond

// zoomByWheel accumulates wheel notches into zoom levels, zooming about the card
// under the pointer — which is what makes it feel like a map instead of a list.
//
// The anchor changes when the *pointer* moves, and at no other time. Zooming
// reflows the grid, which is what zooming is, so the card beneath a stationary
// pointer changes as a side effect of the gesture; re-reading the pointer after
// any reflow means the zoom wanders off the card that was aimed at. Time was
// tried as the guard first — ignore the pointer for a second or so after a zoom
// — and it only made the wandering intermittent, because a pause longer than the
// cooldown in the middle of a long zoom would re-aim at whatever had drifted
// under the pointer by then. Position is the honest test: the pointer has not
// moved, so the target has not changed.
//
// Mouse motion is only reported while a button is held, so a wheel event is the
// only place the pointer's position is learned — which is exactly where it is
// needed.
func (m *Model) zoomByWheel(dir, x, y int) {
	now := time.Now()
	if now.Sub(m.wheelAt) > wheelGestureGap {
		m.wheelAccum = 0
	}
	m.wheelAt = now

	if x != m.anchorX || y != m.anchorY {
		// nearestCard, not hitTest: a pointer in a gutter, or in the margin beside
		// a zoomed-out canvas, is still aimed at something, and refusing to
		// re-anchor there left the zoom pinned to a card the pointer had long
		// since left — which is how it ended up walking into a corner.
		//
		// The selection is set directly rather than through setCursor because
		// setCursor scrolls the selection into view. Panning the grid before
		// working out what the zoom should hold still is how the grid gets out
		// from under the pointer in the first place.
		if idx, ok := m.nearestCard(x, y); ok {
			m.setAnchor(idx)
		}
		m.anchorX, m.anchorY = x, y
	}

	// A direction change starts a fresh count, so reversing a flick responds on
	// the next few notches instead of first paying back the ones already banked.
	if m.wheelAccum*dir < 0 {
		m.wheelAccum = 0
	}
	// At the end of the range there is nothing to accumulate toward, and saying
	// so once per notch would bury the status line under a single flick.
	if (dir > 0 && m.zoom >= zoomMax) || (dir < 0 && m.zoom <= zoomMin) {
		m.wheelAccum = 0
		return
	}

	m.wheelAccum += dir
	if abs(m.wheelAccum) < wheelNotchesPerZoom {
		return
	}
	m.wheelAccum = 0
	// Zoom about the pointer when it is somewhere on the grid, and about the
	// middle of the screen when it is not.
	//
	// The fallback matters more than it looks. If a terminal ever reports a
	// coordinate the grid does not contain, anchoring on the nearest card means
	// anchoring on an edge — and an edge in both axes is a corner, which every
	// further notch then drives deeper into. Zooming about the centre instead
	// keeps whatever is selected, grows it in place, and goes nowhere.
	if _, ok := m.nearestCard(x, y); ok {
		m.zoomAtPointer(dir, x, y)
	} else {
		m.zoomAtCentre(dir)
	}
}

// scrollBy moves the viewport by whole card rows and brings the selection with
// it.
//
// Dragging the cursor along is not a nicety: derive() re-runs ensureVisible on
// every snapshot, so a viewport that has been scrolled away from the selected
// node is yanked back to it within 100ms. Scrolling and the selection have to
// agree, or the fastest-updating one wins and the wheel looks broken — which is
// exactly how it looked.
func (m *Model) scrollBy(rows int) {
	if len(m.vis) == 0 {
		return
	}
	m.panY += rows * m.lay.stepY
	m.clampPan()

	// Keep the cursor where it is if it is still on screen; otherwise pull it to
	// the nearest visible row, holding its column so a vertical scroll does not
	// drift sideways.
	first := m.scrollRow()
	last := first
	if h := m.gridHeight(); m.lay.stepY > 0 {
		last = max(first, (m.panY+h-1)/m.lay.stepY)
		if m.mode != ModeDense {
			// Only rows with the whole card on screen count as somewhere to put
			// the selection.
			if (last+1)*m.lay.stepY-gapY > m.panY+h {
				last = max(first, last-1)
			}
		}
	}
	cols := max(1, m.lay.cols)
	row := clampInt(m.cursor/cols, first, last)
	idx := clampInt(row*cols+m.cursor%cols, 0, len(m.vis)-1)
	m.cursor = idx
	m.cursorName = m.vis[idx].node.Name
}

// recordMouse builds the KNV_DEBUG_MOUSE readout: what arrived, what the grid
// made of it, and where the view ended up.
func (m *Model) recordMouse(msg tea.MouseMsg) {
	f := m.layoutFrame()
	hit := "off-grid"
	if idx, ok := m.hitTest(msg.X, msg.Y); ok {
		hit = fmt.Sprintf("%d %s", idx, m.vis[idx].node.Name)
	} else if idx, ok := m.nearestCard(msg.X, msg.Y); ok {
		hit = fmt.Sprintf("~%d %s", idx, m.vis[idx].node.Name)
	}
	m.lastMouse = fmt.Sprintf("%s @%d,%d grid@%d+%d hit[%s] sel[%s] pan%d,%d box%dx%d cols%d",
		tea.MouseEvent(msg).String(), msg.X, msg.Y, f.header+f.legend, f.grid,
		hit, m.cursorName, m.panX, m.panY, m.lay.boxW, m.lay.boxH, m.lay.cols)
}

// nearestCard is hitTest for aiming rather than for clicking: it never says
// "nowhere". Gutters belong to the card they follow, and a point outside the
// canvas belongs to the nearest edge card.
//
// Zooming needs this and clicking does not. A click in the gap between two cards
// meaning nothing is correct; a *zoom* in that gap meaning nothing leaves the
// gesture aimed at wherever it was last, which is worse than a sensible guess by
// a wide margin.
func (m *Model) nearestCard(x, y int) (int, bool) {
	if len(m.vis) == 0 {
		return 0, false
	}
	f := m.layoutFrame()
	if f.grid <= 0 {
		return 0, false
	}
	// Outside the grid rows is not an aim at all. Clamping it into the grid the
	// way gutters are clamped would map every stray coordinate to an edge card —
	// and a y below the grid to the *last* row, which is a corner. Say no, and
	// let the caller fall back to something that cannot walk anywhere.
	rel := y - f.header - f.legend
	if rel < 0 || rel >= f.grid {
		return 0, false
	}
	if m.mode == ModeDense {
		return clampInt(m.scrollRow()+rel-1, 0, len(m.vis)-1), true
	}
	vx := clampInt(x+m.panX, 0, max(0, m.lay.gridW-1))
	vy := clampInt(rel+m.panY, 0, max(0, m.lay.gridH-1))
	col := clampInt(vx/m.lay.stepX, 0, m.lay.cols-1)
	row := clampInt(vy/m.lay.stepY, 0, max(0, m.lay.pages-1))
	return clampInt(row*m.lay.cols+col, 0, len(m.vis)-1), true
}

// hitTest maps screen coordinates to an index in the visible list, or reports
// that the point is not on a card.
//
// The chrome above the grid is measured, not assumed: layoutFrame drops the
// legend and then the header as the terminal shrinks, and a hit test that still
// counts their rows puts every click a card too high.
func (m *Model) hitTest(x, y int) (int, bool) {
	f := m.layoutFrame()
	top := f.header + f.legend
	if y < top || y >= top+f.grid {
		return 0, false
	}
	rel := y - top
	if m.mode == ModeDense {
		idx := m.scrollRow() + rel - 1 // row 0 is the table header
		if idx < 0 || idx >= len(m.vis) {
			return 0, false
		}
		return idx, true
	}

	// Canvas coordinates: undo the pan, then divide by the card pitch. The
	// gutters belong to no card, which matters for zoom — an anchor picked from
	// the gap between two cards is a guess, and keeping the previous one is
	// better than guessing.
	vx, vy := x+m.panX, rel+m.panY
	if vx < 0 || vy < 0 {
		return 0, false
	}
	col, row := vx/m.lay.stepX, vy/m.lay.stepY
	if vx-col*m.lay.stepX >= m.lay.boxW || vy-row*m.lay.stepY >= m.lay.boxH {
		return 0, false
	}
	if col >= m.lay.cols {
		return 0, false
	}
	idx := row*m.lay.cols + col
	if idx < 0 || idx >= len(m.vis) {
		return 0, false
	}
	return idx, true
}
