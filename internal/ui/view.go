package ui

import (
	"regexp"
	"sort"

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

// Filter narrows what the grid shows through the two high-value selectors: a
// NodePool and a node-name query.
type Filter struct {
	NodePool  string
	NodeQuery string
	nodeRe    *regexp.Regexp
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
	return f.NodePool != "" || f.NodeQuery != ""
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
	add("node", f.NodeQuery)
	return out
}

// Clear resets every filter.
func (f *Filter) Clear() {
	*f = Filter{}
}

// visible is one node and the pods prepared for rendering inside it.
type visible struct {
	node *model.Node
	pods []*model.Pod
}

func (f *Filter) matchesNode(n *model.Node) bool {
	if f.NodePool != "" && n.NodePool != f.NodePool {
		return false
	}
	if f.nodeRe != nil && !f.nodeRe.MatchString(n.Name) {
		return false
	}
	return true
}

// Apply filters and sorts a snapshot into render order.
func (f *Filter) Apply(snap *model.Snapshot, key SortKey, desc bool) []visible {
	out := make([]visible, 0, len(snap.Nodes))

	for _, n := range snap.Nodes {
		if !f.matchesNode(n) {
			continue
		}
		v := visible{node: n, pods: make([]*model.Pod, 0, len(n.Pods))}
		for _, p := range n.Pods {
			v.pods = append(v.pods, p)
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
