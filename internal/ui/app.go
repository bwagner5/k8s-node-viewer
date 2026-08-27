package ui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/anim"
	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// messageTTL is how long a status message stays up.
const messageTTL = 4 * time.Second

// seekOverlayTTL is intentionally brief: long enough to register like a video
// player's seek feedback, short enough not to cover the cluster being reviewed.
const seekOverlayTTL = 1500 * time.Millisecond

// idleFPS is the tick rate when nothing is animating. Dropping to it keeps an
// idle viewer off the CPU while still refreshing ages and pulses.
const idleFPS = 4

// One keypress should move far enough to recover a missed transition without
// making a short lifecycle jump all the way out of view.
const keyRewindStep = 5 * time.Second

// Demo is the optional interactive control surface. Only the simulated source
// implements it; against a real cluster this is nil and the demo commands are
// hidden, because the viewer must never mutate a live cluster.
type Demo interface {
	ScaleUp(n int)
	DrainOne()
	Churn()
	Burst(n int)
}

// Config is everything the UI needs at construction.
type Config struct {
	Snapshots <-chan *model.Snapshot
	Demo      Demo
	FPS       int
	// PlaybackSpeed is the initial cluster timeline rate. One starts live;
	// values below one start buffered playback, and zero starts paused.
	PlaybackSpeed float64
	PlaybackSet   bool
	// HistoryDuration and HistoryMemory bound both delayed snapshots waiting to
	// be replayed and the short rolling rewind window. Zero values select
	// presentation-friendly defaults.
	HistoryDuration time.Duration
	HistoryMemory   int64
	// Replay selects finite archival playback. Its complete source timeline is
	// retained without the live rolling-window limits.
	Replay bool
	// ReplayEnd includes idle time after the last changed snapshot in a cleanly
	// closed recording.
	ReplayEnd time.Time
	// CaptureDetails makes the UI sample every node even in realtime. Recording
	// uses it so the session contains historical events whether or not a detail
	// pane happened to be open.
	CaptureDetails bool
	// Recording provides runtime start/stop control. It is nil for replay.
	Recording     RecordingController
	DetailHistory []RecordedDetail
	Mode          Mode
	Sort          SortKey
	Filter        Filter
	Legend        bool
	// Describe backs the node detail pane. Nil degrades the pane to snapshot
	// data with a note that events need a source, rather than disabling it.
	Describe Describer
}

// RecordedDetail is a node-detail sample loaded alongside archived snapshots.
type RecordedDetail struct {
	At         time.Time
	Identity   string
	Name       string
	NodeClaim  string
	ProviderID string
	Detail     *model.NodeDetail
}

// Model is the bubbletea model.
//
// It owns three separable things, and keeping them separate is what makes this
// maintainable:
//   - the latest snapshot (facts, replaced wholesale, never mutated here)
//   - the animation registry (what the screen is doing)
//   - view state: mode, filters, sort, cursor (what you asked to see)
//
// Update only ever changes view state or swaps the snapshot. View is a pure
// function of the three.
type Model struct {
	cfg   Config
	snaps <-chan *model.Snapshot

	snap  *model.Snapshot
	reg   *anim.Registry
	fleet *anim.Track
	// pend smooths the pending meter. It borrows a Track's two value slots for
	// the two segments of one bar: CPU carries the whole backlog, Mem the
	// unschedulable part of it.
	pend *anim.Track

	mode       Mode
	zoom       int
	sortKey    SortKey
	sortDesc   bool
	filter     Filter
	showLegend bool
	showHelp   bool
	helpScroll int
	// detail is the node describe pane, nil when the grid is showing. It holds
	// every scrap of state the pane needs, so closing it restores the grid
	// exactly — there is nothing else to put back.
	detail                 *detailView
	describe               Describer
	playback               *playback
	detailHistory          map[string][]detailSample
	detailHistoryBytes     int64
	detailCaptureAt        time.Time
	detailCapturing        bool
	detailHistoryExhausted bool
	detailHistoryEpoch     uint64
	// pendingCmd carries a command produced inside a command-registry callback
	// back to execute, whose signature is the Bubble Tea integration boundary.
	pendingCmd tea.Cmd
	// Wheel-zoom gesture state: wheelAccum banks notches between zoom levels,
	// wheelAt separates one flick from the next, and anchorX/anchorY are where
	// the pointer was when the current anchor was chosen — the zoom re-aims when
	// and only when those change. See zoomByWheel.
	wheelAccum       int
	panAccum         int
	wheelAt          time.Time
	anchorX, anchorY int
	// anchorName is the node the wheel is zooming about. It is deliberately not
	// the selection: anything at all can move the selection — an arrow key, a
	// filter, a node arriving — and a zoom that follows the selection follows all
	// of that too, walking off the card the pointer is on.
	anchorName string
	// lastMouse is the most recent mouse event, kept only for the debug readout.
	lastMouse string
	demo      Demo
	recording RecordingController

	bar    cmdBar
	msg    string
	msgErr bool
	msgAt  time.Time
	// seekOverlay is the accumulated distance from consecutive [ presses while
	// the video-player-style feedback remains on screen.
	seekOverlay   time.Duration
	seekOverlayAt time.Time

	// cursorName tracks the selection by identity, so re-sorting or a scale
	// event does not move the highlight off the node you were pointing at.
	cursorName string
	cursor     int

	// panX/panY are where the viewport sits on the card canvas, in cells. They
	// go negative when the canvas is smaller than the window, which is how the
	// grid is centred.
	panX, panY int
	// zoomCols is the column count the canvas is frozen at while zoomed. Zoom
	// changes the card size and nothing else, so that cards keep their places;
	// returning to fit clears it and lays the grid out for the window again.
	zoomCols int

	w, h     int
	quitting bool

	vis  []visible
	lay  layout
	last time.Time
}

