// Package fake drives a model.Store from a simulated cluster.
//
// It exists for three reasons, in order of importance:
//  1. Rehearsing a demo. Cluster autoscaling on cue is not something you want
//     to gamble on in front of an audience; this reproduces the same visuals
//     deterministically and lets you trigger scale-up and drain from the
//     keyboard.
//  2. Developing the renderer without a cluster.
//  3. Exercising every phase, including ones that are hard to catch live (a
//     NodeClaim that exists for two seconds before its Node appears).
//
// It writes through exactly the same model.Store API the informers use, so the
// UI cannot tell the difference.
package fake

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// Controller is the subset of a source the UI may drive interactively. The kube
// source deliberately does not implement it — the viewer is read-only against a
// real cluster.
type Controller interface {
	ScaleUp(n int)
	DrainOne()
	Churn()
}

// Options configures the simulation.
type Options struct {
	Nodes int
	Pools []string
	Seed  int64
	// Speed multiplies the event rate; 2 means twice as many things happen.
	Speed float64
	// Autopilot runs scale-up/drain cycles on its own. With it off, the
	// simulation is quiet until you press a key, which is what you want when
	// narrating a specific sequence.
	Autopilot bool
	// DrainFor is roughly how long a node spends draining before deletion.
	// Worth lengthening when you want to talk over the animation.
	DrainFor time.Duration
}

var instanceTypes = []struct {
	name string
	cpu  int64 // milli
	mem  int64 // bytes
	cost float64
}{
	{"m5.large", 2000, 8 << 30, 0.096},
	{"m5.2xlarge", 8000, 32 << 30, 0.384},
	{"m5.4xlarge", 16000, 64 << 30, 0.768},
	{"c5.9xlarge", 36000, 72 << 30, 1.530},
	{"r5.2xlarge", 8000, 64 << 30, 0.504},
	{"g5.xlarge", 4000, 16 << 30, 1.006},
}

var workloads = []struct {
	name      string
	namespace string
	cpu       int64
	mem       int64
}{
	{"checkout", "shop", 500, 512 << 20},
	{"catalog", "shop", 250, 256 << 20},
	{"payments", "shop", 1000, 1 << 30},
	{"search-indexer", "search", 2000, 4 << 30},
	{"feature-store", "ml", 1500, 6 << 30},
	{"trainer", "ml", 4000, 16 << 30},
	{"api-gateway", "edge", 750, 512 << 20},
}

var daemonSets = []struct {
	name      string
	namespace string
	cpu       int64
	mem       int64
}{
	{"kube-proxy", "kube-system", 100, 64 << 20},
	{"cni-node", "kube-system", 100, 128 << 20},
	{"log-shipper", "observability", 200, 256 << 20},
}

// Cluster is a running simulation.
type Cluster struct {
	opts  Options
	store *model.Store

	mu      sync.Mutex
	rng     *rand.Rand
	nodes   map[string]*simNode
	seq     int
	pending []func() // actions queued to fire on a later tick
}

type simNode struct {
	node     *model.Node
	pods     map[string]*model.Pod
	claim    string
	born     time.Time
	draining bool
	deleteAt time.Time
	// readyAt models the gap between "instance launched" and "kubelet
	// registered", which is the part of a scale-up worth watching.
	readyAt time.Time
}

// New builds a simulation and its store.
func New(opts Options) (*Cluster, *model.Store) {
	if opts.Nodes == 0 {
		opts.Nodes = 12
	}
	if len(opts.Pools) == 0 {
		opts.Pools = []string{"general", "spot-batch", "gpu"}
	}
	if opts.Speed == 0 {
		opts.Speed = 1
	}
	if opts.Seed == 0 {
		opts.Seed = 1
	}
	if opts.DrainFor <= 0 {
		opts.DrainFor = 4 * time.Second
	}
	store := model.NewStore("demo (simulated)")
	store.SetCapabilities(true, true)
	c := &Cluster{
		opts:  opts,
		store: store,
		rng:   rand.New(rand.NewSource(opts.Seed)),
		nodes: map[string]*simNode{},
	}
	return c, store
}

