package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// stubDescriber stands in for a source. The fetch is a bubbletea command, so the
// tests run it themselves and feed the result back through Update, which is
// exactly what the runtime does.
type stubDescriber struct {
	calls  int
	detail *model.NodeDetail
	err    error
}

func (s *stubDescriber) DescribeNode(_ context.Context, name, _ string) (*model.NodeDetail, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	d := *s.detail
	d.Name = name
	return &d, nil
}

// agedTestModel is newTestModel with every node's birth pushed back an hour, so
// the rendered ages read in whole hours and two frames captured a moment apart
// are byte-identical.
func agedTestModel(t *testing.T, w, h, nodes int) *Model {
	t.Helper()
	snap := testSnapshot(nodes)
	for _, n := range snap.Nodes {
		n.Created = n.Created.Add(-2 * time.Hour)
		for _, p := range n.Pods {
			// The fixture leaves pod creation zero; real pods always carry one, and
			// shifting the zero time would render an age in millennia.
			p.Created = time.Now().Add(-2 * time.Hour)
		}
	}
	m := New(Config{FPS: 20, Legend: true})
	m.w, m.h = w, h
	m.applySnapshot(snap)
	return m
}

// sampleDetail is a describe payload with events deliberately out of order, so
// the pane's own ordering is what any assertion about order is measuring. The
// names are ages, not positions: FirstEvent is the oldest, and therefore the one
// that must render last.
func sampleDetail(name string) *model.NodeDetail {
	base := time.Now().Add(-90 * time.Minute)
	return &model.NodeDetail{
		Name:        name,
		FetchedAt:   time.Now(),
		Capacity:    model.Resources{CPUMilli: 16000, MemBytes: 64 << 30, Pods: 110},
		Allocatable: model.Resources{CPUMilli: 15800, MemBytes: 63 << 30, Pods: 110},
		Labels:      map[string]string{"karpenter.sh/nodepool": "general", "kubernetes.io/arch": "amd64"},
		Annotations: map[string]string{"karpenter.oxide.computer/hourly-price": "0.768"},
		System: model.SystemInfo{
			OSImage: "Amazon Linux 2", Kernel: "5.10.219", ContainerRuntime: "containerd://1.7.11",
			Kubelet: "v1.30.4", OS: "linux", Arch: "amd64",
		},
		Addresses: []model.Address{{Type: "InternalIP", Address: "10.0.1.23"}},
		Conditions: []model.Condition{
			{Type: "Ready", Status: "True", Reason: "KubeletReady", Message: "kubelet is posting ready status", Changed: base},
			{Type: "MemoryPressure", Status: "False", Reason: "KubeletHasSufficientMemory", Changed: base},
		},
		Taints: []model.Taint{{Key: "karpenter.sh/disrupted", Value: "underutilized", Effect: "NoSchedule"}},
		Events: []model.Event{
			{Kind: "Node", Object: name, Type: "Normal", Reason: "ThirdEvent", Component: "kubelet",
				Message: "third", Count: 1, First: base.Add(40 * time.Minute), Last: base.Add(40 * time.Minute)},
			{Kind: "NodeClaim", Object: "general-abc12", Type: "Normal", Reason: "FirstEvent", Component: "karpenter",
				Message: "a long launch message that is likely to need wrapping on a narrow terminal, several times over",
				Count:   1, First: base, Last: base},
			{Kind: "Node", Object: name, Type: "Warning", Reason: "SecondEvent", Component: "kubelet",
				Message: "second", Count: 4, First: base.Add(10 * time.Minute), Last: base.Add(20 * time.Minute)},
		},
	}
}

// openPane presses enter and delivers the fetch, returning the pane ready to read.
func openPane(t *testing.T, m *Model) {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.detail == nil {
		t.Fatal("enter did not open the detail pane")
	}
	if cmd == nil {
		t.Fatal("enter did not start a fetch")
	}
	msg := cmd()
	if _, ok := msg.(detailMsg); !ok {
		t.Fatalf("fetch produced %T, want detailMsg", msg)
	}
	m.Update(msg)
}