// New builds the model.
func New(cfg Config) *Model {
	if cfg.FPS <= 0 {
		cfg.FPS = 20
	}
	initialSpeed := cfg.PlaybackSpeed
	if !cfg.PlaybackSet {
		// Config's zero value preserves the historical default: live. Starting
		// paused is available through :speed 0 once the program is running.
		initialSpeed = 1
	}
	m := &Model{
		cfg:           cfg,
		snaps:         cfg.Snapshots,
		snap:          &model.Snapshot{},
		reg:           anim.NewRegistry(),
		fleet:         &anim.Track{},
		pend:          &anim.Track{},
		mode:          cfg.Mode,
		sortKey:       cfg.Sort,
		filter:        cfg.Filter,
		showLegend:    cfg.Legend,
		demo:          cfg.Demo,
		recording:     cfg.Recording,
		describe:      cfg.Describe,
		playback:      newPlaybackMode(initialSpeed, cfg.HistoryDuration, cfg.HistoryMemory, cfg.Replay),
		detailHistory: map[string][]detailSample{},
		last:          time.Now(),
	}
	m.playback.archiveEnd = cfg.ReplayEnd
	for _, loaded := range cfg.DetailHistory {
		if loaded.Detail == nil {
			continue
		}
		sample := detailSample{at: loaded.At, detail: loaded.Detail, bytes: estimateDetailBytes(loaded.Detail)}
		identities := []string{loaded.Identity, detailIdentity(loaded.Name, loaded.NodeClaim, loaded.ProviderID)}
		if loaded.ProviderID != "" {
			identities = append(identities, "provider:"+loaded.ProviderID)
		}
		if loaded.NodeClaim != "" {
			identities = append(identities, "claim:"+loaded.NodeClaim)
		}
		if loaded.Name != "" {
			identities = append(identities, "node:"+loaded.Name)
		}
		seen := map[string]bool{}
		for _, identity := range identities {
			if identity == "" || seen[identity] {
				continue
			}
			seen[identity] = true
			m.detailHistory[identity] = append(m.detailHistory[identity], sample)
		}
		// Aliases point at the same payload and do not multiply retained bytes.
		m.detailHistoryBytes += sample.bytes
	}
	for identity := range m.detailHistory {
		sort.SliceStable(m.detailHistory[identity], func(i, j int) bool {
			return m.detailHistory[identity][i].at.Before(m.detailHistory[identity][j].at)
		})
	}
	return m
}

// debugMouse turns on a live readout of what the terminal reports and what the
// viewer does with it, for diagnosing a gesture that behaves differently on a
// real terminal than it does in any test. The tests can drive every code path
// here but cannot produce the input, so when the two disagree this is the only
// way to see which is lying.
var debugMouse = os.Getenv("KNV_DEBUG_MOUSE") != ""

