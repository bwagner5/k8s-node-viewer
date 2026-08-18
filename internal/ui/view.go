package ui

import (
	"regexp"
	"sort"
	"strings"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// Mode is how much detail each node box shows.
type Mode int

const (
	// ModePods draws every pod as a cell inside the node, kube-ops-view style.
	ModePods Mode = iota
	// ModeNodes drops the pod cells and turns the whole box into a utilisation
	// gauge with a pod count. This is the "I only care about capacity" view.
	ModeNodes
	// ModeDense is one compact row per node, for clusters too large to give
	// every node a box.
	ModeDense
)

var modeNames = [...]string{"pods", "nodes", "dense"}

func (m Mode) String() string { return modeNames[m] }

// ParseMode maps a command argument to a Mode.
func ParseMode(s string) (Mode, bool) {
	for i, n := range modeNames {
		if n == s {
			return Mode(i), true
		}
	}
	return 0, false
}

// SortKey orders the node grid.
type SortKey int

const (
	SortName SortKey = iota
	SortCPU
	SortMem
	SortPods
	SortAge
	SortPool
	SortType
)

var sortNames = [...]string{"name", "cpu", "mem", "pods", "age", "nodepool", "type"}

func (s SortKey) String() string { return sortNames[s] }

// ParseSort maps a command argument to a SortKey.
func ParseSort(s string) (SortKey, bool) {
	for i, n := range sortNames {
		if n == s {
			return SortKey(i), true
		}
	}
	if s == "np" || s == "pool" {
		return SortPool, true
	}
	return 0, false
}

// Filter narrows what the grid shows.
//
// Two distinct behaviours, and the distinction is the point:
//   - Node-level fields (NodePool, NodeQuery, Phases, ...) hide nodes outright.
//   - Pod-level fields (Namespace, Owner) *highlight*: matching pods keep their
//     colour and everything else goes grey, so you can see where a workload
//     lives across the fleet instead of losing its context. Only turns hard
//     when Only is set.
type Filter struct {
	NodePool     string
	InstanceType string
	CapacityType string
	NodeQuery    string
	nodeRe       *regexp.Regexp
	Phases       map[model.Phase]bool

	Namespace string
	Owner     string
	// Only hides nodes that have no pod matching the pod-level filters.
	Only bool
	// HideDaemonSets drops DaemonSet pods from the cells; they are identical on
	// every node and mostly noise once you have seen them.
	HideDaemonSets bool
}

// SetNodeQuery compiles a case-insensitive substring/regex node matcher. An
// invalid pattern is reported rather than silently ignored.
func (f *Filter) SetNodeQuery(q string) error {
	if q == "" {
		f.NodeQuery, f.nodeRe = "", nil
		return nil
	}
	re, err := regexp.Compile("(?i)" + q)
	if err != nil {
		return err
	}
	f.NodeQuery, f.nodeRe = q, re
	return nil
}

// Active reports whether anything is being filtered, for the status bar.
func (f *Filter) Active() bool {
	return f.NodePool != "" || f.InstanceType != "" || f.CapacityType != "" ||
		f.NodeQuery != "" || len(f.Phases) > 0 || f.Namespace != "" || f.Owner != ""
}

// Describe renders the active filters as compact status-bar chips.
func (f *Filter) Describe() []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, k+":"+v)
		}
	}
	add("pool", f.NodePool)
	add("type", f.InstanceType)
	add("cap", f.CapacityType)
	add("node", f.NodeQuery)
	add("ns", f.Namespace)
	add("owner", f.Owner)
	if len(f.Phases) > 0 {
		names := make([]string, 0, len(f.Phases))
		for p := range f.Phases {
			names = append(names, strings.ToLower(p.String()))
		}
		sort.Strings(names)
		add("phase", strings.Join(names, "|"))
	}
	if f.Only {
		out = append(out, "only")
	}
	if f.HideDaemonSets {
		out = append(out, "no-ds")
	}
	return out
}

