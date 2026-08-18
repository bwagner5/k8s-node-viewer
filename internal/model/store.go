package model

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TombstoneGrace is how long a deleted node stays in snapshots after its
// informer delete event, so the UI can play an exit animation. Nothing else in
// the pipeline needs to know about animation timing.
const TombstoneGrace = 3 * time.Second

// Store is the single hub every source writes into and the UI reads from.
//
// Sources (informer handlers, the metrics poller, the fake generator) call the
// Upsert/Delete methods from their own goroutines. Each mutation marks the
// store dirty; Watch coalesces bursts — a 500-node rollout produces thousands
// of events per second — into at most one snapshot per interval. That
// coalescing is the whole reason this type exists rather than wiring informer
// handlers straight into tea.Program.Send.
type Store struct {
	mu         sync.RWMutex
	nodes      map[string]*Node
	pods       map[string]*Pod // ns/name
	podsByNode map[string]map[string]*Pod
	nodePools  map[string]*NodePool
	// claims tracks Karpenter NodeClaims that have no Node yet, so the UI can
	// draw a box the instant a provisioning decision is made. This is the most
	// compelling half-second of a scale-up demo and it is invisible if you
	// only watch Nodes.
	claims map[string]*Node

	context      string
	hasKarpenter bool
	hasMetrics   bool

	gen   atomic.Uint64
	dirty atomic.Bool
	wake  chan struct{}
}

func NewStore(kubeContext string) *Store {
	return &Store{
		nodes:      map[string]*Node{},
		pods:       map[string]*Pod{},
		podsByNode: map[string]map[string]*Pod{},
		nodePools:  map[string]*NodePool{},
		claims:     map[string]*Node{},
		context:    kubeContext,
		wake:       make(chan struct{}, 1),
	}
}

func (s *Store) touch() {
	s.dirty.Store(true)
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already pending; one is enough
	}
}

// SetCapabilities records which optional APIs were discovered, for the status bar.
func (s *Store) SetCapabilities(karpenter, metrics bool) {
	s.mu.Lock()
	s.hasKarpenter, s.hasMetrics = karpenter, metrics
	s.mu.Unlock()
	s.touch()
}

// UpsertNode installs a node. The caller must not retain or mutate n.
func (s *Store) UpsertNode(n *Node) {
	s.mu.Lock()
	if prev, ok := s.nodes[n.Name]; ok {
		// Preserve fields owned by other sources: metrics arrive on a separate
		// poll cadence and the NodeClaim carries pricing.
		if prev.HasUsage && !n.HasUsage {
			n.Usage, n.HasUsage = prev.Usage, true
		}
		if prev.HasPrice && !n.HasPrice {
			n.Price, n.HasPrice = prev.Price, true
		}
		if n.NodeClaim == "" {
			n.NodeClaim = prev.NodeClaim
		}
	}
	// A real Node supersedes its provisioning placeholder.
	//
	// Match on the placeholder's resolved node name, not just on n.NodeClaim:
	// a Node object carries no reference back to its NodeClaim (Karpenter links
	// them via NodeClaim.status.nodeName), so n.NodeClaim is empty for every
	// node the informer converts. Keying only off it meant the placeholder
	// survived until some later NodeClaim event happened to fire, and on a
	// cluster where the node and claim share a name that showed up as the same
	// node drawn twice.
	for claimName, c := range s.claims {
		if c.Name != n.Name && claimName != n.NodeClaim {
			continue
		}
		if n.NodeClaim == "" {
			n.NodeClaim = claimName
		}
		if !n.HasPrice && c.HasPrice {
			n.Price, n.HasPrice = c.Price, true
		}
		if n.NodePool == "" {
			n.NodePool = c.NodePool
		}
		delete(s.claims, claimName)
	}
	s.nodes[n.Name] = n
	s.mu.Unlock()
	s.touch()
}

// DeleteNode tombstones a node rather than dropping it, so the exit animation
// has something to draw. Reap clears it later.
func (s *Store) DeleteNode(name string) {
	s.mu.Lock()
	if n, ok := s.nodes[name]; ok && n.DeletedAt.IsZero() {
		n.Phase = PhaseGone
		n.DeletedAt = time.Now()
	}
	s.mu.Unlock()
	s.touch()
}