// traceFile is KNV_TRACE: a log of every input the model receives and what it
// did with it. A readout on the status line can only show what it was built to
// show — a wheel-only readout says nothing about a terminal that is also sending
// arrow keys, which is precisely the sort of thing worth ruling out.
var traceFile = func() *os.File {
	path := os.Getenv("KNV_TRACE")
	if path == "" {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil
	}
	return f
}()

// trace records one input and the state it produced. Frames and snapshots are
// left out: at twenty a second they would bury the events worth reading.
func (m *Model) trace(kind, detail string) {
	if traceFile == nil {
		return
	}
	f := m.layoutFrame()
	fmt.Fprintf(traceFile, "%-6s %-42s | grid@%d+%d cols=%d box=%dx%d pan=%d,%d zoom=%d accum=%d cur=%d(%s) anchor=%s\n",
		kind, detail, f.gridTop(), f.grid, m.lay.cols, m.lay.boxW, m.lay.boxH,
		m.panX, m.panY, m.zoom, m.wheelAccum, m.cursor, m.cursorName, m.anchorName)
}

// Run starts the program. It returns when the user quits or ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	m := New(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// --- messages ---

type snapshotMsg struct{ snap *model.Snapshot }
type frameMsg time.Time

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.waitSnapshot(), m.tick())
}

// waitSnapshot blocks on the store's channel in a command goroutine. This is the
// only place cluster data enters the UI, and it enters as an immutable value —
// there is no shared state between the informers and the renderer.
func (m *Model) waitSnapshot() tea.Cmd {
	ch := m.snaps
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return tea.QuitMsg{}
		}
		return snapshotMsg{snap}
	}
}

// tick schedules the next animation frame, slowing to idleFPS when nothing is
// moving.
func (m *Model) tick() tea.Cmd {
	fps := m.cfg.FPS
	// A delayed timeline needs the full frame cadence even between animations:
	// snapshots can be only 100ms apart, and an idle 4fps tick would apply
	// several before one render and hide the very transitions playback preserves.
	if (!m.reg.Busy() && m.playback.live) || (!m.playback.live && m.playback.speed == 0) {
		fps = idleFPS
	}
	return tea.Tick(time.Second/time.Duration(fps), func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		defer func() { m.trace("resize", fmt.Sprintf("%dx%d", msg.Width, msg.Height)) }()
		// A resize is the one time a zoomed canvas is re-columned. The frozen
		// count belongs to a window that no longer exists, and holding on to it
		// would leave the cards laid out for the old one.
		m.zoomCols = 0
		m.derive()
		m.zoomCols = m.lay.cols
		return m, nil

	case snapshotMsg:
		wallNow := time.Now()
		var cmds []tea.Cmd
		cmds = append(cmds, m.waitSnapshot())
		for _, snap := range m.playback.Ingest(msg.snap, wallNow) {
			cmds = append(cmds, m.applySnapshot(snap))
		}
		if m.playback.OverLimit(wallNow) {
			cmds = append(cmds, m.goRealtime("playback history limit reached — returned to realtime"))
		}
		return m, tea.Batch(cmds...)

	case frameMsg:
		now := time.Time(msg)
		wallDT := now.Sub(m.last)
		if wallDT < 0 {
			wallDT = 0
		}
		m.last = now
		// Playback is a clock, so it must receive the complete elapsed wall time.
		// Losing the tail of a late frame would make a requested 0.5x rate slower
		// than 0.5x and inflate the reported distance behind realtime.
		playbackDT := wallDT
		// Clamp dt so a suspended terminal does not fast-forward every
		// animation to completion on resume. This clamp belongs only to visual
		// interpolation; it must never alter the cluster timeline above.
		if wallDT > 250*time.Millisecond {
			wallDT = 250 * time.Millisecond
		}
		var cmds []tea.Cmd
		for _, snap := range m.playback.Advance(playbackDT, now) {
			cmds = append(cmds, m.applySnapshot(snap))
		}
		if m.playback.OverLimit(now) {
			cmds = append(cmds, m.goRealtime("playback history limit reached — returned to realtime"))
		}
		clusterDT := wallDT
		if !m.playback.live {
			clusterDT = time.Duration(float64(wallDT) * m.playback.speed)
		}
		m.reg.Advance(clusterDT)
		m.fleet.Step(clusterDT)
		m.pend.Step(clusterDT)
		// The frame tick is also what paces the detail pane's refresh: events
		// arrive while you are reading them, and a pane that never re-reads shows
		// a drain that has since finished.
		if cmd := m.refreshDetail(now); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.captureDetailHistory(now); cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, m.tick())
		return m, tea.Batch(cmds...)

	case detailMsg:
		m.applyDetail(msg)
		return m, nil

	case detailHistoryMsg:
		m.applyDetailHistory(msg)
		return m, nil

	case tea.MouseMsg:
		cmd := m.handleMouse(msg)
		m.trace("mouse", fmt.Sprintf("%-18s x=%d y=%d ctrl=%v alt=%v shift=%v",
			tea.MouseEvent(msg).String(), msg.X, msg.Y, msg.Ctrl, msg.Alt, msg.Shift))
		return m, cmd

	case tea.KeyMsg:
		cmd := m.handleKey(msg)
		m.trace("key", fmt.Sprintf("%q", msg.String()))
		if m.quitting {
			return m, tea.Quit
		}
		return m, cmd
	}
	return m, nil
}