func TestDelayedDetailUsesHistoryOrLabelsLiveFallback(t *testing.T) {
	m := agedTestModel(t, 150, 44, 3)
	m.describe = &stubDescriber{detail: sampleDetail("")}
	if err := m.setPlaybackSpeed(.5); err != nil {
		t.Fatal(err)
	}
	n := m.selected().node
	at := m.displayNow().Add(-time.Second)
	detail := sampleDetail(n.Name)
	identity := detailIdentity(n.Name, n.NodeClaim, n.ProviderID)
	m.detailHistory[identity] = []detailSample{{at: at, detail: detail}}

	if cmd := m.openDetail(); cmd != nil {
		t.Fatal("historical detail should not issue a live fetch")
	}
	if !m.detail.historical || m.detail.liveFallback || m.detail.detail != detail {
		t.Fatalf("history not selected: historical=%v fallback=%v", m.detail.historical, m.detail.liveFallback)
	}
	m.closeDetail()
	m.detailHistory = map[string][]detailSample{}

	cmd := m.openDetail()
	if cmd == nil || !m.detail.liveFallback {
		t.Fatal("missing history should fetch and label live detail")
	}
	m.Update(cmd())
	if out := m.View(); !strings.Contains(out, "LIVE DETAIL") {
		t.Fatalf("live fallback warning missing\n%s", out)
	}
}

func TestDetailPaneShowsEventsChronologically(t *testing.T) {
	m := agedTestModel(t, 150, 44, 12)
	m.describe = &stubDescriber{detail: sampleDetail("")}
	m.setCursor(3)
	openPane(t, m)

	out := m.View()
	first, second, third := strings.Index(out, "FirstEvent"), strings.Index(out, "SecondEvent"), strings.Index(out, "ThirdEvent")
	if first < 0 || second < 0 || third < 0 {
		t.Fatalf("events missing from the pane: %d %d %d\n%s", first, second, third, out)
	}
	// Newest at the top: what the node just did is the first line of the section.
	if !(third < second && second < first) {
		t.Fatalf("events not newest-first: FirstEvent@%d SecondEvent@%d ThirdEvent@%d", first, second, third)
	}
	// The pane is a describe, not just an event log.
	for _, want := range []string{"CONDITIONS", "TAINTS", "EVENTS", "PODS", "KubeletReady",
		"karpenter.sh/disrupted=underutilized:NoSchedule", m.cursorName} {
		if !strings.Contains(out, want) {
			t.Fatalf("pane is missing %q\n%s", want, out)
		}
	}
	// A repeated event has to say so, or four occurrences read as one.
	if !strings.Contains(out, "×4") {
		t.Fatalf("repeat count missing\n%s", out)
	}
}

// TestDetailPaneRestoresGridExactly is the whole point of the pane being a
// separate screen rather than a mode: leaving it must not disturb one cell.
func TestDetailPaneRestoresGridExactly(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyBackspace}} {
		m := agedTestModel(t, 150, 44, 24)
		m.describe = &stubDescriber{detail: sampleDetail("")}

		if _, err := m.Run(":nodepool general"); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Run(":node ip-10"); err != nil {
			t.Fatal(err)
		}
		m.sortKey, m.sortDesc = SortCPU, true
		m.derive()
		m.setCursor(5)
		m.zoomBy(keyZoomStep)
		m.scrollBy(1)

		want := *m
		before := m.View()

		openPane(t, m)
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m.Update(key)

		if m.detail != nil {
			t.Fatalf("%v did not close the pane", key)
		}
		if after := m.View(); after != before {
			t.Fatalf("%v: grid did not come back identical\nbefore:\n%s\nafter:\n%s", key, before, after)
		}
		if got, expect := m.filter.Describe(), want.filter.Describe(); !equalStrings(got, expect) {
			t.Fatalf("%v: filters changed: %v want %v", key, got, expect)
		}
		if m.panX != want.panX || m.panY != want.panY || m.zoom != want.zoom || m.zoomCols != want.zoomCols {
			t.Fatalf("%v: placement changed: pan %d,%d zoom %d/%d want pan %d,%d zoom %d/%d", key,
				m.panX, m.panY, m.zoom, m.zoomCols, want.panX, want.panY, want.zoom, want.zoomCols)
		}
		if m.cursor != want.cursor || m.cursorName != want.cursorName {
			t.Fatalf("%v: selection moved from %d(%s) to %d(%s)", key,
				want.cursor, want.cursorName, m.cursor, m.cursorName)
		}
		if m.sortKey != want.sortKey || m.sortDesc != want.sortDesc || m.mode != want.mode {
			t.Fatalf("%v: view state changed", key)
		}
	}
}