// SetNodeUsage records a metrics-server sample for a node.
func (s *Store) SetNodeUsage(name string, usage Resources) {
	s.mu.Lock()
	if n, ok := s.nodes[name]; ok {
		n.Usage, n.HasUsage = usage, true
	}
	s.mu.Unlock()
	s.touch()
}

// SetPodUsage records a metrics-server sample for a pod.
func (s *Store) SetPodUsage(key string, usage Resources) {
	s.mu.Lock()
	if p, ok := s.pods[key]; ok {
		p.Usage, p.HasUsage = usage, true
	}
	s.mu.Unlock()
	s.touch()
}

// UpsertPod installs a pod. Unscheduled pods are dropped: this is a node
// viewer, and a pod with no node has nowhere to be drawn.
func (s *Store) UpsertPod(p *Pod) {
	if p.NodeName == "" {
		s.DeletePod(p.Key())
		return
	}
	s.mu.Lock()
	key := p.Key()
	if prev, ok := s.pods[key]; ok {
		if prev.HasUsage && !p.HasUsage {
			p.Usage, p.HasUsage = prev.Usage, true
		}
		if prev.NodeName != p.NodeName {
			s.unindexLocked(prev)
		}
	}
	s.pods[key] = p
	byNode, ok := s.podsByNode[p.NodeName]
	if !ok {
		byNode = map[string]*Pod{}
		s.podsByNode[p.NodeName] = byNode
	}
	byNode[key] = p
	s.mu.Unlock()
	s.touch()
}

func (s *Store) DeletePod(key string) {
	s.mu.Lock()
	if p, ok := s.pods[key]; ok {
		s.unindexLocked(p)
		delete(s.pods, key)
	}
	s.mu.Unlock()
	s.touch()
}

func (s *Store) unindexLocked(p *Pod) {
	if byNode, ok := s.podsByNode[p.NodeName]; ok {
		delete(byNode, p.Key())
		if len(byNode) == 0 {
			delete(s.podsByNode, p.NodeName)
		}
	}
}

func (s *Store) UpsertNodePool(np *NodePool) {
	s.mu.Lock()
	s.nodePools[np.Name] = np
	s.mu.Unlock()
	s.touch()
}

func (s *Store) DeleteNodePool(name string) {
	s.mu.Lock()
	delete(s.nodePools, name)
	s.mu.Unlock()
	s.touch()
}

// UpsertClaim registers a NodeClaim. Once the claim's Node exists the
// placeholder is dropped and the claim only contributes pricing and pool
// membership to the real node.
func (s *Store) UpsertClaim(claimName string, placeholder *Node) {
	s.mu.Lock()
	adopted := false
	for _, n := range s.nodes {
		if n.NodeClaim == claimName || (placeholder.Name != "" && n.Name == placeholder.Name) {
			n.NodeClaim = claimName
			if placeholder.HasPrice {
				n.Price, n.HasPrice = placeholder.Price, true
			}
			if n.NodePool == "" {
				n.NodePool = placeholder.NodePool
			}
			adopted = true
		}
	}
	if adopted {
		delete(s.claims, claimName)
	} else {
		s.claims[claimName] = placeholder
	}
	s.mu.Unlock()
	s.touch()
}

func (s *Store) DeleteClaim(claimName string) {
	s.mu.Lock()
	delete(s.claims, claimName)
	s.mu.Unlock()
	s.touch()
}

// reap drops tombstones whose grace period has elapsed. Returns true if any
// were removed, so Watch knows to emit one final snapshot without them.
func (s *Store) reap(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	for name, n := range s.nodes {
		if !n.DeletedAt.IsZero() && now.Sub(n.DeletedAt) > TombstoneGrace {
			delete(s.nodes, name)
			delete(s.podsByNode, name)
			removed = true
		}
	}
	return removed
}

