package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// Describer fetches the describe payload for one node, on demand.
//
// Both sources implement it — the live cluster with an API read, the simulation
// from its own recorded history — so the pane behaves identically in a rehearsal
// and in front of a real cluster. A nil Describer degrades the pane to what the
// snapshot already knows rather than disabling it.
type Describer interface {
	DescribeNode(ctx context.Context, name, nodeClaim string) (*model.NodeDetail, error)
}

const (
	// detailTimeout bounds one fetch. A hung API server must not leave the pane
	// saying "loading" forever, and it must never block the event loop — the
	// fetch runs in a bubbletea command, so the grid keeps animating throughout.
	detailTimeout = 5 * time.Second
	// detailRefresh is how often an open pane re-reads. Events are the reason
	// this pane exists and they arrive while you are looking at it; a static
	// snapshot of a node that is mid-drain is the one thing worse than no pane.
	detailRefresh = 5 * time.Second
	// detailWheelRows is how far one wheel notch scrolls the text.
	detailWheelRows = 3
)

// detailView is the describe pane's state.
//
// It is deliberately the *only* thing opening the pane changes. Filters, sort,
// zoom, pan and the cursor are untouched, which is what makes Esc exact rather
// than approximate: there is no view state to restore because none was
// disturbed. Resist the temptation to "tidy up" the grid on open.
type detailView struct {
	name string
	// claim is the node's NodeClaim, captured at open. The live source needs it
	// to find the Karpenter events, which is where a node's provisioning and
	// disruption decisions are recorded.
	claim string
	// providerID is the cloud instance ID, when the source knew one. It and the
	// claim name are what let the pane follow a provisioning NodeClaim through to
	// the Node that replaces it — see followHandover.
	providerID string
	detail     *model.NodeDetail
	err        error
	loading    bool
	// fetchedAt is when the last attempt finished, successful or not, so the
	// refresh timer paces retries as well as reloads.
	fetchedAt time.Time
	// historical means detail came from the playback recorder at sampleAt.
	// liveFallback is the explicit, labelled escape hatch when that history is
	// unavailable or the user asks for a fresh read while the grid is delayed.
	historical   bool
	liveFallback bool
	forceLive    bool
	sampleAt     time.Time
	scroll       int
}

// describingClaim reports whether what is on screen is a NodeClaim's payload for
// the object the pane is currently pointed at. The name check is what keeps the
// handover honest: a claim's detail lingers for a fetch after the pane has moved
// on to the Node, and during that frame it is stale data about a different
// object, not grounds for telling the reader there is no Node yet.
func (d *detailView) describingClaim() bool {
	return d.detail != nil && d.detail.Kind == "NodeClaim" && d.detail.Name == d.name
}

// detailMsg carries a completed fetch back into Update.
type detailMsg struct {
	name   string
	detail *model.NodeDetail
	err    error
}

// openDetail shows the describe pane for the selected node and starts its fetch.
func (m *Model) openDetail() tea.Cmd {
	sel := m.selected()
	if sel == nil {
		m.notify("no node selected", true)
		return nil
	}
	n := sel.node
	m.detail = &detailView{name: n.Name, claim: n.NodeClaim, providerID: n.ProviderID,
		loading: m.describe != nil}
	if !m.playback.live {
		if detail, at, ok := m.historicalDetail(n.Name, n.NodeClaim, n.ProviderID); ok {
			m.detail.detail, m.detail.sampleAt = detail, at
			m.detail.historical, m.detail.loading = true, false
			return nil
		}
		m.detail.liveFallback = true
	}
	if m.describe == nil {
		m.detail.err = errors.New("no describe source: events need a live or simulated cluster")
		return nil
	}
	return m.fetchDetail(m.detail.name, m.detail.claim)
}

// closeDetail returns to the grid. Nothing else is reset — see detailView.
func (m *Model) closeDetail() { m.detail = nil }

func (m *Model) fetchDetail(name, claim string) tea.Cmd {
	src := m.describe
	if src == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), detailTimeout)
		defer cancel()
		detail, err := src.DescribeNode(ctx, name, claim)
		return detailMsg{name: name, detail: detail, err: err}
	}
}