func TestDetailPaneFrameGeometry(t *testing.T) {
	sizes := []struct{ w, h int }{{200, 60}, {150, 44}, {120, 40}, {80, 24}, {60, 16}, {30, 8}, {20, 4}}
	for _, size := range sizes {
		// Loaded, loading and failed all have to produce a rigid frame.
		loaded := agedTestModel(t, size.w, size.h, 8)
		loaded.describe = &stubDescriber{detail: sampleDetail("")}
		openPane(t, loaded)
		assertFrame(t, loaded, size.w, size.h, loaded.mode, 8, "detail loaded")

		for i := 0; i < 3; i++ {
			loaded.Update(tea.KeyMsg{Type: tea.KeyPgDown})
			assertFrame(t, loaded, size.w, size.h, loaded.mode, 8, "detail scrolled")
		}

		loading := agedTestModel(t, size.w, size.h, 8)
		loading.describe = &stubDescriber{detail: sampleDetail("")}
		loading.Update(tea.KeyMsg{Type: tea.KeyEnter})
		assertFrame(t, loading, size.w, size.h, loading.mode, 8, "detail loading")

		failed := agedTestModel(t, size.w, size.h, 8)
		failed.describe = &stubDescriber{err: errors.New("nodes.v1 is forbidden: user cannot get resource nodes")}
		failed.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd := failed.fetchDetail(failed.detail.name, ""); cmd != nil {
			failed.Update(cmd())
		}
		assertFrame(t, failed, size.w, size.h, failed.mode, 8, "detail failed")
	}
}

// TestDetailPaneWithoutDescriber covers the no-source case: the pane still shows
// what the snapshot knows and says why the events are missing.
func TestDetailPaneWithoutDescriber(t *testing.T) {
	m := agedTestModel(t, 140, 40, 6)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.detail == nil {
		t.Fatal("pane did not open without a describe source")
	}
	out := m.View()
	if !strings.Contains(out, "no describe source") {
		t.Fatalf("pane did not explain the missing source\n%s", out)
	}
	assertFrame(t, m, 140, 40, m.mode, 6, "no describer")
}

func TestDetailFetchIgnoresStaleResult(t *testing.T) {
	m := agedTestModel(t, 150, 44, 12)
	m.describe = &stubDescriber{detail: sampleDetail("")}
	m.setCursor(0)
	first := m.vis[0].node.Name

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.setCursor(1)
	second := m.vis[1].node.Name
	if first == second {
		t.Fatal("test needs two distinct nodes")
	}
	openPane(t, m)

	// The abandoned pane's fetch lands late. It must not overwrite the live one.
	m.Update(detailMsg{name: first, detail: sampleDetail(first)})
	if m.detail.detail.Name != second {
		t.Fatalf("stale fetch for %s overwrote the pane for %s", first, second)
	}
}