// Snapshot builds an immutable view. Nodes and their pod slices are copied, so
// the UI can hold a snapshot across frames while sources keep writing.
func (s *Store) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := &Snapshot{
		Generation:   s.gen.Add(1),
		Taken:        time.Now(),
		Context:      s.context,
		HasKarpenter: s.hasKarpenter,
		HasMetrics:   s.hasMetrics,
		Nodes:        make([]*Node, 0, len(s.nodes)+len(s.claims)),
	}

	namespaces := map[string]struct{}{}
	poolCounts := map[string]int{}
	// emitted guards the one invariant the renderer depends on: a node name
	// appears at most once per snapshot. Placeholders and real nodes live in
	// separate maps, so without this any gap in their reconciliation surfaces as
	// the same node drawn twice.
	emitted := make(map[string]struct{}, len(s.nodes)+len(s.claims))

	for _, src := range s.nodes {
		n := *src // shallow copy; the fields we mutate below are replaced wholesale
		n.Requests, n.Limits = Resources{}, Resources{}
		byNode := s.podsByNode[src.Name]
		n.Pods = make([]*Pod, 0, len(byNode))
		for _, p := range byNode {
			namespaces[p.Namespace] = struct{}{}
			if !p.Phase.Active() {
				continue
			}
			cp := *p
			n.Pods = append(n.Pods, &cp)
			n.Requests = n.Requests.Add(cp.Requests)
			n.Limits = n.Limits.Add(cp.Limits)
		}
		n.Requests.Pods = int64(len(n.Pods))
		sortPods(n.Pods)

		snap.Nodes = append(snap.Nodes, &n)
		emitted[n.Name] = struct{}{}
		poolCounts[n.NodePool]++
		if n.Phase != PhaseGone {
			snap.Totals.Nodes++
			snap.Totals.Pods += len(n.Pods)
			snap.Totals.Allocatable = snap.Totals.Allocatable.Add(n.Allocatable)
			snap.Totals.Requests = snap.Totals.Requests.Add(n.Requests)
			if n.HasUsage {
				snap.Totals.Usage = snap.Totals.Usage.Add(n.Usage)
			}
			snap.Totals.HourlyCost += n.Price
		}
	}

	// Provisioning placeholders come last so they slot in at the end of the
	// grid instead of shuffling existing boxes around mid-animation. A
	// placeholder whose node has already registered is dropped: the real node is
	// strictly better information, and drawing both is the duplicate.
	for _, c := range s.claims {
		if _, dup := emitted[c.Name]; dup {
			continue
		}
		n := *c
		n.Phase = PhaseProvisioning
		snap.Nodes = append(snap.Nodes, &n)
		emitted[n.Name] = struct{}{}
		poolCounts[n.NodePool]++
	}

	for _, np := range s.nodePools {
		cp := *np
		cp.NodeRefs = poolCounts[np.Name]
		snap.NodePools = append(snap.NodePools, &cp)
	}
	sort.Slice(snap.NodePools, func(i, j int) bool { return snap.NodePools[i].Name < snap.NodePools[j].Name })

	snap.Namespaces = make([]string, 0, len(namespaces))
	for ns := range namespaces {
		snap.Namespaces = append(snap.Namespaces, ns)
	}
	sort.Strings(snap.Namespaces)

	return snap
}

// sortPods gives pod cells a stable order: DaemonSets first (they are the
// boring background on every node), then by owner and name. Stability matters
// more than the particular order — cells must not jump between frames.
func sortPods(pods []*Pod) {
	sort.Slice(pods, func(i, j int) bool {
		a, b := pods[i], pods[j]
		if a.DaemonSet != b.DaemonSet {
			return a.DaemonSet
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Name < b.Name
	})
}

// Watch emits a snapshot whenever the store changes, rate-limited to one per
// minInterval, plus a periodic snapshot so tombstone reaping and age columns
// stay live on an idle cluster. The channel closes when ctx is done.
func (s *Store) Watch(ctx context.Context, minInterval time.Duration) <-chan *Snapshot {
	out := make(chan *Snapshot, 1)
	go func() {
		defer close(out)
		ticker := time.NewTicker(minInterval)
		defer ticker.Stop()
		idle := time.NewTicker(time.Second)
		defer idle.Stop()

		emit := func() {
			snap := s.Snapshot()
			select {
			case out <- snap:
			case <-ctx.Done():
			}
		}
		emit() // don't make the first paint wait for an event

		for {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				// Drain to the next tick so a burst collapses into one build.
				select {
				case <-ticker.C:
					if s.dirty.Swap(false) {
						emit()
					}
				case <-ctx.Done():
					return
				}
			case <-idle.C:
				if s.reap(time.Now()) || s.dirty.Swap(false) {
					emit()
				}
			}
		}
	}()
	return out
}

// TrimOwner collapses a ReplicaSet name to its Deployment ("web-6f4b9c7d8-" ->
// "web") so every pod of a workload shares one colour.
func TrimOwner(kind, name string) string {
	if kind != "ReplicaSet" {
		return name
	}
	if i := strings.LastIndex(name, "-"); i > 0 {
		return name[:i]
	}
	return name
}