// refreshDetail re-reads an open pane once its content has gone stale. It is
// driven from the frame tick rather than its own timer so that a pane which is
// closed and reopened cannot leave a ticker behind.
func (m *Model) refreshDetail(now time.Time) tea.Cmd {
	d := m.detail
	if d == nil || d.loading || m.describe == nil {
		return nil
	}
	if !m.playback.live && !d.liveFallback {
		return nil
	}
	if now.Sub(d.fetchedAt) < detailRefresh {
		return nil
	}
	d.loading = true
	return m.fetchDetail(d.name, d.claim)
}

// followHandover retargets an open pane when the NodeClaim it is watching turns
// into a Node.
//
// The two objects have different names, so a pane opened on a provisioning claim
// is pointed at a name that ceases to exist the moment kubelet registers — which
// is exactly when the reader is watching. Left alone the pane would say the node
// is gone, having in fact just succeeded. Following the claim is the whole
// lifecycle in one pane: the claim's own fields, then the Node's, with no gap
// and no keystroke.
//
// The claim's detail stays on screen until the Node's read lands. Almost all of
// it — capacity, labels, the event stream — is the same, and blanking the pane
// for a fetch would flicker at the busiest moment of the node's life.
//
// Called from applySnapshot after the new snapshot is installed and before
// derive, which is what lets the cursor be carried across the rename too.
func (m *Model) followHandover() tea.Cmd {
	d := m.detail
	if d == nil || m.snapNode(d.name) != nil {
		return nil
	}
	n := m.nodeForClaim(d.claim, d.providerID)
	if n == nil || n.Name == d.name {
		return nil
	}
	// Take the cursor along: Esc has to land on the node you were just reading
	// about, not on whichever box inherited the old index. derive, next, is what
	// turns the new name back into an index.
	if m.cursorName == d.name {
		m.cursorName = n.Name
	}
	d.name, d.err = n.Name, nil
	if n.NodeClaim != "" {
		d.claim = n.NodeClaim
	}
	if n.ProviderID != "" {
		d.providerID = n.ProviderID
	}
	if m.describe == nil {
		return nil
	}
	if !m.playback.live && !d.liveFallback {
		d.loading = false
		m.refreshHistoricalDetail()
		return nil
	}
	d.loading = true
	return m.fetchDetail(d.name, d.claim)
}

// nodeForClaim finds the node a claim has been joined to, by claim name or by
// the providerID both objects carry. Name is no help here — the pane is looking
// precisely because the name it had is not in the snapshot any more.
func (m *Model) nodeForClaim(claim, providerID string) *model.Node {
	if claim == "" && providerID == "" {
		return nil
	}
	for _, n := range m.snap.Nodes {
		if n.Phase == model.PhaseGone {
			continue
		}
		if claim != "" && n.NodeClaim == claim {
			return n
		}
		if providerID != "" && n.ProviderID == providerID {
			return n
		}
	}
	return nil
}

// applyDetail installs a fetch result, ignoring one that belongs to a pane the
// user has already left or replaced.
func (m *Model) applyDetail(msg detailMsg) {
	d := m.detail
	if d == nil || d.name != msg.name {
		return
	}
	d.loading, d.fetchedAt = false, time.Now()
	if msg.err != nil {
		d.err = msg.err
	} else {
		// Newest-first order is the pane's promise, so it is the pane that keeps
		// it: sorting here rather than trusting each source means a source that
		// hands back events in whatever order the API server listed them still
		// reads correctly. Once per fetch, not once per frame.
		model.SortEvents(msg.detail.Events)
		d.detail, d.err = msg.detail, nil
		d.historical = false
		if !m.playback.live {
			d.liveFallback = true
		}
	}
	d.scroll = clampInt(d.scroll, 0, m.detailMaxScroll())
}

