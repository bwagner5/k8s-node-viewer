package ui

import (
	"context"
	"reflect"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

const (
	detailHistoryInterval    = 5 * time.Second
	detailHistoryConcurrency = 4
)

type detailSample struct {
	at     time.Time
	detail *model.NodeDetail
	bytes  int64
}

type detailRequest struct {
	identity string
	name     string
	claim    string
}

type detailCaptureResult struct {
	identity string
	detail   *model.NodeDetail
	err      error
}

type detailHistoryMsg struct {
	at      time.Time
	epoch   uint64
	results []detailCaptureResult
}

// captureDetailHistory samples every live node while playback is delayed. It
// uses the existing bounded Describe path and a small worker pool, so a twenty
// node demo does not become a twenty-request burst against the API server.
func (m *Model) captureDetailHistory(now time.Time) tea.Cmd {
	captureDetails := m.cfg.CaptureDetails && (m.cfg.Recording == nil || m.recordingActive())
	if (m.playback.live && !captureDetails) || m.describe == nil ||
		(m.detailHistoryExhausted && !captureDetails) || m.detailCapturing ||
		(!m.detailCaptureAt.IsZero() && now.Sub(m.detailCaptureAt) < detailHistoryInterval) {
		return nil
	}
	snap := m.playback.latest
	if snap == nil {
		return nil
	}
	requests := make([]detailRequest, 0, len(snap.Nodes))
	seen := map[string]bool{}
	for _, n := range snap.Nodes {
		if n == nil || n.Phase == model.PhaseGone {
			continue
		}
		identity := detailIdentity(n.Name, n.NodeClaim, n.ProviderID)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		requests = append(requests, detailRequest{identity: identity, name: n.Name, claim: n.NodeClaim})
	}
	if len(requests) == 0 {
		return nil
	}
	m.detailCapturing = true
	m.detailCaptureAt = now
	epoch := m.detailHistoryEpoch
	src := m.describe
	return func() tea.Msg {
		jobs := make(chan detailRequest)
		results := make(chan detailCaptureResult, len(requests))
		workers := min(detailHistoryConcurrency, len(requests))
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer wg.Done()
				for req := range jobs {
					ctx, cancel := context.WithTimeout(context.Background(), detailTimeout)
					detail, err := src.DescribeNode(ctx, req.name, req.claim)
					cancel()
					results <- detailCaptureResult{identity: req.identity, detail: detail, err: err}
				}
			}()
		}
		go func() {
			for _, req := range requests {
				jobs <- req
			}
			close(jobs)
			wg.Wait()
			close(results)
		}()
		out := detailHistoryMsg{at: now, epoch: epoch, results: make([]detailCaptureResult, 0, len(requests))}
		for result := range results {
			out.results = append(out.results, result)
		}
		return out
	}
}

func (m *Model) applyDetailHistory(msg detailHistoryMsg) {
	m.detailCapturing = false
	if msg.epoch != m.detailHistoryEpoch || m.playback.live {
		return
	}
	for _, result := range msg.results {
		if result.err != nil || result.detail == nil {
			continue
		}
		if m.detailHistoryExhausted {
			// A recording wrapper has already persisted the result. Keep sampling
			// for the file without trying to grow the exhausted UI buffer.
			continue
		}
		model.SortEvents(result.detail.Events)
		history := m.detailHistory[result.identity]
		if len(history) > 0 && equalNodeDetail(history[len(history)-1].detail, result.detail) {
			continue
		}
		sampleAt := result.detail.FetchedAt
		if sampleAt.IsZero() {
			sampleAt = msg.at
		}
		sample := detailSample{at: sampleAt, detail: result.detail, bytes: estimateDetailBytes(result.detail)}
		if m.detailHistoryBytes+sample.bytes+m.playback.playbackBytes() > m.playback.maxBytes {
			m.clearDetailHistory()
			m.detailHistoryExhausted = true
			if m.detail != nil {
				m.detail.liveFallback, m.detail.forceLive, m.detail.historical = true, true, false
				m.detail.fetchedAt = time.Time{}
			}
			m.notify("node detail history limit reached — detail views are live", true)
			return
		}
		m.detailHistory[result.identity] = append(history, sample)
		m.detailHistoryBytes += sample.bytes
	}
	m.pruneDetailHistory()
	m.refreshHistoricalDetail()
}

