package ui

import (
	"fmt"
	"math"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

const (
	defaultHistoryDuration = 10 * time.Minute
	defaultHistoryMemory   = int64(256 << 20)
	defaultRewindDuration  = 30 * time.Second
)

// playback is the cluster-time clock, its short displayed-history ring, and its
// not-yet-shown snapshot queue.
//
// Live is deliberately a mode rather than another spelling of speed 1. A
// delayed stream may be played at 1x forever while remaining, say, forty
// seconds behind the cluster. Only GoLive discards that history and catches up.
type playback struct {
	live        bool
	archive     bool
	archiveEnd  time.Time
	speed       float64
	resumeSpeed float64
	now         time.Time
	latest      *model.Snapshot
	history     []*model.Snapshot
	queue       []*model.Snapshot
	bytes       int64
	maxAge      time.Duration
	maxBytes    int64
}

func newPlayback(initialSpeed float64, maxAge time.Duration, maxBytes int64) *playback {
	return newPlaybackMode(initialSpeed, maxAge, maxBytes, false)
}

func newPlaybackMode(initialSpeed float64, maxAge time.Duration, maxBytes int64, archive bool) *playback {
	if maxAge <= 0 {
		maxAge = defaultHistoryDuration
	}
	if maxBytes <= 0 {
		maxBytes = defaultHistoryMemory
	}
	p := &playback{live: !archive, archive: archive, speed: 1, resumeSpeed: 1, maxAge: maxAge, maxBytes: maxBytes}
	if archive {
		if initialSpeed >= 0 && initialSpeed <= 16 {
			p.speed = initialSpeed
			if initialSpeed > 0 {
				p.resumeSpeed = initialSpeed
			}
		}
		return p
	}
	if initialSpeed >= 0 && initialSpeed < 1 {
		p.live = false
		p.speed = initialSpeed
		if initialSpeed > 0 {
			p.resumeSpeed = initialSpeed
		}
	}
	return p
}

// Ingest accepts source snapshots without back-pressuring the source. The
// first delayed snapshot is displayed immediately and becomes the playback
// epoch; subsequent snapshots wait for cluster time to reach their timestamp.
func (p *playback) Ingest(snap *model.Snapshot, wallNow time.Time) []*model.Snapshot {
	if snap == nil {
		return nil
	}
	first := p.latest == nil
	p.latest = snap
	if p.live {
		p.now = wallNow
		p.remember(snap, wallNow)
		return []*model.Snapshot{snap}
	}
	if first || p.now.IsZero() {
		p.now = snapshotTime(snap, wallNow)
		p.remember(snap, wallNow)
		return []*model.Snapshot{snap}
	}
	p.queue = append(p.queue, snap)
	p.bytes += estimateSnapshotBytes(snap)
	return nil
}

// Advance moves cluster time and releases all snapshots now due. UI time is
// deliberately not represented here: key handling and status messages continue
// to run on wall time even while cluster time is paused.
func (p *playback) Advance(wallDT time.Duration, wallNow time.Time) []*model.Snapshot {
	if p.live {
		p.now = wallNow
		return nil
	}
	if wallDT > 0 && p.speed > 0 {
		p.now = p.now.Add(time.Duration(float64(wallDT) * p.speed))
		if !p.archive && p.now.After(wallNow) {
			p.now = wallNow
		}
		if p.archive && p.latest != nil {
			end := p.timelineEnd(p.now)
			if p.now.After(end) {
				p.now = end
			}
		}
	}

	n := 0
	for n < len(p.queue) && !snapshotTime(p.queue[n], wallNow).After(p.now) {
		n++
	}
	if n == 0 {
		return nil
	}
	due := append([]*model.Snapshot(nil), p.queue[:n]...)
	copy(p.queue, p.queue[n:])
	p.queue = p.queue[:len(p.queue)-n]
	// Moving a snapshot from the future queue into displayed history does not
	// change the retained byte count. History is what makes a second rewind
	// possible after part of the backlog has already been replayed.
	p.history = append(p.history, due...)
	p.pruneHistory(wallNow)
	return due
}

// Rewind moves the display clock backward and rebuilds the future queue from
// snapshots that were already shown. It preserves the current speed, so the
// natural sequence is p, [, p: pause, step back, then resume. Rewinding from
// realtime starts a delayed 1x timeline instead.
func (p *playback) Rewind(amount time.Duration, wallNow time.Time) (*model.Snapshot, time.Duration) {
	if amount <= 0 || len(p.history) == 0 {
		return nil, 0
	}
	from := p.DisplayNow(wallNow)
	want := from.Add(-amount)

	// A snapshot is a complete state, so use the newest one at or before the
	// desired time. If the request reaches beyond the retained window, land on
	// its oldest reconstructable state.
	target := 0
	for i := len(p.history) - 1; i >= 0; i-- {
		if !snapshotTime(p.history[i], wallNow).After(want) {
			target = i
			break
		}
	}
	targetTime := want
	oldestTime := snapshotTime(p.history[0], wallNow)
	if targetTime.Before(oldestTime) {
		targetTime = oldestTime
	}
	if !targetTime.Before(from) {
		return nil, 0
	}

	// Everything displayed after the target becomes future work again. Existing
	// queued snapshots arrived while playback was already delayed and follow it.
	future := append([]*model.Snapshot(nil), p.history[target+1:]...)
	future = append(future, p.queue...)
	p.queue = future
	p.history = p.history[:target+1]
	p.live = false
	p.now = targetTime
	return p.history[target], from.Sub(targetTime)
}

// Forward seeks toward the freshest buffered snapshot without changing the
// current rate or pause state. It returns the newest crossed snapshot; a seek is
// applied atomically by the UI rather than animating every intermediate frame.
func (p *playback) Forward(amount time.Duration, wallNow time.Time) (*model.Snapshot, time.Duration) {
	if amount <= 0 || p.live || p.latest == nil {
		return nil, 0
	}
	from := p.DisplayNow(wallNow)
	end := p.timelineEnd(from)
	if !end.After(from) {
		return nil, 0
	}
	targetTime := from.Add(amount)
	if targetTime.After(end) {
		targetTime = end
	}
	n := 0
	for n < len(p.queue) && !snapshotTime(p.queue[n], wallNow).After(targetTime) {
		n++
	}
	var target *model.Snapshot
	if n > 0 {
		target = p.queue[n-1]
		p.history = append(p.history, p.queue[:n]...)
		copy(p.queue, p.queue[n:])
		p.queue = p.queue[:len(p.queue)-n]
		p.pruneHistory(wallNow)
	}
	p.now = targetTime
	return target, targetTime.Sub(from)
}

// remember retains a displayed snapshot in the short rolling rewind window.
func (p *playback) remember(snap *model.Snapshot, wallNow time.Time) {
	if snap == nil {
		return
	}
	p.history = append(p.history, snap)
	p.bytes += estimateSnapshotBytes(snap)
	p.pruneHistory(wallNow)
}

func (p *playback) pruneHistory(wallNow time.Time) {
	if len(p.history) == 0 || p.archive {
		return
	}
	window := defaultRewindDuration
	if p.maxAge > 0 && p.maxAge < window {
		window = p.maxAge
	}
	cutoff := p.DisplayNow(wallNow).Add(-window)

	// Keep the newest snapshot at or before the cutoff as the full-state anchor,
	// plus every snapshot after it.
	drop := 0
	for drop+1 < len(p.history) && !snapshotTime(p.history[drop+1], wallNow).After(cutoff) {
		drop++
	}
	for drop > 0 {
		p.bytes -= estimateSnapshotBytes(p.history[0])
		p.history[0] = nil
		p.history = p.history[1:]
		drop--
	}

	// While live, old rewind states are expendable under memory pressure. While
	// delayed, the queue is not: OverLimit will safely return to realtime rather
	// than silently punch a hole in the replay.
	for p.live && p.maxBytes > 0 && p.bytes > p.maxBytes && len(p.history) > 1 {
		p.bytes -= estimateSnapshotBytes(p.history[0])
		p.history[0] = nil
		p.history = p.history[1:]
	}
	if p.bytes < 0 {
		p.bytes = 0
	}
}

// SetSpeed changes playback rate without changing its current position. A 1x
// delayed stream therefore stays delayed. Use GoLive for the catch-up action.
func (p *playback) SetSpeed(speed float64, wallNow time.Time) error {
	maxSpeed := float64(1)
	if p.archive {
		maxSpeed = 16
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < 0 || speed > maxSpeed {
		return fmt.Errorf("speed must be between 0x and %gx", maxSpeed)
	}
	if p.live {
		p.live = false
		p.now = wallNow
	}
	p.speed = speed
	if speed > 0 {
		p.resumeSpeed = speed
	}
	return nil
}

// TogglePause freezes cluster time, or resumes the rate that was active before
// the pause. Pausing from live starts a delayed 1x timeline.
func (p *playback) TogglePause(wallNow time.Time) {
	if p.live {
		p.live = false
		p.now = wallNow
		p.resumeSpeed = 1
		p.speed = 0
		return
	}
	if p.speed == 0 {
		p.speed = p.resumeSpeed
		return
	}
	p.resumeSpeed = p.speed
	p.speed = 0
}

// GoLive discards the replay queue and old history, returns its freshest
// snapshot so the UI can apply exactly one current state, and seeds that state
// as the beginning of a new rolling rewind window. Without the anchor an idle
// cluster would have no rewind history until its next change.
func (p *playback) GoLive(wallNow time.Time) *model.Snapshot {
	latest := p.latest
	if p.archive {
		p.history = append(p.history, p.queue...)
		p.queue = nil
		p.now = p.timelineEnd(p.now)
		return latest
	}
	p.live, p.speed, p.resumeSpeed = true, 1, 1
	p.now = wallNow
	p.history = nil
	p.queue = nil
	p.bytes = 0
	if latest != nil {
		// Taken belongs to the source observation. The anchor represents the same
		// full state at the instant the user chose realtime, so copy the snapshot
		// header and give only the retained anchor a new timeline timestamp.
		anchor := *latest
		anchor.Taken = wallNow
		p.remember(&anchor, wallNow)
	}
	return latest
}

func (p *playback) DisplayNow(wallNow time.Time) time.Time {
	if p.live || p.now.IsZero() {
		return wallNow
	}
	return p.now
}

func (p *playback) Behind(wallNow time.Time) time.Duration {
	if p.live || p.now.IsZero() || !wallNow.After(p.now) {
		return 0
	}
	return wallNow.Sub(p.now)
}

func (p *playback) OverLimit(wallNow time.Time) bool {
	return !p.archive && !p.live && ((p.maxAge > 0 && p.Behind(wallNow) > p.maxAge) ||
		(p.maxBytes > 0 && p.bytes > p.maxBytes))
}

func snapshotTime(snap *model.Snapshot, fallback time.Time) time.Time {
	if snap.Taken.IsZero() {
		return fallback
	}
	return snap.Taken
}

func (p *playback) timelineEnd(fallback time.Time) time.Time {
	end := p.archiveEnd
	if p.latest != nil {
		latest := snapshotTime(p.latest, fallback)
		if end.IsZero() || latest.After(end) {
			end = latest
		}
	}
	if end.IsZero() {
		return fallback
	}
	return end
}

// estimateSnapshotBytes is intentionally conservative rather than pretending
// to be Go heap accounting. It covers the allocations retained uniquely by a
// snapshot; the limit is a safety rail, while maxAge is the user-facing policy.
func estimateSnapshotBytes(s *model.Snapshot) int64 {
	if s == nil {
		return 0
	}
	n := int64(512 + len(s.Context))
	for _, np := range s.NodePools {
		n += int64(128 + len(np.Name))
	}
	for _, node := range s.Nodes {
		if node == nil {
			continue
		}
		n += 512
		n += int64(len(node.Transitions) * 32)
		n += stringBytes(node.Name, node.InstanceType, node.Zone, node.Region, node.Arch,
			node.CapacityType, node.NodePool, node.NodeClaim, node.ProviderID, node.Message,
			node.ConsolidationReason, node.DisruptionReason)
		for k, v := range node.Labels {
			n += int64(48 + len(k) + len(v))
		}
		for _, pod := range node.Pods {
			if pod == nil {
				continue
			}
			n += 320 + stringBytes(pod.Namespace, pod.Name, pod.NodeName, pod.Owner)
		}
	}
	return n
}

func stringBytes(ss ...string) int64 {
	var n int64
	for _, s := range ss {
		n += int64(16 + len(s))
	}
	return n
}