func TestDetailRefreshesWhileOpen(t *testing.T) {
	m := agedTestModel(t, 150, 44, 12)
	stub := &stubDescriber{detail: sampleDetail("")}
	m.describe = stub
	openPane(t, m)
	if stub.calls != 1 {
		t.Fatalf("expected one fetch on open, got %d", stub.calls)
	}

	// A frame straight after a read must not re-read: the pane would hammer the
	// API server at the frame rate.
	if cmd := m.refreshDetail(time.Now()); cmd != nil {
		t.Fatal("pane refreshed immediately after loading")
	}
	m.detail.fetchedAt = time.Now().Add(-2 * detailRefresh)
	cmd := m.refreshDetail(time.Now())
	if cmd == nil {
		t.Fatal("stale pane did not refresh")
	}
	m.Update(cmd())
	if stub.calls != 2 {
		t.Fatalf("expected a second fetch, got %d", stub.calls)
	}
	// And a closed pane never refreshes.
	m.closeDetail()
	if cmd := m.refreshDetail(time.Now()); cmd != nil {
		t.Fatal("closed pane still refreshing")
	}
}

func TestDetailScrollClamps(t *testing.T) {
	m := agedTestModel(t, 150, 20, 12)
	m.describe = &stubDescriber{detail: sampleDetail("")}
	openPane(t, m)

	maxScroll := m.detailMaxScroll()
	if maxScroll == 0 {
		t.Fatal("test needs content taller than the pane")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.detail.scroll != maxScroll {
		t.Fatalf("G scrolled to %d, want %d", m.detail.scroll, maxScroll)
	}
	for i := 0; i < 10; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if m.detail.scroll != maxScroll {
		t.Fatalf("scrolled past the end to %d, want %d", m.detail.scroll, maxScroll)
	}
	for i := 0; i < 40; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if m.detail.scroll != 0 {
		t.Fatalf("scrolled above the top to %d", m.detail.scroll)
	}
	// The wheel scrolls the text rather than zooming a grid that is not on screen.
	zoom := m.zoom
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.detail.scroll != detailWheelRows {
		t.Fatalf("wheel scrolled to %d, want %d", m.detail.scroll, detailWheelRows)
	}
	if m.zoom != zoom {
		t.Fatalf("wheel zoomed the hidden grid: %d -> %d", zoom, m.zoom)
	}
}

// TestDetailPaneSurvivesNodeLeavingCluster covers reading about a node that gets
// deleted while the pane is open — routine on a Karpenter cluster.
func TestDetailPaneSurvivesNodeLeavingCluster(t *testing.T) {
	m := agedTestModel(t, 150, 44, 12)
	m.describe = &stubDescriber{detail: sampleDetail("")}
	openPane(t, m)

	gone := m.detail.name
	snap := testSnapshot(12)
	kept := snap.Nodes[:0]
	for _, n := range snap.Nodes {
		if n.Name != gone {
			kept = append(kept, n)
		}
	}
	snap.Nodes = kept
	m.applySnapshot(snap)

	out := m.View()
	if !strings.Contains(out, "left the cluster") {
		t.Fatalf("pane did not report the node leaving\n%s", out)
	}
	assertFrame(t, m, 150, 44, m.mode, 11, "node gone")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWrapTextFitsWidth(t *testing.T) {
	long := "Disrupting NodeClaim: Underutilized/Delete, terminating 1 nodes (12 pods) " +
		"ip-10-0-1-23/m5.4xlarge/spot"
	for _, w := range []int{10, 24, 40, 200} {
		lines := wrapText(long, w)
		if len(lines) == 0 {
			t.Fatalf("width %d: no lines", w)
		}
		for _, line := range lines {
			if got := len([]rune(line)); got > w {
				t.Fatalf("width %d: line of %d runes: %q", w, got, line)
			}
		}
		// Nothing may be dropped. Spaces are exempt: a width too narrow for a word
		// has to break it, and that inserts a line boundary mid-word.
		strip := func(s string) string { return strings.ReplaceAll(s, " ", "") }
		if got := strip(strings.Join(lines, "")); got != strip(long) {
			t.Fatalf("width %d: text lost in wrapping: %q", w, got)
		}
	}
}