// Run seeds the cluster and then advances it on a tick until ctx is done.
func (c *Cluster) Run(ctx context.Context) error {
	c.seed()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			c.step()
		}
	}
}

func (c *Cluster) seed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, name := range c.opts.Pools {
		c.store.UpsertNodePool(&model.NodePool{
			Name:    name,
			Weight:  int32(len(c.opts.Pools) - i),
			Created: time.Now().Add(-72 * time.Hour),
			Limits:  model.Resources{CPUMilli: 1_000_000, MemBytes: 4 << 40},
		})
	}
	for i := 0; i < c.opts.Nodes; i++ {
		n := c.launchLocked(c.opts.Pools[i%len(c.opts.Pools)])
		// Pre-existing nodes are already registered and already loaded, so the
		// first frame looks like a running cluster rather than a cold start.
		n.readyAt = time.Now().Add(-time.Hour)
		n.born = time.Now().Add(-time.Duration(1+c.rng.Intn(600)) * time.Minute)
		n.node.Created = n.born
		c.promoteLocked(n)
		for j := 0; j < 3+c.rng.Intn(8); j++ {
			c.schedulePodLocked(n)
		}
		c.publishLocked(n)
	}
}

// step advances the simulation by one tick: it settles provisioning nodes,
// progresses drains, and (on autopilot) occasionally starts something new.
func (c *Cluster) step() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()

	for _, n := range c.nodes {
		switch {
		case n.node.Phase == model.PhaseProvisioning && now.After(n.readyAt):
			c.promoteLocked(n)
			// A fresh node immediately attracts its DaemonSets and a couple of
			// workload pods, which is the satisfying part of the animation.
			for j := 0; j < 2+c.rng.Intn(4); j++ {
				c.schedulePodLocked(n)
			}
			c.publishLocked(n)
		case n.draining:
			c.stepDrainLocked(n, now)
		}
	}

	if !c.opts.Autopilot {
		c.runPendingLocked()
		return
	}
	if c.chance(0.10) {
		c.churnLocked()
	}
	if c.chance(0.035) {
		c.scaleUpLocked(1 + c.rng.Intn(3))
	}
	if c.chance(0.03) && c.readyCountLocked() > 4 {
		c.drainOneLocked()
	}
	c.runPendingLocked()
}

func (c *Cluster) runPendingLocked() {
	pending := c.pending
	c.pending = nil
	for _, fn := range pending {
		fn()
	}
}

// chance scales a per-tick probability by the configured speed.
func (c *Cluster) chance(p float64) bool { return c.rng.Float64() < p*c.opts.Speed }

// --- interactive controls ---

// ScaleUp launches n new nodes, as a NodePool would.
func (c *Cluster) ScaleUp(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scaleUpLocked(n)
}

// DrainOne cordons a node, evicts its pods over the next few seconds, then
// deletes it.
func (c *Cluster) DrainOne() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drainOneLocked()
}

// Churn adds and removes a handful of pods.
func (c *Cluster) Churn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < 5; i++ {
		c.churnLocked()
	}
}

// --- simulation internals ---

func (c *Cluster) scaleUpLocked(count int) {
	for i := 0; i < count; i++ {
		pool := c.opts.Pools[c.rng.Intn(len(c.opts.Pools))]
		c.launchLocked(pool)
		// Deliberately no publishLocked here: a launched node exists only as a
		// NodeClaim until kubelet registers it, which is the gap this tool is
		// built to show. Publishing immediately skipped that window and, more
		// quietly, skipped the store's claim-to-node reconciliation entirely.
	}
}