// applySnapshot swaps in new facts and reconciles the animation registry against
// them. Filters deliberately play no part here: hiding a node must not make it
// animate away, or unhiding it would replay its entrance.
//
// It returns the fetch an open detail pane needs when the node it is describing
// has changed identity under it — see followHandover.
func (m *Model) applySnapshot(snap *model.Snapshot) tea.Cmd {
	previous := make(map[string]model.Phase, len(m.snap.Nodes))
	for _, n := range m.snap.Nodes {
		previous[n.Name] = n.Phase
	}
	m.snap = snap
	// Before derive: a NodeClaim becoming a Node renames the thing both the pane
	// and the cursor are tracking, and derive re-finds the cursor by name. Given
	// the new name first, it re-finds the right box; given it afterwards, the
	// cursor has already been reassigned to whatever inherited the old index.
	cmd := m.followHandover()
	m.reg.BeginSync()
	for _, n := range snap.Nodes {
		track := m.reg.Node(n.Name)
		if phase, existed := previous[n.Name]; existed && phase != n.Phase {
			track.Notify()
		}
		if removalReadyToCollapse(n, m.displayNow()) {
			track.SetLeaving()
		}
		for _, p := range n.Pods {
			m.reg.Pod(p.Key())
		}
	}
	m.reg.EndSync()
	m.derive()
	m.refreshHistoricalDetail()
	return cmd
}

