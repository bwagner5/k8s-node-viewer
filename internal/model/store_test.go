package model

import (
	"testing"
	"time"
)

// A node name must appear at most once per snapshot. Real Nodes and Karpenter
// NodeClaim placeholders live in separate maps, so every path that could let
// both survive for the same node is a duplicate box on screen.

func names(snap *Snapshot) []string {
	out := make([]string, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		out = append(out, n.Name)
	}
	return out
}

func assertNoDuplicates(t *testing.T, snap *Snapshot) {
	t.Helper()
	seen := map[string]int{}
	for _, n := range snap.Nodes {
		seen[n.Name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Fatalf("node %q appears %d times in one snapshot: %v", name, count, names(snap))
		}
	}
}

// claimPlaceholder mimics what the Karpenter informer builds. Crucially it does
// NOT set NodeClaim on the node the Node informer produces, because a real
// corev1.Node carries no reference back to its claim.
func claimPlaceholder(claim, nodeName string) *Node {
	return &Node{
		Name:      nodeName,
		NodeClaim: claim,
		NodePool:  "default",
		Price:     0.5,
		HasPrice:  true,
		Phase:     PhaseProvisioning,
	}
}

func registeredNode(name string) *Node {
	// Exactly what convertNode yields: no NodeClaim, no price.
	return &Node{
		Name:        name,
		Ready:       true,
		Schedulable: true,
		Phase:       PhaseReady,
		Created:     time.Now(),
		Allocatable: Resources{CPUMilli: 4000, MemBytes: 8 << 30, Pods: 110},
	}
}

// TestClaimThenNodeDoesNotDuplicate is the reported bug: at startup the two
// informers sync concurrently, and if the NodeClaim's Add lands before the
// Node's Add, nothing removed the placeholder until the next claim event or the
// 10-minute resync. On a cluster where the node and claim share a name that
// rendered as the same node twice.
func TestClaimThenNodeDoesNotDuplicate(t *testing.T) {
	s := NewStore("test")
	const name = "karpenter-default-ds9mm"

	s.UpsertClaim(name, claimPlaceholder(name, name))
	if got := len(s.Snapshot().Nodes); got != 1 {
		t.Fatalf("claim alone should draw one provisioning box, got %d", got)
	}

	// The Node registers. No further NodeClaim event ever arrives.
	s.UpsertNode(registeredNode(name))

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
	if snap.Nodes[0].Phase != PhaseReady {
		t.Fatalf("the real node must win over its placeholder, got phase %v", snap.Nodes[0].Phase)
	}
}

func TestNodeThenClaimDoesNotDuplicate(t *testing.T) {
	s := NewStore("test")
	const name = "karpenter-default-ds9mm"

	s.UpsertNode(registeredNode(name))
	s.UpsertClaim(name, claimPlaceholder(name, name))

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
}

// TestClaimAdoptionCarriesPricing checks the placeholder is not merely dropped:
// the claim is the only source of pool and price, so that data must survive.
func TestClaimAdoptionCarriesPricing(t *testing.T) {
	s := NewStore("test")
	const claim, node = "default-abc12", "ip-10-0-1-5"

	s.UpsertClaim(claim, claimPlaceholder(claim, node))
	s.UpsertNode(registeredNode(node))

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
	n := snap.Nodes[0]
	if n.Name != node {
		t.Fatalf("kept %q, want the registered node %q", n.Name, node)
	}
	if !n.HasPrice || n.Price != 0.5 {
		t.Errorf("price from the NodeClaim was lost: %v %v", n.HasPrice, n.Price)
	}
	if n.NodePool != "default" {
		t.Errorf("nodepool from the NodeClaim was lost: %q", n.NodePool)
	}
	if n.NodeClaim != claim {
		t.Errorf("claim name was lost: %q", n.NodeClaim)
	}
}

// TestUnresolvedClaimStillShows guards the feature the placeholder exists for:
// a claim with no Node yet must still draw, or scale-ups become invisible.
func TestUnresolvedClaimStillShows(t *testing.T) {
	s := NewStore("test")
	s.UpsertNode(registeredNode("ip-10-0-1-5"))
	s.UpsertClaim("default-pending", claimPlaceholder("default-pending", "default-pending"))

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2: %v", len(snap.Nodes), names(snap))
	}
	var provisioning int
	for _, n := range snap.Nodes {
		if n.Phase == PhaseProvisioning {
			provisioning++
		}
	}
	if provisioning != 1 {
		t.Fatalf("want one provisioning placeholder, got %d", provisioning)
	}
}

// TestTombstonedNodeDoesNotDuplicateWithClaim covers the teardown ordering: the
// Node's delete event can arrive before the NodeClaim's.
func TestTombstonedNodeDoesNotDuplicateWithClaim(t *testing.T) {
	s := NewStore("test")
	const name = "karpenter-default-ds9mm"

	s.UpsertClaim(name, claimPlaceholder(name, name))
	s.UpsertNode(registeredNode(name))
	s.DeleteNode(name)
	// The claim lingers and gets one more update while the node is tombstoned.
	s.UpsertClaim(name, claimPlaceholder(name, name))

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
	if snap.Nodes[0].Phase != PhaseGone {
		t.Fatalf("tombstone must survive so the exit animation can play, got %v", snap.Nodes[0].Phase)
	}
}

// TestClaimRenameDoesNotStrand covers a claim whose status.nodeName appears
// later: the placeholder is first keyed by the claim name, then by the node.
func TestClaimRenameDoesNotStrand(t *testing.T) {
	s := NewStore("test")
	const claim, node = "default-abc12", "ip-10-0-1-5"

	s.UpsertClaim(claim, claimPlaceholder(claim, claim)) // status.nodeName empty
	s.UpsertClaim(claim, claimPlaceholder(claim, node))  // status.nodeName set
	s.UpsertNode(registeredNode(node))

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
}
