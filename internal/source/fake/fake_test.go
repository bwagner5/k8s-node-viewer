package fake

import (
	"context"
	"testing"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// These exercise the whole data path the UI depends on — source writes, store
// coalescing, snapshot construction — without needing a cluster.

func TestSeededClusterProducesUsableSnapshot(t *testing.T) {
	c, store := New(Options{Nodes: 8, Seed: 7})
	c.seed()

	snap := store.Snapshot()
	if len(snap.Nodes) != 8 {
		t.Fatalf("got %d nodes, want 8", len(snap.Nodes))
	}
	if snap.Totals.Pods == 0 {
		t.Fatal("seeded nodes have no pods")
	}
	if len(snap.NodePools) == 0 {
		t.Fatal("no nodepools published")
	}
	if snap.Totals.Allocatable.CPUMilli == 0 {
		t.Fatal("no allocatable capacity")
	}

	for _, n := range snap.Nodes {
		if n.Phase != model.PhaseReady {
			t.Fatalf("node %s seeded in phase %v, want Ready", n.Name, n.Phase)
		}
		// The store must aggregate pod requests onto the node; the renderer
		// depends on this rather than summing pods itself.
		var want model.Resources
		for _, p := range n.Pods {
			want = want.Add(p.Requests)
		}
		if n.Requests.CPUMilli != want.CPUMilli {
			t.Fatalf("node %s: requests %d, want %d", n.Name, n.Requests.CPUMilli, want.CPUMilli)
		}
		if n.Requests.CPUMilli > n.Allocatable.CPUMilli {
			t.Fatalf("node %s overcommitted: %d > %d", n.Name, n.Requests.CPUMilli, n.Allocatable.CPUMilli)
		}
		if !n.HasUsage {
			t.Fatalf("node %s has no usage sample", n.Name)
		}
	}
}

func TestScaleUpShowsProvisioningBeforeNode(t *testing.T) {
	c, store := New(Options{Nodes: 2, Seed: 3})
	c.seed()
	before := len(store.Snapshot().Nodes)

	c.ScaleUp(2)
	snap := store.Snapshot()
	if len(snap.Nodes) != before+2 {
		t.Fatalf("got %d nodes, want %d", len(snap.Nodes), before+2)
	}
	provisioning := 0
	for _, n := range snap.Nodes {
		if n.Phase == model.PhaseProvisioning {
			provisioning++
		}
	}
	// This is the point of modelling NodeClaims: the box exists before the Node
	// does, so a scale-up is visible immediately.
	if provisioning != 2 {
		t.Fatalf("got %d provisioning nodes, want 2", provisioning)
	}
}

func TestDrainEvictsThenTombstones(t *testing.T) {
	c, store := New(Options{Nodes: 3, Seed: 11, DrainFor: 100 * time.Millisecond})
	c.seed()
	c.DrainOne()

	snap := store.Snapshot()
	draining := ""
	for _, n := range snap.Nodes {
		if n.Phase == model.PhaseDraining {
			draining = n.Name
		}
	}
	if draining == "" {
		t.Fatal("DrainOne did not put any node into Draining")
	}

	// Step until the node is gone, then confirm it lingers as a tombstone so the
	// UI has something to animate out.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.step()
		snap = store.Snapshot()
		var found *model.Node
		for _, n := range snap.Nodes {
			if n.Name == draining {
				found = n
			}
		}
		if found == nil {
			t.Fatalf("node %s vanished without a tombstone", draining)
		}
		if found.Phase == model.PhaseGone {
			if found.DeletedAt.IsZero() {
				t.Fatal("tombstone has no DeletedAt, so exit animation cannot be timed")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s never finished draining (phase %v)", draining, found.Phase)
		}
	}
}

func TestWatchCoalescesBursts(t *testing.T) {
	c, store := New(Options{Nodes: 4, Seed: 5})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := store.Watch(ctx, 50*time.Millisecond)
	if snap := <-ch; snap == nil {
		t.Fatal("Watch did not emit an initial snapshot")
	}

	c.seed()
	for i := 0; i < 200; i++ {
		c.Churn()
	}

	// Hundreds of mutations must not become hundreds of snapshots; the rate
	// limit is what keeps a large rollout from starving the renderer.
	deadline := time.After(400 * time.Millisecond)
	count := 0
	for {
		select {
		case <-ch:
			count++
		case <-deadline:
			if count == 0 {
				t.Fatal("no snapshot emitted after mutations")
			}
			if count > 12 {
				t.Fatalf("emitted %d snapshots in 400ms; coalescing is not working", count)
			}
			return
		}
	}
}

// TestNoDuplicateNodesThroughLifecycle drives the simulation hard and asserts
// the invariant the renderer depends on: one box per node name. Real Nodes and
// NodeClaim placeholders live in separate maps, and a gap in their
// reconciliation shows up on screen as the same node drawn twice.
func TestNoDuplicateNodesThroughLifecycle(t *testing.T) {
	c, store := New(Options{Nodes: 6, Seed: 21, DrainFor: 80 * time.Millisecond})
	c.seed()

	check := func(stage string) {
		t.Helper()
		seen := map[string]int{}
		for _, n := range store.Snapshot().Nodes {
			seen[n.Name]++
			if seen[n.Name] > 1 {
				t.Fatalf("%s: node %q appears %d times in one snapshot", stage, n.Name, seen[n.Name])
			}
		}
	}

	check("seeded")
	for i := 0; i < 120; i++ {
		switch i % 5 {
		case 0:
			c.ScaleUp(1)
		case 2:
			c.Churn()
		case 4:
			c.DrainOne()
		}
		c.step()
		check("step " + itoa(i))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestBurstFillsThenDrainsTheBacklog walks the sequence the pending meter
// exists to show: pods arrive with no node, the scheduler gives up on what does
// not fit, and placing them empties the backlog again.
func TestBurstFillsThenDrainsTheBacklog(t *testing.T) {
	// One small node, so a burst cannot possibly all fit.
	c, store := New(Options{Nodes: 1, Seed: 11})
	c.seed()

	c.Burst(12)
	snap := store.Snapshot()
	if snap.Totals.Pending != 12 {
		t.Fatalf("pending = %d after a burst of 12, want 12", snap.Totals.Pending)
	}
	if snap.Totals.Unschedulable != 0 {
		t.Fatalf("unschedulable = %d immediately, want 0: the scheduler has not tried yet",
			snap.Totals.Unschedulable)
	}

	// Backdate the arrivals rather than sleeping: the wait is a simulated
	// scheduling cycle, not something a test should sit through.
	c.mu.Lock()
	for _, p := range c.backlog {
		p.Created = p.Created.Add(-2 * unschedulableAfter)
	}
	c.mu.Unlock()

	c.step()
	if got := store.Snapshot().Totals.Unschedulable; got == 0 {
		t.Fatal("nothing became unschedulable on a full cluster")
	}

	// Capacity is the answer: scale up and keep ticking until it is absorbed.
	// The new instances are made to have finished booting, so the test measures
	// scheduling rather than the simulated launch delay.
	c.ScaleUp(4)
	c.mu.Lock()
	for _, n := range c.nodes {
		n.readyAt = time.Now().Add(-time.Second)
	}
	c.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for store.Snapshot().Totals.Pending > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("backlog never drained: %d still pending", store.Snapshot().Totals.Pending)
		}
		c.step()
	}
	if got := store.Snapshot().Totals.Unschedulable; got != 0 {
		t.Errorf("unschedulable = %d with an empty backlog, want 0", got)
	}
}
