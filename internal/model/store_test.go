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

func TestPhaseHistoryPreservesTransitionsBetweenSnapshots(t *testing.T) {
	s := NewStore("test")
	n := registeredNode("node-a")
	n.Created = time.Now().Add(-time.Hour)
	s.UpsertNode(n)

	cordoned := *n
	cordoned.Phase = PhaseCordoned
	cordoned.Schedulable = false
	s.UpsertNode(&cordoned)

	draining := cordoned
	draining.Phase = PhaseDraining
	s.UpsertNode(&draining)

	snap := s.Snapshot()
	got := snap.Nodes[0].Transitions
	want := []Phase{PhaseReady, PhaseCordoned, PhaseDraining}
	if len(got) != len(want) {
		t.Fatalf("transitions = %v, want phases %v", got, want)
	}
	for i := range want {
		if got[i].Phase != want[i] {
			t.Errorf("transition %d = %v, want %v", i, got[i].Phase, want[i])
		}
	}

	// Snapshot history is immutable even as the live store advances.
	terminating := draining
	terminating.Phase = PhaseTerminating
	s.UpsertNode(&terminating)
	if len(snap.Nodes[0].Transitions) != len(want) {
		t.Fatal("a later upsert mutated transition history in an existing snapshot")
	}
}

func TestTombstoneRetainsTerminatingHistory(t *testing.T) {
	s := NewStore("test")
	n := registeredNode("node-a")
	n.Phase = PhaseTerminating
	s.UpsertNode(n)
	s.DeleteNode(n.Name)

	got := s.Snapshot().Nodes[0]
	if got.Phase != PhaseGone {
		t.Fatalf("phase = %v, want removed tombstone", got.Phase)
	}
	if len(got.Transitions) != 2 || got.Transitions[0].Phase != PhaseTerminating || got.Transitions[1].Phase != PhaseGone {
		t.Fatalf("tombstone history = %v, want Terminating -> Removed", got.Transitions)
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
	transitions := snap.Nodes[0].Transitions
	if len(transitions) != 2 || transitions[0].Phase != PhaseProvisioning || transitions[1].Phase != PhaseReady {
		t.Fatalf("claim handover history = %v, want Provisioning -> Ready", transitions)
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

func TestNodeClaimDisruptionReasonSurvivesHandover(t *testing.T) {
	s := NewStore("test")
	const name = "karpenter-default-disrupting"
	claim := claimPlaceholder(name, name)
	claim.DisruptionReason = "Underutilized"
	s.UpsertClaim(name, claim)
	s.UpsertNode(registeredNode(name))

	got := s.Snapshot().Nodes[0]
	if got.DisruptionReason != "Underutilized" {
		t.Fatalf("disruption reason after claim handover = %q, want Underutilized", got.DisruptionReason)
	}
	cleared := claimPlaceholder(name, name)
	s.UpsertClaim(name, cleared)
	if got := s.Snapshot().Nodes[0].DisruptionReason; got != "" {
		t.Fatalf("cleared disruption reason remained %q", got)
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
// TestClaimBeforeNodeNameResolves is the window: Karpenter creates the claim,
// the instance boots and kubelet registers the Node under its own name, and
// only on a later reconcile does Karpenter write status.nodeName. Until then the
// placeholder is named after the claim and matches the Node on neither name nor
// NodeClaim, so both were drawn — for as long as that reconcile took. providerID
// is on both objects the whole time and closes it.
func TestClaimBeforeNodeNameResolves(t *testing.T) {
	s := NewStore("test")
	const claim, node = "default-abc12", "ip-10-0-1-5"
	const provider = "aws:///us-west-2a/i-0abc123"

	// status.nodeName not set yet: the placeholder is named after the claim.
	c := claimPlaceholder(claim, claim)
	c.ProviderID = provider
	s.UpsertClaim(claim, c)

	n := registeredNode(node)
	n.ProviderID = provider
	s.UpsertNode(n)

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
	got := snap.Nodes[0]
	if got.Name != node {
		t.Errorf("kept %q, want the registered node %q", got.Name, node)
	}
	if got.Phase != PhaseReady {
		t.Errorf("phase = %v, want the real node's %v", got.Phase, PhaseReady)
	}
	if got.NodeClaim != claim {
		t.Errorf("claim name was lost: %q", got.NodeClaim)
	}
	if !got.HasPrice {
		t.Error("price from the NodeClaim was lost")
	}

	// The late reconcile finally names the node. It must not resurrect anything.
	resolved := claimPlaceholder(claim, node)
	resolved.ProviderID = provider
	s.UpsertClaim(claim, resolved)
	snap = s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("late claim update re-added a box: %v", names(snap))
	}
}

// The snapshot must not draw the same machine twice even if the write-path join
// missed it, so the invariant does not rest on reconciliation being perfect.
func TestSnapshotDedupesByProviderID(t *testing.T) {
	s := NewStore("test")
	const provider = "aws:///us-west-2a/i-0abc123"

	n := registeredNode("ip-10-0-1-5")
	n.ProviderID = provider
	s.UpsertNode(n)

	// Reach past UpsertClaim to plant the state it would have reconciled away.
	orphan := claimPlaceholder("default-abc12", "default-abc12")
	orphan.ProviderID = provider
	s.mu.Lock()
	s.claims["default-abc12"] = orphan
	s.mu.Unlock()

	snap := s.Snapshot()
	assertNoDuplicates(t, snap)
	if len(snap.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
	}
}

// A node's age must be continuous across the claim-to-node handover. The Node's
// own creationTimestamp is kubelet-registration time, a minute or so after
// Karpenter created the claim, so taking it at face value made the age column
// jump backwards at the exact moment the box went from provisioning to ready.
func TestAgeStartsAtClaimCreation(t *testing.T) {
	const claim, node = "default-abc12", "ip-10-0-1-5"
	claimBorn := time.Now().Add(-5 * time.Minute)
	registered := time.Now().Add(-4 * time.Minute)

	withClaimAge := func() *Node {
		c := claimPlaceholder(claim, node)
		c.Created = claimBorn
		return c
	}
	withNodeAge := func() *Node {
		n := registeredNode(node)
		n.Created = registered
		return n
	}

	for _, tc := range []struct {
		name string
		play func(*Store)
	}{
		{"claim then node", func(s *Store) { s.UpsertClaim(claim, withClaimAge()); s.UpsertNode(withNodeAge()) }},
		{"node then claim", func(s *Store) { s.UpsertNode(withNodeAge()); s.UpsertClaim(claim, withClaimAge()) }},
		// A resync replays the Node's Add long after the claim is gone; the age
		// must not regress just because the placeholder is no longer around.
		{"resync after adoption", func(s *Store) {
			s.UpsertClaim(claim, withClaimAge())
			s.UpsertNode(withNodeAge())
			s.UpsertNode(withNodeAge())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore("test")
			tc.play(s)
			snap := s.Snapshot()
			if len(snap.Nodes) != 1 {
				t.Fatalf("got %d nodes, want 1: %v", len(snap.Nodes), names(snap))
			}
			if got := snap.Nodes[0].Created; !got.Equal(claimBorn) {
				t.Errorf("age starts at %v, want the claim's %v", got, claimBorn)
			}
		})
	}
}

// A node with no claim keeps its own timestamp: earliest() must not let a
// placeholder's zero Created win by being "older" than everything.
func TestAgeIgnoresMissingClaimTimestamp(t *testing.T) {
	s := NewStore("test")
	const claim, node = "default-abc12", "ip-10-0-1-5"

	s.UpsertClaim(claim, claimPlaceholder(claim, node)) // no Created, as an early claim event has
	want := time.Now().Add(-time.Minute)
	n := registeredNode(node)
	n.Created = want
	s.UpsertNode(n)

	if got := s.Snapshot().Nodes[0].Created; !got.Equal(want) {
		t.Errorf("Created = %v, want the node's own %v", got, want)
	}
}

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