func (c *Cluster) launchLocked(pool string) *simNode {
	c.seq++
	it := instanceTypes[c.rng.Intn(len(instanceTypes))]
	if pool == "gpu" {
		it = instanceTypes[len(instanceTypes)-1]
	}
	now := time.Now()
	name := fmt.Sprintf("ip-10-%d-%d-%d", c.rng.Intn(4), c.rng.Intn(250), c.seq%250)
	capacity := "on-demand"
	if pool == "spot-batch" {
		capacity = "spot"
	}
	node := &model.Node{
		Name:         name,
		NodeClaim:    fmt.Sprintf("%s-%s", pool, randSuffix(c.rng)),
		NodePool:     pool,
		InstanceType: it.name,
		Zone:         fmt.Sprintf("us-west-2%c", 'a'+rune(c.rng.Intn(3))),
		Region:       "us-west-2",
		Arch:         "amd64",
		CapacityType: capacity,
		Created:      now,
		Phase:        model.PhaseProvisioning,
		Message:      "Launched",
		Price:        it.cost,
		HasPrice:     true,
		// Allocatable is slightly under capacity, as kube-reserved makes it.
		Allocatable: model.Resources{CPUMilli: it.cpu - 200, MemBytes: it.mem - (1 << 30), Pods: 110},
		Labels:      map[string]string{"karpenter.sh/nodepool": pool, "node.kubernetes.io/instance-type": it.name},
	}
	if pool == "gpu" {
		node.Allocatable.GPU = 1
	}
	sn := &simNode{
		node:    node,
		pods:    map[string]*model.Pod{},
		claim:   node.NodeClaim,
		born:    now,
		readyAt: now.Add(time.Duration(2500+c.rng.Intn(4000)) * time.Millisecond),
	}
	c.nodes[name] = sn
	// Register the claim first, with no Node: this is the provisioning box.
	c.store.UpsertClaim(sn.claim, cloneNode(node))
	return sn
}

func (c *Cluster) promoteLocked(n *simNode) {
	n.node.Phase = model.PhaseReady
	n.node.Ready = true
	n.node.Schedulable = true
	n.node.Message = ""
	for _, ds := range daemonSets {
		p := &model.Pod{
			Namespace: ds.namespace,
			Name:      fmt.Sprintf("%s-%s", ds.name, randSuffix(c.rng)),
			NodeName:  n.node.Name,
			Phase:     model.PodRunning,
			Ready:     true,
			DaemonSet: true,
			Owner:     ds.name,
			Requests:  model.Resources{CPUMilli: ds.cpu, MemBytes: ds.mem, Pods: 1},
			Created:   time.Now(),
		}
		n.pods[p.Key()] = p
		c.store.UpsertPod(p)
	}
}

func (c *Cluster) schedulePodLocked(n *simNode) {
	if n.draining || n.node.Phase == model.PhaseProvisioning {
		return
	}
	w := workloads[c.rng.Intn(len(workloads))]
	if n.node.NodePool == "gpu" {
		w = workloads[5] // trainer
	}
	// Refuse to overcommit: a node that would exceed allocatable simply has no
	// room, which is what makes the fill meters believable.
	var used model.Resources
	for _, p := range n.pods {
		used = used.Add(p.Requests)
	}
	if used.CPUMilli+w.cpu > n.node.Allocatable.CPUMilli || used.MemBytes+w.mem > n.node.Allocatable.MemBytes {
		return
	}
	p := &model.Pod{
		Namespace: w.namespace,
		Name:      fmt.Sprintf("%s-%s-%s", w.name, randSuffix(c.rng), randSuffix(c.rng)[:4]),
		NodeName:  n.node.Name,
		Phase:     model.PodPending,
		Owner:     w.name,
		Requests:  model.Resources{CPUMilli: w.cpu, MemBytes: w.mem, Pods: 1},
		Created:   time.Now(),
	}
	n.pods[p.Key()] = p
	c.store.UpsertPod(p)
	// Pending -> Running a beat later, so new cells visibly settle in.
	key := p.Key()
	c.after(func() {
		if live, ok := n.pods[key]; ok {
			live.Phase, live.Ready = model.PodRunning, true
			c.store.UpsertPod(live)
			c.publishLocked(n)
		}
	})
}

func (c *Cluster) churnLocked() {
	n := c.pickLocked(func(s *simNode) bool { return !s.draining && s.node.Phase == model.PhaseReady })
	if n == nil {
		return
	}
	if c.rng.Float64() < 0.6 {
		c.schedulePodLocked(n)
		return
	}
	for key, p := range n.pods {
		if p.DaemonSet {
			continue
		}
		delete(n.pods, key)
		c.store.DeletePod(key)
		break
	}
	c.publishLocked(n)
}