// handleDetailKey routes a keypress while the pane is up.
//
// Unlike the help overlay, an unrecognised key does nothing at all: the pane is
// somewhere you read and scroll for a while, and losing your place in a long
// event list to a stray keystroke would be its own bug.
func (m *Model) handleDetailKey(msg tea.KeyMsg) tea.Cmd {
	d := m.detail
	rows := m.detailBodyRows()
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
	case "esc", "backspace", "q", "enter":
		m.closeDetail()
	case "ctrl+r":
		if m.describe != nil && !d.loading {
			d.liveFallback, d.forceLive, d.historical = !m.playback.live, !m.playback.live, false
			d.loading = true
			return m.fetchDetail(d.name, d.claim)
		}
	case "up", "k":
		m.scrollDetail(-1)
	case "down", "j":
		m.scrollDetail(1)
	case "pgup", "ctrl+b":
		m.scrollDetail(-rows)
	case "pgdown", "ctrl+f", " ":
		m.scrollDetail(rows)
	case "g", "home":
		d.scroll = 0
	case "G", "end":
		d.scroll = m.detailMaxScroll()
	}
	return nil
}

func (m *Model) scrollDetail(rows int) {
	if m.detail == nil {
		return
	}
	m.detail.scroll = clampInt(m.detail.scroll+rows, 0, m.detailMaxScroll())
}

// detailBodyRows is how many text rows the pane shows between its title and
// footer strips.
func (m *Model) detailBodyRows() int { return max(1, m.h-2) }

func (m *Model) detailMaxScroll() int {
	return max(0, len(m.detailLines(m.w))-m.detailBodyRows())
}

