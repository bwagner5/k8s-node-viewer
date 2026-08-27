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
	// pending holds pods with no node at all. They cannot be drawn in a box —
	// there is no box — but "how deep is the backlog, and how much of it has the
	// scheduler given up on" is the question a scale-up demo is answering, so
	// they are kept in their own index rather than dropped.
	pending   map[string]*Pod // ns/name
	nodePools map[string]*NodePool
	// claims tracks Karpenter NodeClaims that have no Node yet, so the UI can
	// draw a box the instant a provisioning decision is made. This is the most
	// compelling half-second of a scale-up demo and it is invisible if you
	// only watch Nodes.
	claims map[string]*Node
	// consolidation holds Karpenter's disruption verdicts, keyed by the name of
	// the object the event named — a Node or a NodeClaim. It is deliberately not
	// merged into the nodes: the event routinely arrives before the Node does (and
	// names the claim, which is what Karpenter is actually reasoning about), so
	// the two are joined at snapshot time instead.
	consolidation map[string]consolidationSignal

	context      string
	hasKarpenter bool

	gen   atomic.Uint64
	dirty atomic.Bool
	wake  chan struct{}
}

// consolidationSignal is one reported verdict and when it was reported.
type consolidationSignal struct {
	state  Consolidation
	reason string
	at     time.Time
}

// phaseHistoryLimit is deliberately small. The card only needs a recent
// breadcrumb and the detail pane only needs the lifecycle handoff; Kubernetes
// events remain the source for a complete audit trail.
const phaseHistoryLimit = 8

func NewStore(kubeContext string) *Store {
	return &Store{
		nodes:         map[string]*Node{},
		pods:          map[string]*Pod{},
		podsByNode:    map[string]map[string]*Pod{},
		pending:       map[string]*Pod{},
		nodePools:     map[string]*NodePool{},
		claims:        map[string]*Node{},
		consolidation: map[string]consolidationSignal{},
		context:       kubeContext,
		wake:          make(chan struct{}, 1),
	}
}

func (s *Store) touch() {
	s.dirty.Store(true)
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already pending; one is enough
	}
}

// SetKarpenter records whether the optional Karpenter API was discovered.
func (s *Store) SetKarpenter(karpenter bool) {
	s.mu.Lock()
	s.hasKarpenter = karpenter
	s.mu.Unlock()
	s.touch()
}

// UpsertNode installs a node. The caller must not retain or mutate n.
func (s *Store) UpsertNode(n *Node) {
	s.mu.Lock()
	// Honour the ownership contract even when a source accidentally retains its
	// input. Phase observation depends on the stored previous value not changing
	// behind the store's back.
	cp := *n
	cp.Transitions = append([]PhaseTransition(nil), n.Transitions...)
	n = &cp
	now := time.Now()
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
		if n.ProviderID == "" {
			n.ProviderID = prev.ProviderID
		}
		n.Created = earliest(prev.Created, n.Created)
		n.Transitions = append([]PhaseTransition(nil), prev.Transitions...)
		recordPhaseTransition(n, n.Phase, now)
	} else if len(n.Transitions) == 0 {
		at := n.Created
		if at.IsZero() {
			at = now
		}
		n.Transitions = []PhaseTransition{{Phase: n.Phase, At: at}}
	}
	// A real Node supersedes its provisioning placeholder.
	for claimName, c := range s.claims {
		if !claimMatches(claimName, c, n) {
			continue
		}
		if n.NodeClaim == "" {
			n.NodeClaim = claimName
		}
		if n.ProviderID == "" {
			n.ProviderID = c.ProviderID
		}
		if !n.HasPrice && c.HasPrice {
			n.Price, n.HasPrice = c.Price, true
		}
		if n.NodePool == "" {
			n.NodePool = c.NodePool
		}
		// The claim is older than its Node — Karpenter creates it, then waits for
		// the instance to boot and kubelet to register, which is where the Node's
		// own creationTimestamp comes from. Keeping the claim's start means the
		// age a box has been showing while provisioning keeps counting up instead
		// of resetting to zero at the moment it turns green.
		n.Created = earliest(c.Created, n.Created)
		if len(n.Transitions) <= 1 && len(c.Transitions) > 0 {
			n.Transitions = append([]PhaseTransition(nil), c.Transitions...)
			recordPhaseTransition(n, n.Phase, now)
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
		recordPhaseTransition(n, PhaseGone, n.DeletedAt)
	}
	s.mu.Unlock()
	s.touch()
}