func (c *Cluster) drainOneLocked() {
	n := c.pickLocked(func(s *simNode) bool { return !s.draining && s.node.Phase == model.PhaseReady })
	if n == nil {
		return
	}
	n.draining = true
	n.node.Phase = model.PhaseDraining
	n.node.Schedulable = false
	n.node.Message = "disrupted: underutilized"
	jitter := time.Duration(c.rng.Int63n(int64(c.opts.DrainFor/2) + 1))
	n.deleteAt = time.Now().Add(c.opts.DrainFor + jitter)
	c.publishLocked(n)
}

// stepDrainLocked evicts one pod per tick and finally deletes the node, so the
// box empties out before it disappears.
func (c *Cluster) stepDrainLocked(n *simNode, now time.Time) {
	for key, p := range n.pods {
		if p.Phase != model.PodTerminating {
			p.Phase = model.PodTerminating
			c.store.UpsertPod(p)
			c.publishLocked(n)
			return
		}
		if c.rng.Float64() < 0.5 {
			delete(n.pods, key)
			c.store.DeletePod(key)
			c.publishLocked(n)
			return
		}
	}
	if now.After(n.deleteAt) {
		if n.node.Phase != model.PhaseDeleting {
			n.node.Phase = model.PhaseDeleting
			n.node.Message = "terminating"
			c.publishLocked(n)
			return
		}
		for key := range n.pods {
			delete(n.pods, key)
			c.store.DeletePod(key)
		}
		delete(c.nodes, n.node.Name)
		c.store.DeleteClaim(n.claim)
		c.store.DeleteNode(n.node.Name)
	}
}

func (c *Cluster) pickLocked(pred func(*simNode) bool) *simNode {
	var candidates []*simNode
	for _, n := range c.nodes {
		if pred(n) {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[c.rng.Intn(len(candidates))]
}

func (c *Cluster) readyCountLocked() int {
	count := 0
	for _, n := range c.nodes {
		if n.node.Phase == model.PhaseReady && !n.draining {
			count++
		}
	}
	return count
}

// publishLocked pushes the node and a plausible usage sample. Usage tracks
// requests with jitter, so meters move even without a metrics server.
func (c *Cluster) publishLocked(n *simNode) {
	published := cloneNode(n.node)
	published.NodeClaim = "" // as the Node informer would deliver it
	c.store.UpsertNode(published)
	var req model.Resources
	for _, p := range n.pods {
		if p.Phase.Active() {
			req = req.Add(p.Requests)
		}
	}
	jitter := 0.55 + c.rng.Float64()*0.5
	c.store.SetNodeUsage(n.node.Name, model.Resources{
		CPUMilli: int64(float64(req.CPUMilli) * jitter),
		MemBytes: int64(float64(req.MemBytes) * (0.6 + c.rng.Float64()*0.35)),
	})
}

// after queues fn to run on the next tick, under the lock. The simulation is
// single-threaded by design; spawning goroutines per pod would make it
// non-reproducible.
func (c *Cluster) after(fn func()) { c.pending = append(c.pending, fn) }

// cloneNode copies a simulated node for publication.
//
// forNode drops NodeClaim, because a real corev1.Node carries no reference back
// to its NodeClaim — the link only exists on the claim's status.nodeName. The
// simulation used to publish it anyway, which quietly exercised a store path
// production never takes and hid a duplicate-node bug.
func cloneNode(n *model.Node) *model.Node {
	cp := *n
	cp.Labels = make(map[string]string, len(n.Labels))
	for k, v := range n.Labels {
		cp.Labels[k] = v
	}
	cp.Pods = nil
	return &cp
}

const suffixChars = "bcdfghjklmnpqrstvwxz2456789"

func randSuffix(rng *rand.Rand) string {
	b := make([]byte, 5)
	for i := range b {
		b[i] = suffixChars[rng.Intn(len(suffixChars))]
	}
	return string(b)
}