// derive recomputes the filtered, sorted node list and the grid geometry. It is
// called whenever facts or view state change — never during View, so rendering
// stays free of side effects.
func (m *Model) derive() {
	m.vis = m.filter.Apply(m.snap, m.sortKey, m.sortDesc)
	gridH := m.gridHeight()
	if m.mode == ModeDense {
		// Dense mode is a one-row-per-node table. It is the same canvas with
		// one-cell-tall cards and no gutter, which lets the pan, the hit test and
		// ensureVisible stay mode-agnostic. The header row is the first cell row.
		m.lay = layout{cols: 1, boxW: m.w, boxH: 1, stepX: max(1, m.w), stepY: 1,
			pages: len(m.vis), gridW: m.w, gridH: len(m.vis), scale: 1}
	} else {
		m.lay = computeLayout(m.mode, m.w, gridH, len(m.vis), m.zoom, m.zoomCols)
	}

	// Re-find the cursor by name so the selection survives re-ordering.
	m.cursor = clampInt(m.cursor, 0, max(0, len(m.vis)-1))
	if m.cursorName != "" {
		for i, v := range m.vis {
			if v.node.Name == m.cursorName {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor < len(m.vis) {
		m.cursorName = m.vis[m.cursor].node.Name
	}
	m.ensureVisible()
}

// frame is the vertical budget for one screen. Every section's height is decided
// here and nowhere else, so View cannot produce a frame that is not exactly m.h
// rows tall — an invariant bubbletea's renderer depends on.
type frame struct {
	header, action, legend, bar, grid, status int
}

func (f frame) gridTop() int { return f.header + f.action + f.legend }

// layoutFrame allocates rows, shedding chrome as the terminal shrinks. Order of
// sacrifice: legend, then header, then the active-action ribbon. The status line
// and at least one grid row always survive, because a screen with no grid and no
// status says nothing.
func (m *Model) layoutFrame() frame {
	var f frame
	h := m.h
	if h >= 2 {
		f.status = 1
	}
	if h >= 8 {
		f.header = headerHeight
	}
	if m.showLegend && h >= 14 {
		f.legend = legendHeight
	}
	if h >= 4 && len(detectConsolidations(m.snap, m.displayNow()).actions) > 0 {
		f.action = 1
	}

	rest := h - f.status - f.gridTop()
	f.bar = min(m.bar.height(), max(0, rest-1))
	f.grid = max(0, rest-f.bar)
	return f
}

func (m *Model) gridHeight() int { return max(1, m.layoutFrame().grid) }

// ensureVisible pans the least it can to bring the selected card fully on
// screen, and does nothing at all when it is already there — a viewport that
// re-centres on every snapshot would undo every scroll.
func (m *Model) ensureVisible() {
	if len(m.vis) == 0 {
		m.panX, m.panY = 0, 0
		return
	}
	row, col := m.cursor/max(1, m.lay.cols), m.cursor%max(1, m.lay.cols)
	x0, y0 := col*m.lay.stepX, row*m.lay.stepY
	w, h := m.w, m.gridHeight()

	if x0 < m.panX {
		m.panX = x0
	} else if x1 := x0 + m.lay.boxW; x1 > m.panX+w {
		m.panX = x1 - w
	}
	if y0 < m.panY {
		m.panY = y0
	} else if y1 := y0 + m.lay.boxH; y1 > m.panY+h {
		m.panY = y1 - h
	}
	m.clampPan()
}

// clampPan keeps the window on the canvas, and centres the canvas on the axes
// where it is smaller than the window — a negative pan is the margin.
func (m *Model) clampPan() {
	w, h := m.w, m.gridHeight()
	if m.lay.gridW <= w {
		m.panX = -(w - m.lay.gridW) / 2
	} else {
		m.panX = clampInt(m.panX, 0, m.lay.gridW-w)
	}
	if m.lay.gridH <= h {
		m.panY = -(h - m.lay.gridH) / 2
	} else {
		m.panY = clampInt(m.panY, 0, m.lay.gridH-h)
	}
	if m.mode == ModeDense {
		// The table has a header row and no vertical centring: it is a list.
		m.panY = clampInt(m.panY, 0, max(0, m.lay.pages-max(1, h-1)))
	}
}

// visibleRows is how many whole card rows the viewport shows, for paging.
func (m *Model) visibleRows() int {
	if m.mode == ModeDense {
		return max(1, m.gridHeight()-1)
	}
	return max(1, m.lay.rows(m.gridHeight()))
}

// scrollRow is the first card row at least partly visible; dense mode's table
// renderer and the hit test both count from it.
func (m *Model) scrollRow() int {
	if m.lay.stepY <= 0 {
		return 0
	}
	return max(0, m.panY) / m.lay.stepY
}

// --- view state helpers used by commands ---

func (m *Model) setMode(mode Mode) {
	m.mode = mode
	m.derive()
}

// setZoom changes the zoom level and reports whether it moved, so a keypress at
// the end of the range can say so instead of silently doing nothing.
//
// derive re-finds the cursor by name and scrolls it back into view, which is
// what makes zooming in on the selected node the default behaviour: the card you
// were pointing at is the one still on screen when the grid gets coarser.
func (m *Model) setZoom(zoom int) bool {
	zoom = clampInt(zoom, zoomMin, zoomMax)
	if zoom == m.zoom {
		return false
	}
	switch {
	case zoom == 0:
		m.zoomCols = 0 // back to fit: lay the grid out for the window again
	case m.zoom == 0:
		// Leaving fit: freeze the column count so that from here on zooming
		// resizes the cards without moving any of them.
		m.zoomCols = m.lay.cols
	}
	m.zoom = zoom
	m.derive()
	return true
}

// keyZoomStep is how many levels one press of z or Z moves.
//
// The levels are deliberately fine so the wheel is smooth; a keypress wants a
// coarser grain, or reaching either end of the range is a dozen presses during a
// talk. Two levels is about 1.4× — a step you can see, from a key you pressed on
// purpose.
const keyZoomStep = 2

// anchorIndex is the card the zoom is about: the one the wheel last aimed at,
// or the selection when the mouse has not been used or the aimed node has gone.
func (m *Model) anchorIndex() int {
	if m.anchorName != "" {
		for i, v := range m.vis {
			if v.node.Name == m.anchorName {
				return i
			}
		}
	}
	return clampInt(m.cursor, 0, max(0, len(m.vis)-1))
}

// setAnchor points the zoom at a card, and moves the selection there so the
// highlight says what the next zoom will grow.
func (m *Model) setAnchor(idx int) {
	if idx < 0 || idx >= len(m.vis) {
		return
	}
	m.anchorName = m.vis[idx].node.Name
	m.cursor, m.cursorName = idx, m.anchorName
}

// zoomBy steps the level, keeping the selected card where it is on screen. This
// is the keyboard path: it has no pointer, so it holds the card's own top-left
// corner still, and it zooms about the selection because that is what the
// keyboard was last pointing at.
func (m *Model) zoomBy(delta int) {
	m.anchorName = ""
	idx := clampInt(m.cursor, 0, max(0, len(m.vis)-1))
	row, col := idx/max(1, m.lay.cols), idx%max(1, m.lay.cols)
	m.zoomAbout(delta, col*m.lay.stepX-m.panX, row*m.lay.stepY-m.panY, 0, 0)
}

// zoomAtPointer zooms about a point on the screen — the pointer — so the card
// under it stays under it. This is what makes the gesture aimable.
func (m *Model) zoomAtPointer(delta, x, y int) {
	f := m.layoutFrame()
	gridY := y - f.gridTop()

	// Where in the anchored card the pointer is, as a fraction of the card. The
	// same fraction of the resized card goes back under the same pixel, which is
	// what "zoom about a point" means.
	//
	// Clamped to the card, but only just outside it does that bite. Two things
	// can put the pointer off the anchor: a gutter, where clamping costs a cell
	// or two and is stable; and the view having been moved by something else
	// between notches — an arrow key, a filter — where the offset is meaningless
	// and clamping is what walks the card back under the pointer. Honouring a
	// wild offset exactly would preserve a relationship that no longer means
	// anything, and leave the pointer sitting on a neighbour.
	idx := m.anchorIndex()
	row, col := idx/max(1, m.lay.cols), idx%max(1, m.lay.cols)
	fx := frac(x+m.panX-col*m.lay.stepX, m.lay.boxW)
	fy := frac(gridY+m.panY-row*m.lay.stepY, m.lay.boxH)
	m.zoomAbout(delta, x, gridY, fx, fy)
}

// zoomAtCentre zooms about the middle of the viewport, holding the selected
// card's middle there. It is what the mouse falls back to without a usable
// pointer position, and it cannot drift: the fixed point is the screen's own
// centre, not a card edge.
func (m *Model) zoomAtCentre(delta int) {
	m.zoomAbout(delta, m.w/2, m.gridHeight()/2, 0.5, 0.5)
}

// zoomAbout steps the zoom and pans so that the point (fx, fy) *within the
// selected card* — as a fraction of its size — lands back on screen position
// (screenX, gridY).
//
// Because the column count is frozen while zoomed, the card keeps its row and
// column, so this is exact: the card does not merely stay visible, it stays
// where it was, and its neighbours grow around it instead of being dealt out
// into different places.
func (m *Model) zoomAbout(delta, screenX, gridY int, fx, fy float64) {
	if m.mode == ModeDense {
		m.notify("dense mode is one row per node — zoom applies to pods and nodes modes", true)
		return
	}

	before := m.lay
	moved := m.setZoom(m.zoom + delta)

	// Skip levels that redraw nothing. Card sizes are whole cells and are clamped
	// by the viewport, so near the ends of the range several consecutive levels
	// resolve to the same grid — a step that changes only a number in the status
	// bar reads as a key that did not work. Only stepping does this; ":zoom 5"
	// means level 5 exactly.
	dir := 1
	if delta < 0 {
		dir = -1
	}
	for m.lay.sameGeometry(before) && m.setZoom(m.zoom+dir) {
		moved = true
	}

	if !moved || m.lay.sameGeometry(before) {
		if delta > 0 {
			m.notify("already fully zoomed in", true)
		} else {
			m.notify("already fully zoomed out", true)
		}
		return
	}

	idx := m.anchorIndex()
	row, col := idx/max(1, m.lay.cols), idx%max(1, m.lay.cols)
	m.panX = col*m.lay.stepX + int(fx*float64(m.lay.boxW)+0.5) - screenX
	m.panY = row*m.lay.stepY + int(fy*float64(m.lay.boxH)+0.5) - gridY
	m.keepWholeCard(row, col)
	m.clampPan()

	m.notify(fmt.Sprintf("zoom: %s · %d per row", zoomLabel(m.lay.scale), m.lay.cols), false)
}

// keepWholeCard pulls the pan back, if it must, until the anchored card is
// whole on screen.
//
// Holding the pointer's point perfectly still is right until the card is nearly
// as big as the window, at which point it starts to hang off an edge and can
// never be seen entire however far you zoom — you asked to look at that card and
// got two halves of two cards. The constraint is loose while zoomed out, where
// it costs nothing, and tightens into exact alignment as the card approaches the
// size of the window.
func (m *Model) keepWholeCard(row, col int) {
	x0, y0 := col*m.lay.stepX, row*m.lay.stepY
	x1, y1 := x0+m.lay.boxW, y0+m.lay.boxH
	w, h := m.w, m.gridHeight()

	if m.lay.boxW <= w {
		m.panX = clampInt(m.panX, x1-w, x0)
	} else {
		m.panX = clampInt(m.panX, x0, x1-w) // card wider than the window: no gaps
	}
	if m.lay.boxH <= h {
		m.panY = clampInt(m.panY, y1-h, y0)
	} else {
		m.panY = clampInt(m.panY, y0, y1-h)
	}
}

// frac is v as a fraction of size, clamped to the card: see zoomAtPointer.
//
// The clamp is to the last cell *inside* the card, not to its width. Clamping to
// the width puts the card's exclusive right edge under the pointer, which is the
// first cell of the gutter — the pointer would land one cell past the card it is
// supposed to be holding.
func frac(v, size int) float64 {
	if size <= 0 {
		return 0
	}
	return float64(clampInt(v, 0, size-1)) / float64(size)
}

func (m *Model) selected() *visible {
	if m.cursor < 0 || m.cursor >= len(m.vis) {
		return nil
	}
	return &m.vis[m.cursor]
}

func (m *Model) phaseCounts() map[model.Phase]int {
	counts := map[model.Phase]int{}
	for _, n := range m.snap.Nodes {
		counts[n.Phase]++
	}
	return counts
}

func (m *Model) knownNodePool(name string) bool {
	for _, np := range m.snap.NodePools {
		if np.Name == name {
			return true
		}
	}
	// Nodes can carry a nodepool label with no NodePool object visible (no RBAC
	// on the CRD, for instance), so accept a pool we have seen on a node too.
	for _, n := range m.snap.Nodes {
		if n.NodePool == name {
			return true
		}
	}
	return false
}

func (m *Model) snapshotPools() []string {
	seen := map[string]bool{}
	var out []string
	for _, np := range m.snap.NodePools {
		if !seen[np.Name] {
			seen[np.Name] = true
			out = append(out, np.Name)
		}
	}
	for _, n := range m.snap.Nodes {
		if n.NodePool != "" && !seen[n.NodePool] {
			seen[n.NodePool] = true
			out = append(out, n.NodePool)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Model) notify(msg string, isErr bool) {
	m.msg, m.msgErr, m.msgAt = msg, isErr, time.Now()
}

func (m *Model) displayNow() time.Time { return m.playback.DisplayNow(time.Now()) }

func (m *Model) setPlaybackSpeed(speed float64) error {
	wasLive := m.playback.live
	now := time.Now()
	if err := m.playback.SetSpeed(speed, now); err != nil {
		return err
	}
	if wasLive {
		m.clearDetailHistory()
		m.detailHistoryExhausted = false
		m.seedOpenDetailHistory(now)
	}
	return nil
}

func (m *Model) togglePause() {
	wasLive := m.playback.live
	now := time.Now()
	m.playback.TogglePause(now)
	if wasLive {
		m.clearDetailHistory()
		m.detailHistoryExhausted = false
		m.seedOpenDetailHistory(now)
	}
	m.notify(playbackStatus(m.playback, now), false)
}

func (m *Model) rewindPlayback(amount time.Duration) (tea.Cmd, time.Duration) {
	wasLive := m.playback.live
	snap, moved := m.playback.Rewind(amount, time.Now())
	if snap == nil || moved <= 0 {
		return nil, 0
	}
	if wasLive {
		// List snapshots are cheap enough to retain continuously. Describe data
		// requires API calls per node, so it begins sampling only once playback is
		// delayed; a detail pane honestly labels itself live until a sample exists.
		m.clearDetailHistory()
		m.detailHistoryExhausted = false
	}
	// A rewind is a seek, not an animation. Throw away interpolation from the
	// newer frame so cards and meters land atomically on the selected snapshot;
	// playback speed applies only to movement after this point.
	m.reg = anim.NewRegistry()
	m.fleet = &anim.Track{}
	m.pend = &anim.Track{}
	cmd := m.applySnapshot(snap)
	m.reg.Advance(anim.ExitDur)
	if d := m.detail; d != nil {
		if detail, at, ok := m.historicalDetail(d.name, d.claim, d.providerID); ok {
			d.detail, d.sampleAt = detail, at
			d.historical, d.liveFallback, d.forceLive, d.loading = true, false, false, false
		} else {
			d.historical, d.liveFallback, d.forceLive = false, true, false
			d.sampleAt = time.Time{}
		}
	}
	return cmd, moved
}

func (m *Model) forwardPlayback(amount time.Duration) (tea.Cmd, time.Duration) {
	snap, moved := m.playback.Forward(amount, time.Now())
	if moved <= 0 {
		return nil, 0
	}
	if snap == nil {
		m.refreshHistoricalDetail()
		return nil, moved
	}
	// Like rewind, an explicit forward seek lands atomically.
	m.reg = anim.NewRegistry()
	m.fleet = &anim.Track{}
	m.pend = &anim.Track{}
	cmd := m.applySnapshot(snap)
	m.reg.Advance(anim.ExitDur)
	return cmd, moved
}

// goRealtime is the one operation that discards history. Setting speed to 1x
// intentionally does not call it: that plays forward normally from the current
// delayed frame and keeps the existing lag.
func (m *Model) goRealtime(message string) tea.Cmd {
	wasLive := m.playback.live
	now := time.Now()
	snap := m.playback.GoLive(now)
	if !m.playback.archive {
		m.clearDetailHistory()
		m.detailHistoryExhausted = false
	}
	if message == "" {
		message = playbackStatus(m.playback, now)
	}
	m.notify(message, strings.Contains(message, "limit"))
	var cmds []tea.Cmd
	if !wasLive && snap != nil && (m.snap == nil || snap.Generation != m.snap.Generation) {
		cmds = append(cmds, m.applySnapshot(snap))
	}
	if !wasLive && m.detail != nil && m.describe != nil {
		m.detail.historical, m.detail.liveFallback, m.detail.forceLive, m.detail.loading = false, false, false, true
		cmds = append(cmds, m.fetchDetail(m.detail.name, m.detail.claim))
	}
	return tea.Batch(cmds...)
}

// --- View ---

func (m *Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.quitting {
		return ""
	}
	// The detail pane comes first, matching the key router: while it is up it owns
	// the screen and the keyboard both.
	if m.detail != nil {
		return m.renderSeekOverlay(m.renderDetail(m.w, m.h), m.w, m.h)
	}
	if m.showHelp {
		return m.renderSeekOverlay(m.renderHelp(m.w, m.h), m.w, m.h)
	}

	now := m.displayNow()
	actions := detectConsolidations(m.snap, now)
	ctx := boxCtx{reg: m.reg, mode: m.mode, now: now, actions: actions.members}

	f := m.layoutFrame()
	var lines []string
	if f.header > 0 {
		lines = append(lines, strings.Split(m.renderHeader(m.w), "\n")...)
	}
	if f.action > 0 {
		lines = append(lines, renderConsolidationRibbon(m.w, actions))
	}
	if f.legend > 0 {
		lines = append(lines, strings.Split(m.renderLegend(m.w), "\n")...)
	}
	if f.grid > 0 {
		var grid string
		if m.mode == ModeDense {
			grid = renderDense(m.vis, m.w, f.grid, ctx, m.scrollRow(), m.cursor)
		} else {
			grid = renderGrid(m.vis, m.lay, m.w, f.grid, ctx, m.panX, m.panY, m.cursor)
		}
		lines = append(lines, strings.Split(grid, "\n")...)
	}
	if f.bar > 0 {
		lines = append(lines, m.bar.render(m.w, f.bar)...)
	}
	if f.status > 0 {
		lines = append(lines, m.renderStatus(m.w))
	}

	// Normalise as a last line of defence: a section that miscounts its own
	// height must not be able to corrupt the whole frame.
	for i := range lines {
		lines[i] = padRow(lines[i], m.w)
	}
	for len(lines) < m.h {
		lines = append(lines, padRow("", m.w))
	}
	return m.renderSeekOverlay(strings.Join(lines[:m.h], "\n"), m.w, m.h)
}
