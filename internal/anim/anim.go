// Package anim holds the view-only animation state.
//
// The design rule: animation state lives here, never in model.Snapshot. A
// snapshot is a fact about the cluster; a Track is a fact about what the screen
// is currently doing. Keeping them apart means a snapshot arriving mid-flight
// never resets an in-progress animation, and the renderer stays a pure function
// of (snapshot, tracks, layout).
package anim

import (
	"math"
	"time"
)

// Durations tuned for a presentation: long enough to be noticed from the back
// of a room, short enough not to lag behind a fast scale-up.
const (
	EnterDur = 450 * time.Millisecond
	ExitDur  = 900 * time.Millisecond
	FlashDur = 700 * time.Millisecond
	// MeterTau is the time constant for meter smoothing. Meters chase their
	// target exponentially rather than snapping, which turns a step change in
	// requests into a visible sweep.
	MeterTau = 350 * time.Millisecond
)

// Track is the animation state of one entity (node or pod).
type Track struct {
	// Enter ramps 0->1 when the entity appears.
	Enter float64
	// Exit ramps 0->1 once Leaving is set; at 1 the entity can be forgotten.
	Exit    float64
	Leaving bool
	// Flash decays 1->0 after a notable change, driving a bright highlight.
	Flash float64
	// CPU and Mem are the smoothed values the meters actually draw; they chase
	// targetCPU/targetMem, which is what the snapshot reports.
	CPU, Mem             float64
	targetCPU, targetMem float64
	// offset desynchronises pulses between entities so a draining fleet
	// shimmers instead of strobing in unison.
	offset float64
	seen   bool
	init   bool
}

// EnterEase is the eased 0->1 appearance progress.
func (t *Track) EnterEase() float64 { return easeOutCubic(t.Enter) }

// ExitEase is the eased 0->1 disappearance progress.
func (t *Track) ExitEase() float64 { return easeInQuad(t.Exit) }

// Done reports that an exiting entity has finished animating out.
func (t *Track) Done() bool { return t.Leaving && t.Exit >= 1 }

// Registry owns the tracks for one screen.
type Registry struct {
	nodes map[string]*Track
	pods  map[string]*Track
	clock float64 // seconds since start, drives pulses
}

func NewRegistry() *Registry {
	return &Registry{nodes: map[string]*Track{}, pods: map[string]*Track{}}
}

// Clock is the seconds-since-start used by Pulse.
func (r *Registry) Clock() float64 { return r.clock }

// Node returns the track for a node, creating it on first sight.
func (r *Registry) Node(name string) *Track { return get(r.nodes, name) }

// Pod returns the track for a pod key, creating it on first sight.
func (r *Registry) Pod(key string) *Track { return get(r.pods, key) }

func get(m map[string]*Track, key string) *Track {
	if t, ok := m[key]; ok {
		t.seen = true
		return t
	}
	t := &Track{seen: true, offset: hashOffset(key)}
	m[key] = t
	return t
}

// BeginSync starts a mark-and-sweep pass. Call Node/Pod for every entity that
// is still present, then EndSync to retire the rest.
func (r *Registry) BeginSync() {
	for _, t := range r.nodes {
		t.seen = false
	}
	for _, t := range r.pods {
		t.seen = false
	}
}

// EndSync marks unseen entities as leaving and deletes those that have finished
// their exit animation. Nodes are usually retired by the store's tombstone, but
// this catches anything that vanishes from view for another reason — a filter
// change, or a pod rescheduling elsewhere.
func (r *Registry) EndSync() {
	sweep(r.nodes)
	sweep(r.pods)
}

func sweep(m map[string]*Track) {
	for key, t := range m {
		if t.seen {
			continue
		}
		t.Leaving = true
		if t.Exit >= 1 {
			delete(m, key)
		}
	}
}

// SetLeaving marks a track as animating out (used for store tombstones, which
// are still present in the snapshot but known to be dying).
func (t *Track) SetLeaving() { t.Leaving = true }

// Notify triggers a flash highlight.
func (t *Track) Notify() { t.Flash = 1 }

// Target sets the values the meters ease toward. The first call snaps, so nodes
// that already exist at startup do not all sweep up from zero at once — only
// genuinely new nodes get the fill animation.
func (t *Track) Target(cpu, mem float64, snap bool) {
	t.targetCPU, t.targetMem = cpu, mem
	if !t.init || snap {
		t.CPU, t.Mem, t.init = cpu, mem, true
	}
}

// Step advances a track that is not owned by a Registry. The cluster-wide header
// meters use one: they are a single, permanent gauge with no entity to key off.
func (t *Track) Step(dt time.Duration) { t.advance(dt) }

// Advance steps every track by dt. Called once per frame, before rendering.
func (r *Registry) Advance(dt time.Duration) {
	r.clock += dt.Seconds()
	for _, t := range r.nodes {
		t.advance(dt)
	}
	for _, t := range r.pods {
		t.advance(dt)
	}
}

func (t *Track) advance(dt time.Duration) {
	step := func(v float64, dur time.Duration) float64 {
		return math.Min(1, v+float64(dt)/float64(dur))
	}
	t.Enter = step(t.Enter, EnterDur)
	if t.Leaving {
		t.Exit = step(t.Exit, ExitDur)
	}
	if t.Flash > 0 {
		t.Flash = math.Max(0, t.Flash-float64(dt)/float64(FlashDur))
	}
	// Exponential approach: framerate-independent and never overshoots.
	k := 1 - math.Exp(-float64(dt)/float64(MeterTau))
	t.CPU += (t.targetCPU - t.CPU) * k
	t.Mem += (t.targetMem - t.Mem) * k
}

// Busy reports whether anything is still moving. The event loop drops to a slow
// tick when nothing is, so an idle cluster does not burn a core redrawing.
func (r *Registry) Busy() bool {
	for _, t := range r.nodes {
		if t.busy() {
			return true
		}
	}
	for _, t := range r.pods {
		if t.busy() {
			return true
		}
	}
	return false
}

const meterEpsilon = 0.002

func (t *Track) busy() bool {
	return t.Enter < 1 || t.Flash > 0 || (t.Leaving && t.Exit < 1) ||
		math.Abs(t.targetCPU-t.CPU) > meterEpsilon || math.Abs(t.targetMem-t.Mem) > meterEpsilon
}

// Pulse is a 0..1 sine wave of the given period, offset per entity. Used for
// the draining shimmer and the provisioning breathe.
func (r *Registry) Pulse(t *Track, period time.Duration) float64 {
	if period <= 0 {
		return 0
	}
	phase := (r.clock/period.Seconds() + t.offset) * 2 * math.Pi
	return 0.5 + 0.5*math.Sin(phase)
}

func hashOffset(key string) float64 {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h = (h ^ uint32(key[i])) * 16777619
	}
	return float64(h%1000) / 1000
}

func easeOutCubic(x float64) float64 {
	x = clamp01(x)
	u := 1 - x
	return 1 - u*u*u
}

func easeInQuad(x float64) float64 {
	x = clamp01(x)
	return x * x
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