// snapNode finds a node in the current snapshot by name.
//
// The pane reads its live figures — utilisation, pods, phase — from the snapshot
// rather than from the fetched detail, so a node that fills up or starts draining
// while you are reading about it says so without waiting for the next fetch.
func (m *Model) snapNode(name string) *model.Node {
	for _, n := range m.snap.Nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// --- line model ---

// span is a run of text in one colour. Building the pane as spans rather than as
// pre-styled strings keeps every line measurable in cells, which is what lets the
// pane scroll and truncate without ever emitting a row of the wrong width.
type span struct {
	text string
	col  lipgloss.Color
	bold bool
}

type detailLine struct{ spans []span }

func sp(text string, col lipgloss.Color) span  { return span{text: text, col: col} }
func spb(text string, col lipgloss.Color) span { return span{text: text, col: col, bold: true} }

// --- content ---

// kvWidth is the label column in the identity block.
const kvWidth = 12

// detailLines builds the whole pane body for a width. It is a pure function of
// the model, called from both the key handler (which needs the length to clamp
// scrolling) and View.
func (m *Model) detailLines(w int) []detailLine {
	t := theme.Current
	d := m.detail
	if d == nil {
		return nil
	}
	avail := max(24, w-4)

	var out []detailLine
	add := func(spans ...span) { out = append(out, detailLine{spans: spans}) }
	gap := func() {
		if len(out) > 0 {
			add()
		}
	}
	head := func(s string) {
		gap()
		add(spb(s, t.Accent))
	}
	kv := func(k, v string) {
		if v == "" {
			return
		}
		add(sp(padTo(k, kvWidth), t.Dim), sp(truncHead(v, avail-kvWidth), t.Fg))
	}
	wrapped := func(s string, col lipgloss.Color) {
		for _, line := range wrapText(s, avail) {
			add(sp(line, col))
		}
	}

	node, det := m.snapNode(d.name), d.detail
	now := m.displayNow()
	if d.liveFallback && !m.playback.live {
		head("LIVE DETAIL")
		add(sp(fmt.Sprintf("detail history is unavailable; this pane is live while the overview is %s behind",
			playbackLag(m.playback.Behind(time.Now()))), t.Warn))
	} else if d.historical {
		head("PLAYBACK DETAIL")
		add(sp("node details and events are aligned with the delayed cluster timeline", t.Dim))
	}

	if node != nil {
		state := phaseLabel(node.Phase)
		if reason := phaseDescription(node); reason != "" {
			state += " — " + reason
		}
		add(sp(padTo("state", kvWidth), t.Dim),
			spb(truncHead(state, avail-kvWidth), theme.Mix(t.PhaseColor(int(node.Phase)), t.Fg, 0.25)))
		if history := phaseHistoryLabel(node, now); history != "" {
			add(sp(padTo("lifecycle", kvWidth), t.Dim), sp(truncHead(history, avail-kvWidth), t.Fg))
		}
		instance := joinParts(" · ", node.InstanceType, node.Arch, node.CapacityType)
		if node.HasPrice {
			instance = joinParts(" · ", instance, fmt.Sprintf("$%.3f/hr", node.Price))
		}
		kv("instance", instance)
		kv("location", joinParts(" / ", node.Zone, node.Region))
		kv("nodepool", joinParts(" · ", node.NodePool, claimLabel(node.NodeClaim)))
		if node.Consolidatable != model.ConsolidationUnknown {
			// The same verdict the table's CONS column shows, with the message and
			// the age the column has no room for — which is the whole reason to open
			// the pane on a node marked y.
			verdict := node.Consolidatable.String()
			if node.ConsolidationReason != "" {
				verdict += " — " + node.ConsolidationReason
			}
			if !node.ConsolidationAt.IsZero() {
				verdict += fmt.Sprintf(" (%s ago)", model.HumanAge(now.Sub(node.ConsolidationAt)))
			}
			add(sp(padTo("consolidate", kvWidth), t.Dim),
				sp(truncHead(verdict, avail-kvWidth), consolidationColor(node.Consolidatable)))
		}
		if !node.Created.IsZero() {
			kv("age", fmt.Sprintf("%s (created %s)", model.HumanAge(now.Sub(node.Created)),
				node.Created.Local().Format("2006-01-02 15:04:05")))
		}
	} else {
		kv("state", "not in the current snapshot — the node has left the cluster")
	}

	if det != nil {
		if d.describingClaim() {
			// Say what is being described, because the blocks below are the claim's
			// and the absent ones — kubelet, addresses — are absent rather than empty.
			add(sp(padTo("object", kvWidth), t.Dim),
				sp(truncHead("nodeclaim — kubelet has not registered, so there is no Node object yet",
					avail-kvWidth), t.Warn))
		}
		kv("provider", det.ProviderID)
		kv("kubelet", joinParts(" · ", det.System.Kubelet, det.System.ContainerRuntime, det.System.OSImage))
		kv("kernel", det.System.Kernel)
		if len(det.Addresses) > 0 {
			parts := make([]string, 0, len(det.Addresses))
			for _, a := range det.Addresses {
				parts = append(parts, a.Type+" "+a.Address)
			}
			kv("addresses", strings.Join(parts, " · "))
		}
	}

	if node != nil {
		m.appendCapacity(&out, node, det, avail)
	}

	switch {
	case det == nil && d.loading:
		head("DETAIL")
		add(sp("reading node details…", t.Dim))
	case det == nil:
		head("DETAIL")
		if d.err != nil {
			wrapped(d.err.Error(), t.Err)
		} else {
			add(sp("no detail available", t.Dim))
		}
	default:
		if d.err != nil {
			// A failed refresh over content that already loaded: say so, but keep
			// showing what we have. Stale detail beats an empty pane.
			head("REFRESH FAILED")
			wrapped(d.err.Error(), t.Err)
		}
		appendConditions(&out, det, avail, now)
		appendTaints(&out, det)
	}

	// Events come before the pod table and before labels and annotations, which is
	// not where kubectl describe puts them. They are why the pane exists, and on a
	// thirty-row terminal a hundred labels above them means scrolling past noise
	// to reach the thing you opened it for.
	if det != nil {
		appendEvents(&out, det, avail, now, d.historical)
	}
	if node != nil {
		appendPods(&out, node, avail, now)
	}
	if det != nil {
		appendMap(&out, "LABELS", det.Labels, avail)
		appendMap(&out, "ANNOTATIONS", det.Annotations, avail)
	}
	return out
}

// appendCapacity draws the meters, on the same requests/usage basis the grid is
// using, so the pane and the card behind it can never disagree.
func (m *Model) appendCapacity(out *[]detailLine, node *model.Node, det *model.NodeDetail, avail int) {
	t := theme.Current
	add := func(spans ...span) { *out = append(*out, detailLine{spans: spans}) }
	*out = append(*out, detailLine{}, detailLine{spans: []span{spb("CAPACITY", t.Accent)}})

	alloc := node.Allocatable
	if det != nil && det.Allocatable.CPUMilli > 0 {
		alloc = det.Allocatable
	}
	barW := clampInt(avail-46, 10, 30)

	meter := func(label string, used, total int64, human func(int64) string) {
		frac := 0.0
		if total > 0 {
			frac = clamp01(float64(used) / float64(total))
		}
		spans := []span{sp(padTo(label, 14), t.Dim)}
		spans = append(spans, meterSpans(frac, barW)...)
		spans = append(spans, sp(fmt.Sprintf(" %3.0f%%  %s / %s", frac*100, human(used), human(total)), t.Fg))
		add(spans...)
	}

	meter("cpu requests", node.Requests.CPUMilli, alloc.CPUMilli, model.HumanCPU)
	meter("mem requests", node.Requests.MemBytes, alloc.MemBytes, model.HumanMem)
	if node.HasUsage {
		meter("cpu usage", node.Usage.CPUMilli, alloc.CPUMilli, model.HumanCPU)
		meter("mem usage", node.Usage.MemBytes, alloc.MemBytes, model.HumanMem)
	}
	meter("pods", int64(len(node.Pods)), max64(alloc.Pods, int64(len(node.Pods))), func(v int64) string {
		return fmt.Sprintf("%d", v)
	})
	if alloc.GPU > 0 {
		add(sp(padTo("gpu", 14), t.Dim), sp(fmt.Sprintf("%d allocatable", alloc.GPU), t.Fg))
	}
	// Capacity minus allocatable is the reservation, and the gap is where "the
	// node has 16 cores but nothing 16-core-shaped fits" comes from.
	if det != nil && det.Capacity.CPUMilli > 0 {
		add(sp(padTo("reserved", 14), t.Dim), sp(fmt.Sprintf("cpu %s · mem %s (capacity %s / %s)",
			model.HumanCPU(det.Capacity.CPUMilli-det.Allocatable.CPUMilli),
			model.HumanMem(det.Capacity.MemBytes-det.Allocatable.MemBytes),
			model.HumanCPU(det.Capacity.CPUMilli), model.HumanMem(det.Capacity.MemBytes)), t.Dim))
	}
}

func appendConditions(out *[]detailLine, det *model.NodeDetail, avail int, now time.Time) {
	t := theme.Current
	if len(det.Conditions) == 0 {
		return
	}
	*out = append(*out, detailLine{}, detailLine{spans: []span{spb("CONDITIONS", t.Accent)}})
	const typeW, statusW, ageW, reasonW = 18, 9, 7, 26
	for _, c := range det.Conditions {
		col := conditionColor(c)
		age := ""
		if !c.Changed.IsZero() {
			age = model.HumanAge(now.Sub(c.Changed))
		}
		spans := []span{
			sp(padTo(c.Type, typeW), t.Fg),
			spb(padTo(c.Status, statusW), col),
			sp(padTo(age, ageW), t.Dim),
			sp(padTo(c.Reason, reasonW), t.Dim),
		}
		if rest := avail - typeW - statusW - ageW - reasonW; rest > 8 {
			spans = append(spans, sp(truncHead(c.Message, rest), t.Dim))
		}
		*out = append(*out, detailLine{spans: spans})
	}
}

// conditionColor separates "not yet" from "wrong". Unknown on a positive
// condition is the normal state of a node that is still coming up — every
// milestone on a fresh NodeClaim reads Unknown — and painting a routine
// ninety-second wait in the error colour would teach the reader to ignore it.
func conditionColor(c model.Condition) lipgloss.Color {
	t := theme.Current
	switch {
	case !c.Bad():
		return t.Ok
	case c.Status == "Unknown":
		return t.Warn
	default:
		return t.Err
	}
}

func appendTaints(out *[]detailLine, det *model.NodeDetail) {
	t := theme.Current
	if len(det.Taints) == 0 && len(det.Conditions) == 0 {
		// A node with no taints still has conditions; both empty means there was no
		// Node object to read — a provisioning NodeClaim — and "TAINTS none" would
		// be asserting something we never learned.
		return
	}
	*out = append(*out, detailLine{}, detailLine{spans: []span{spb("TAINTS", t.Accent)}})
	if len(det.Taints) == 0 {
		*out = append(*out, detailLine{spans: []span{sp("none", t.Dim)}})
		return
	}
	for _, tn := range det.Taints {
		*out = append(*out, detailLine{spans: []span{sp(tn.String(), t.Warn)}})
	}
}

// appendMap renders labels and annotations, sorted, one per line. Values are
// truncated rather than wrapped: these are keyed data, and a wrapped value makes
// the key column impossible to scan.
func appendMap(out *[]detailLine, heading string, kv map[string]string, avail int) {
	t := theme.Current
	if len(kv) == 0 {
		return
	}
	keys := make([]string, 0, len(kv))
	keyW := 0
	for k := range kv {
		keys = append(keys, k)
		keyW = max(keyW, runewidth.StringWidth(k)+2)
	}
	sort.Strings(keys)
	keyW = clampInt(keyW, 12, max(12, avail/2))

	*out = append(*out, detailLine{}, detailLine{spans: []span{spb(fmt.Sprintf("%s (%d)", heading, len(keys)), t.Accent)}})
	for _, k := range keys {
		*out = append(*out, detailLine{spans: []span{
			sp(padTo(k, keyW), t.Dim),
			sp(truncHead(kv[k], avail-keyW), t.Fg),
		}})
	}
}

func appendPods(out *[]detailLine, node *model.Node, avail int, now time.Time) {
	t := theme.Current
	*out = append(*out, detailLine{}, detailLine{spans: []span{
		spb(fmt.Sprintf("PODS (%d)", len(node.Pods)), t.Accent)}})
	if len(node.Pods) == 0 {
		*out = append(*out, detailLine{spans: []span{sp("none scheduled", t.Dim)}})
		return
	}

	// Sorted by namespace then name, the way kubectl describe lists them, rather
	// than in the cell order the card uses: this is a table you read down.
	pods := make([]*model.Pod, len(node.Pods))
	copy(pods, node.Pods)
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})

	stateW, cpuW, memW, ageW := 14, 8, 9, 6
	nameW := clampInt(avail-stateW-cpuW-memW-ageW-2, 20, 60)
	*out = append(*out, detailLine{spans: []span{
		sp(padTo("namespace/name", nameW), t.Dim),
		sp(padTo("state", stateW), t.Dim),
		sp(padTo("cpu", cpuW), t.Dim),
		sp(padTo("mem", memW), t.Dim),
		sp(padTo("age", ageW), t.Dim),
	}})
	for _, p := range pods {
		state := strings.ToLower(p.Phase.String())
		if p.DaemonSet {
			state += " ds"
		}
		age := ""
		if !p.Created.IsZero() {
			age = model.HumanAge(now.Sub(p.Created))
		}
		*out = append(*out, detailLine{spans: []span{
			sp(padTo(shorten(p.Namespace+"/"+p.Name, nameW-1), nameW), t.Fg),
			sp(padTo(state, stateW), stateColor(strings.ToLower(p.Phase.String()))),
			sp(padTo(model.HumanCPU(p.Requests.CPUMilli), cpuW), t.Dim),
			sp(padTo(model.HumanMem(p.Requests.MemBytes), memW), t.Dim),
			sp(padTo(age, ageW), t.Dim),
		}})
	}
}