func recordPhaseTransition(n *Node, phase Phase, at time.Time) {
	if len(n.Transitions) > 0 && n.Transitions[len(n.Transitions)-1].Phase == phase {
		return
	}
	n.Transitions = append(n.Transitions, PhaseTransition{Phase: phase, At: at})
	if extra := len(n.Transitions) - phaseHistoryLimit; extra > 0 {
		copy(n.Transitions, n.Transitions[extra:])
		n.Transitions = n.Transitions[:phaseHistoryLimit]
	}
}

// SetConsolidation records a disruption verdict against a Node or NodeClaim name.
//
// Out-of-order delivery is the norm rather than the exception here — an informer
// resync replays its whole cache in map order — so an older verdict never
// overwrites a newer one. That check is what stops the column from flickering
// between y and n on every resync.
func (s *Store) SetConsolidation(object string, state Consolidation, reason string, at time.Time) {
	if object == "" || state == ConsolidationUnknown {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	s.mu.Lock()
	prev, ok := s.consolidation[object]
	changed := !ok || !prev.at.After(at)
	if changed {
		s.consolidation[object] = consolidationSignal{state: state, reason: reason, at: at}
	}
	s.mu.Unlock()
	if changed {
		s.touch()
	}
}

// consolidationForLocked joins a node to its verdict, preferring whichever of the
// node's own name and its claim's was reported more recently. Expired signals
// report unknown rather than a stale answer.
func (s *Store) consolidationForLocked(n *Node, now time.Time) (consolidationSignal, bool) {
	var best consolidationSignal
	found := false
	for _, key := range [...]string{n.Name, n.NodeClaim} {
		if key == "" {
			continue
		}
		sig, ok := s.consolidation[key]
		if !ok || now.Sub(sig.at) > ConsolidationTTL {
			continue
		}
		if !found || sig.at.After(best.at) {
			best, found = sig, true
		}
	}
	return best, found
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

// UpsertPod installs a pod. A pod with no node is counted as backlog instead of
// being drawn; anything already finished is not counted at all.
func (s *Store) UpsertPod(p *Pod) {
	key := p.Key()
	if p.NodeName == "" {
		s.mu.Lock()
		// It may have been scheduled a moment ago and lost its node again (a
		// preempted pod is recreated unassigned), so clear the placed indexes too.
		if prev, ok := s.pods[key]; ok {
			s.unindexLocked(prev)
			delete(s.pods, key)
		}
		if p.Phase == PodPending {
			s.pending[key] = p
		} else {
			delete(s.pending, key)
		}
		s.mu.Unlock()
		s.touch()
		return
	}
	s.mu.Lock()
	delete(s.pending, key)
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
	delete(s.pending, key)
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
	cp := *placeholder
	cp.Transitions = append([]PhaseTransition(nil), placeholder.Transitions...)
	placeholder = &cp
	now := time.Now()
	if prev, ok := s.claims[claimName]; ok {
		placeholder.Transitions = append([]PhaseTransition(nil), prev.Transitions...)
		recordPhaseTransition(placeholder, placeholder.Phase, now)
	} else if len(placeholder.Transitions) == 0 {
		at := placeholder.Created
		if at.IsZero() {
			at = now
		}
		placeholder.Transitions = []PhaseTransition{{Phase: placeholder.Phase, At: at}}
	}
	adopted := false
	for _, n := range s.nodes {
		if !claimMatches(claimName, placeholder, n) {
			continue
		}
		n.NodeClaim = claimName
		if n.ProviderID == "" {
			n.ProviderID = placeholder.ProviderID
		}
		if placeholder.HasPrice {
			n.Price, n.HasPrice = placeholder.Price, true
		}
		if n.NodePool == "" {
			n.NodePool = placeholder.NodePool
		}
		n.Created = earliest(placeholder.Created, n.Created)
		adopted = true
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

// reap drops tombstones whose grace period has elapsed, and consolidation
// verdicts that have gone stale. Returns true if anything was removed, so Watch
// knows to emit one final snapshot without them — expiring a verdict has to
// repaint the column, or it would keep showing an answer the store no longer
// believes.
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
	for object, sig := range s.consolidation {
		if now.Sub(sig.at) > ConsolidationTTL {
			delete(s.consolidation, object)
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
		Nodes:        make([]*Node, 0, len(s.nodes)+len(s.claims)),
	}

	poolCounts := map[string]int{}
	// emitted guards the one invariant the renderer depends on: a node name
	// appears at most once per snapshot. Placeholders and real nodes live in
	// separate maps, so without this any gap in their reconciliation surfaces as
	// the same node drawn twice.
	emitted := make(map[string]struct{}, len(s.nodes)+len(s.claims))
	// A placeholder that has not been joined yet is still named after its claim,
	// so the name check alone cannot see that it duplicates a node already
	// emitted. Tracking providerIDs makes the invariant hold at render time
	// regardless of what the reconciliation paths managed to match.
	emittedProviders := make(map[string]struct{}, len(s.nodes))

	for _, src := range s.nodes {
		n := *src // shallow copy; the fields we mutate below are replaced wholesale
		n.Transitions = append([]PhaseTransition(nil), src.Transitions...)
		n.Requests, n.Limits = Resources{}, Resources{}
		byNode := s.podsByNode[src.Name]
		n.Pods = make([]*Pod, 0, len(byNode))
		for _, p := range byNode {
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

		if sig, ok := s.consolidationForLocked(src, snap.Taken); ok {
			n.Consolidatable, n.ConsolidationReason, n.ConsolidationAt = sig.state, sig.reason, sig.at
		}

		snap.Nodes = append(snap.Nodes, &n)
		emitted[n.Name] = struct{}{}
		if n.ProviderID != "" {
			emittedProviders[n.ProviderID] = struct{}{}
		}
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
		if c.ProviderID != "" {
			if _, dup := emittedProviders[c.ProviderID]; dup {
				continue
			}
		}
		n := *c
		n.Phase = PhaseProvisioning
		n.Transitions = append([]PhaseTransition(nil), c.Transitions...)
		snap.Nodes = append(snap.Nodes, &n)
		emitted[n.Name] = struct{}{}
		poolCounts[n.NodePool]++
	}

	// The backlog is cluster-wide by nature: it belongs to no node, so it is
	// summarised into the totals and nowhere else.
	for _, p := range s.pending {
		snap.Totals.Pending++
		if p.Unschedulable {
			snap.Totals.Unschedulable++
		}
	}

	for _, np := range s.nodePools {
		cp := *np
		cp.NodeRefs = poolCounts[np.Name]
		snap.NodePools = append(snap.NodePools, &cp)
	}
	sort.Slice(snap.NodePools, func(i, j int) bool { return snap.NodePools[i].Name < snap.NodePools[j].Name })

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

// claimMatches reports whether the placeholder c, registered under claimName,
// describes the same machine as the real node n.
//
// Three keys, because which of them are populated depends on how far Karpenter
// has got, and the whole point is to match at the earliest possible moment:
//
//   - ProviderID is on the NodeClaim from the instant the instance is launched
//     and on the Node from the instant kubelet registers it, so it links the two
//     without waiting for anything. Before this key existed there was a window —
//     Node registered, Karpenter not yet round to writing status.nodeName — in
//     which the placeholder was still named after the claim and matched nothing,
//     and the same machine was drawn twice for the length of a reconcile.
//   - claimName against n.NodeClaim catches nodes we have already joined once,
//     which is what makes an informer resync idempotent.
//   - The names, for a claim whose status.nodeName has landed, and for clusters
//     with no providerID on the Node at all (an out-of-tree cloud provider sets
//     it via the cloud-controller-manager, which can lag registration).
func claimMatches(claimName string, c, n *Node) bool {
	if c.ProviderID != "" && c.ProviderID == n.ProviderID {
		return true
	}
	if n.NodeClaim != "" && n.NodeClaim == claimName {
		return true
	}
	return c.Name != "" && c.Name == n.Name
}

// earliest picks the older of two timestamps, ignoring zero values — a node's
// age is measured from the first moment anything knew about it, and a source
// that has no timestamp must not win by being "earlier" than everything.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case a.Before(b):
		return a
	default:
		return b
	}
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