// playbackBytes is kept as a method to make the shared budget explicit at the
// call site without exposing playback's queue representation.
func (p *playback) playbackBytes() int64 { return p.bytes }

func (m *Model) historicalDetail(name, claim, providerID string) (*model.NodeDetail, time.Time, bool) {
	identity := detailIdentity(name, claim, providerID)
	history := m.detailHistory[identity]
	now := m.displayNow()
	for i := len(history) - 1; i >= 0; i-- {
		if !history[i].at.After(now) {
			return history[i].detail, history[i].at, true
		}
	}
	return nil, time.Time{}, false
}

func (m *Model) refreshHistoricalDetail() {
	if m.playback.live || m.detail == nil || m.detail.forceLive {
		return
	}
	d := m.detail
	detail, at, ok := m.historicalDetail(d.name, d.claim, d.providerID)
	if !ok || (!d.sampleAt.IsZero() && !at.After(d.sampleAt)) {
		return
	}
	d.detail, d.sampleAt, d.historical, d.liveFallback = detail, at, true, false
	d.err, d.loading = nil, false
	d.scroll = clampInt(d.scroll, 0, m.detailMaxScroll())
}

func (m *Model) pruneDetailHistory() {
	if m.playback.archive {
		return
	}
	cutoff := m.displayNow()
	for identity, history := range m.detailHistory {
		keep := 0
		for i := range history {
			if !history[i].at.After(cutoff) {
				keep = i
			}
		}
		if keep == 0 {
			continue
		}
		for i := 0; i < keep; i++ {
			m.detailHistoryBytes -= history[i].bytes
		}
		m.detailHistory[identity] = append([]detailSample(nil), history[keep:]...)
	}
}

func (m *Model) clearDetailHistory() {
	m.detailHistoryEpoch++
	m.detailHistory = map[string][]detailSample{}
	m.detailHistoryBytes = 0
	m.detailCapturing = false
	m.detailCaptureAt = time.Time{}
}

func (m *Model) seedOpenDetailHistory(now time.Time) {
	d := m.detail
	if d == nil || d.detail == nil {
		return
	}
	identity := detailIdentity(d.name, d.claim, d.providerID)
	sampleAt := d.detail.FetchedAt
	if sampleAt.IsZero() {
		sampleAt = d.fetchedAt
	}
	if sampleAt.IsZero() {
		sampleAt = now
	}
	sample := detailSample{at: sampleAt, detail: d.detail, bytes: estimateDetailBytes(d.detail)}
	m.detailHistory[identity] = []detailSample{sample}
	m.detailHistoryBytes = sample.bytes
	d.sampleAt, d.historical, d.liveFallback, d.forceLive = sampleAt, true, false, false
}

func detailIdentity(name, claim, providerID string) string {
	switch {
	case providerID != "":
		return "provider:" + providerID
	case claim != "":
		return "claim:" + claim
	default:
		return "node:" + name
	}
}

func equalNodeDetail(a, b *model.NodeDetail) bool {
	if a == nil || b == nil {
		return a == b
	}
	ac, bc := *a, *b
	ac.FetchedAt, bc.FetchedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(ac, bc)
}

func estimateDetailBytes(d *model.NodeDetail) int64 {
	if d == nil {
		return 0
	}
	n := int64(1024) + stringBytes(d.Name, d.Kind, d.ProviderID, d.EventsErr,
		d.System.OSImage, d.System.Kernel, d.System.ContainerRuntime, d.System.Kubelet,
		d.System.KubeProxy, d.System.OS, d.System.Arch)
	for _, c := range d.Conditions {
		n += 160 + stringBytes(c.Type, c.Status, c.Reason, c.Message)
	}
	for _, t := range d.Taints {
		n += 96 + stringBytes(t.Key, t.Value, t.Effect)
	}
	for _, a := range d.Addresses {
		n += 64 + stringBytes(a.Type, a.Address)
	}
	for k, v := range d.Labels {
		n += 48 + int64(len(k)+len(v))
	}
	for k, v := range d.Annotations {
		n += 48 + int64(len(k)+len(v))
	}
	for _, e := range d.Events {
		n += 192 + stringBytes(e.Kind, e.Object, e.Type, e.Reason, e.Component, e.Message)
	}
	return n
}