// appendEvents is the point of the pane: the node's history, newest first.
func appendEvents(out *[]detailLine, det *model.NodeDetail, avail int, now time.Time, historical bool) {
	t := theme.Current
	add := func(spans ...span) { *out = append(*out, detailLine{spans: spans}) }
	events := det.Events
	if historical {
		events = make([]model.Event, 0, len(det.Events))
		for _, ev := range det.Events {
			if !ev.When().After(now) {
				events = append(events, ev)
			}
		}
	}
	*out = append(*out, detailLine{}, detailLine{spans: []span{
		spb(fmt.Sprintf("EVENTS (%d)", len(events)), t.Accent),
		sp("  newest first", t.Dim)}})

	if det.EventsErr != "" {
		for _, line := range wrapText("could not read events: "+det.EventsErr, avail) {
			add(sp(line, t.Err))
		}
	}
	if det.EventsCapped {
		add(sp("the event list was truncated by the API server — older events are missing", t.Warn))
	}
	if len(events) == 0 {
		if det.EventsErr == "" {
			add(sp("none — the API server only keeps events for about an hour", t.Dim))
		}
		return
	}

	const ageW, clockW, reasonW = 8, 10, 24
	// Who reported the event is the first column to go when space runs short: it is
	// the least of the four, and the message is the most.
	compW := 0
	if avail >= 92 {
		compW = 20
	}
	msgX := ageW + clockW + reasonW + compW
	msgW := avail - msgX

	for _, ev := range events {
		reason, col := "  "+ev.Reason, t.Fg
		if ev.Warning() {
			// The marker matters as much as the colour: a warning has to be
			// findable while scrolling past on a washed-out projector.
			reason, col = "! "+ev.Reason, t.Warn
		}
		from := ev.Component
		if ev.Kind == "NodeClaim" {
			from = joinParts(" ", from, "(claim)")
		}
		msg := ev.Message
		if ev.Count > 1 {
			msg += fmt.Sprintf("  ×%d, first %s ago", ev.Count, model.HumanAge(now.Sub(ev.First)))
		}

		spans := []span{
			sp(padTo(model.HumanAge(now.Sub(ev.When()))+" ago", ageW), t.Dim),
			sp(padTo(ev.When().Local().Format("15:04:05"), clockW), t.Dim),
			spb(padTo(reason, reasonW), col),
		}
		if compW > 0 {
			spans = append(spans, sp(padTo(from, compW), t.Dim))
		}

		if msgW >= 24 {
			lines := wrapText(msg, msgW)
			add(append(spans, sp(lines[0], t.Fg))...)
			for _, cont := range lines[1:] {
				add(sp(spaces(msgX), t.Dim), sp(cont, t.Dim))
			}
			continue
		}
		// Too narrow for a message column: the header row carries the identity and
		// the message wraps underneath it, indented.
		add(spans...)
		for _, cont := range wrapText(msg, avail-4) {
			add(sp(spaces(4)+cont, t.Fg))
		}
	}
}