// Clear resets every filter.
func (f *Filter) Clear() {
	hideDS := f.HideDaemonSets
	*f = Filter{HideDaemonSets: hideDS}
}

// visible is one node prepared for rendering: the node itself plus which of its
// pods match the pod-level filters.
type visible struct {
	node *model.Node
	pods []*model.Pod
	// matched marks, per index in pods, whether the pod satisfies the pod-level
	// filters. Non-matching pods still occupy their cells but render grey.
	matched []bool
	// matchCount is how many pods matched; zero means the node itself is
	// irrelevant to the current pod filter.
	matchCount int
}

func (f *Filter) matchesNode(n *model.Node) bool {
	if f.NodePool != "" && n.NodePool != f.NodePool {
		return false
	}
	if f.InstanceType != "" && n.InstanceType != f.InstanceType {
		return false
	}
	if f.CapacityType != "" && n.CapacityType != f.CapacityType {
		return false
	}
	if f.nodeRe != nil && !f.nodeRe.MatchString(n.Name) {
		return false
	}
	if len(f.Phases) > 0 {
		// A tombstone keeps its slot regardless of the phase filter, so a node
		// deleted while filtered still animates out instead of vanishing.
		if n.Phase != model.PhaseGone && !f.Phases[n.Phase] {
			return false
		}
	}
	return true
}

func (f *Filter) matchesPod(p *model.Pod) bool {
	if f.Namespace != "" && p.Namespace != f.Namespace {
		return false
	}
	if f.Owner != "" && !strings.Contains(p.Owner, f.Owner) {
		return false
	}
	return true
}

// Apply filters and sorts a snapshot into render order.
func (f *Filter) Apply(snap *model.Snapshot, key SortKey, desc bool) []visible {
	out := make([]visible, 0, len(snap.Nodes))
	podFilterOn := f.Namespace != "" || f.Owner != ""

	for _, n := range snap.Nodes {
		if !f.matchesNode(n) {
			continue
		}
		v := visible{node: n, pods: make([]*model.Pod, 0, len(n.Pods)), matched: make([]bool, 0, len(n.Pods))}
		for _, p := range n.Pods {
			if f.HideDaemonSets && p.DaemonSet {
				continue
			}
			ok := f.matchesPod(p)
			if ok {
				v.matchCount++
			}
			v.pods = append(v.pods, p)
			v.matched = append(v.matched, ok)
		}
		if f.Only && podFilterOn && v.matchCount == 0 && n.Phase != model.PhaseGone {
			continue
		}
		out = append(out, v)
	}

	sortVisible(out, key, desc)
	return out
}

func sortVisible(vs []visible, key SortKey, desc bool) {
	less := func(i, j int) bool {
		a, b := vs[i].node, vs[j].node
		switch key {
		case SortCPU:
			ac, _ := a.Requests.Frac(a.Allocatable)
			bc, _ := b.Requests.Frac(b.Allocatable)
			if ac != bc {
				return ac < bc
			}
		case SortMem:
			_, am := a.Requests.Frac(a.Allocatable)
			_, bm := b.Requests.Frac(b.Allocatable)
			if am != bm {
				return am < bm
			}
		case SortPods:
			if len(a.Pods) != len(b.Pods) {
				return len(a.Pods) < len(b.Pods)
			}
		case SortAge:
			if !a.Created.Equal(b.Created) {
				return a.Created.After(b.Created) // youngest first reads as "newest"
			}
		case SortPool:
			if a.NodePool != b.NodePool {
				return a.NodePool < b.NodePool
			}
		case SortType:
			if a.InstanceType != b.InstanceType {
				return a.InstanceType < b.InstanceType
			}
		}
		// Name is the tiebreaker for every key, which is what keeps boxes from
		// swapping places between frames when their metrics are equal.
		return a.Name < b.Name
	}
	if desc {
		sort.SliceStable(vs, func(i, j int) bool { return less(j, i) })
		return
	}
	sort.SliceStable(vs, less)
}