// --- rendering ---

// renderDetail draws the pane as a full screen: a title strip, the scrolling
// body, and a footer. It is a full replacement rather than an overlay so that
// layoutFrame — and therefore the grid's geometry, pan and zoom — is not
// consulted at all while the pane is up and cannot be perturbed by it.
func (m *Model) renderDetail(w, h int) string {
	t := theme.Current
	c := newCanvas(w, h, t.Bg, t.Fg)
	d := m.detail
	if d == nil {
		return c.String()
	}
	node := m.snapNode(d.name)

	// Title strip. A claim is labelled as one: the name in it is the claim's, not
	// a node's, and reading it as a node name is how you end up looking for a
	// machine that does not exist yet.
	label := "node"
	if d.describingClaim() {
		label = "claim"
	}
	c.rect(0, 0, w, 1, t.Panel)
	c.text(1, 0, label, t.Accent, true)
	x := 2 + runewidth.StringWidth(label)
	c.text(x, 0, truncHead(d.name, max(0, w-x-30)), t.PanelFg, true)
	x += runewidth.StringWidth(truncHead(d.name, max(0, w-x-30))) + 2
	if node != nil {
		label := phaseChipLabel(node.Phase)
		chipW := runewidth.StringWidth(label) + 2
		if x+chipW < w-24 {
			col := t.PhaseColor(int(node.Phase))
			c.rect(x, 0, chipW, 1, col)
			c.text(x, 0, " "+label+" ", contrastOn(col), true)
		}
	} else {
		c.text(x, 0, "removed", t.Dim, true)
	}
	c.textRight(0, 0, w-1, "↑↓ scroll · ctrl+r refresh · esc back ", t.Dim, false)

	// Body.
	lines := m.detailLines(w)
	rows := m.detailBodyRows()
	scroll := clampInt(d.scroll, 0, max(0, len(lines)-rows))
	for i := 0; i < rows && scroll+i < len(lines); i++ {
		y := 1 + i
		if y >= h-1 {
			break
		}
		lx := 2
		for _, s := range lines[scroll+i].spans {
			if lx >= w-1 {
				break
			}
			text := truncHead(s.text, w-1-lx)
			c.text(lx, y, text, s.col, s.bold)
			lx += runewidth.StringWidth(text)
		}
	}

	// Footer.
	if h >= 2 {
		y := h - 1
		c.rect(0, y, w, 1, t.Panel)
		status, col := "", t.Dim
		switch {
		case d.loading && d.detail == nil:
			status = "reading…"
		case d.err != nil:
			status = "detail unavailable"
			col = t.Err
		case d.detail != nil:
			if d.historical {
				status = fmt.Sprintf("%d events · playback sample %s ago", len(d.detail.Events),
					model.HumanAge(m.displayNow().Sub(d.sampleAt)))
			} else {
				status = fmt.Sprintf("%d events · read %s ago", len(d.detail.Events),
					model.HumanAge(time.Since(d.fetchedAt)))
			}
		}
		c.text(1, y, truncHead(status, max(0, w/2)), col, false)

		pos := "all"
		if len(lines) > rows {
			pos = fmt.Sprintf("%d–%d of %d", scroll+1, min(scroll+rows, len(lines)), len(lines))
		}
		c.textRight(0, y, w-1, pos+" · esc or backspace returns to the grid ", t.Dim, false)
	}
	return c.String()
}

// meterSpans renders a fill bar as text, so it can sit in a line of spans
// alongside the numbers instead of needing its own canvas pass.
// Four of these can end up stacked (requests and usage, cpu and mem), so they
// are half-height rails for the same reason the header meters are: full-height
// blocks on adjacent rows merge into one shape.
func meterSpans(frac float64, w int) []span {
	t := theme.Current
	full := clampInt(int(clamp01(frac)*float64(w)+0.5), 0, w)
	rail := string(railCell)
	return []span{
		{text: strings.Repeat(rail, full), col: t.Util(clamp01(frac))},
		{text: strings.Repeat(rail, w-full), col: theme.Mix(t.Dim, t.Bg, 0.55)},
	}
}

// --- small helpers ---

// padTo trims or pads s to exactly n cells so that columns line up without
// every caller measuring for itself.
func padTo(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = truncHead(s, n)
	return s + spaces(n-runewidth.StringWidth(s))
}

// wrapText breaks s into lines of at most w cells, on spaces where it can and
// mid-word where it must. It always returns at least one line, so callers can
// index [0] for the first.
func wrapText(s string, w int) []string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if w <= 0 {
		return []string{""}
	}
	var out []string
	for len(s) > 0 {
		if runewidth.StringWidth(s) <= w {
			out = append(out, s)
			break
		}
		cut := runewidth.Truncate(s, w, "")
		if i := strings.LastIndexByte(cut, ' '); i > w/3 {
			cut = cut[:i]
		}
		out = append(out, strings.TrimRight(cut, " "))
		s = strings.TrimLeft(s[len(cut):], " ")
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

// joinParts joins the non-empty parts, which is most of what building a
// one-line summary of optional fields consists of.
func joinParts(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

func claimLabel(claim string) string {
	if claim == "" {
		return ""
	}
	return "nodeclaim " + claim
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
