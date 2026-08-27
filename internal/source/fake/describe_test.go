package fake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

func TestDescribeSeededNodeReadsAsHistory(t *testing.T) {
	c, store := New(Options{Nodes: 4, Seed: 5})
	c.seed()

	name := store.Snapshot().Nodes[0].Name
	d, err := c.DescribeNode(context.Background(), name, "")
	if err != nil {
		t.Fatal(err)
	}

	if d.Name != name {
		t.Fatalf("described %s, want %s", d.Name, name)
	}
	if len(d.Conditions) == 0 || len(d.Addresses) == 0 || d.System.Kubelet == "" {
		t.Fatal("describe payload is missing the static blocks")
	}
	if d.Capacity.CPUMilli <= d.Allocatable.CPUMilli {
		t.Fatal("capacity must exceed allocatable, or the reservation is invisible")
	}
	if len(d.Events) < 3 {
		t.Fatalf("got %d events, want the launch sequence", len(d.Events))
	}
	// Newest first, and backdated: a node presented as hours old must not claim
	// kubelet registered a moment ago.
	for i := 1; i < len(d.Events); i++ {
		if d.Events[i].When().After(d.Events[i-1].When()) {
			t.Fatalf("event %d is newer than the one before it", i)
		}
	}
	oldest := d.Events[len(d.Events)-1]
	if age := time.Since(oldest.When()); age < time.Minute {
		t.Fatalf("seeded node's oldest event is %s old; expected a backdated history", age)
	}
	if oldest.Reason != "Launched" || oldest.Kind != "NodeClaim" {
		t.Fatalf("history does not begin with the claim's launch: %+v", oldest)
	}
}

func TestDescribeRecordsDrainAsItHappens(t *testing.T) {
	c, store := New(Options{Nodes: 3, Seed: 9, DrainFor: 50 * time.Millisecond})
	c.seed()
	c.DrainOne()

	name := ""
	for _, n := range store.Snapshot().Nodes {
		if n.Phase == model.PhaseDraining {
			name = n.Name
		}
	}
	if name == "" {
		t.Fatal("nothing is draining")
	}

	d, err := c.DescribeNode(context.Background(), name, "")
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]bool{}
	for _, ev := range d.Events {
		reasons[ev.Reason] = true
	}
	for _, want := range []string{"Launched", "NodeReady", "DisruptionTerminating", "NodeNotSchedulable"} {
		if !reasons[want] {
			t.Fatalf("drain history is missing %s: have %v", want, reasons)
		}
	}
	// A draining node is tainted, and the pane is where you go to see why.
	taints := make([]string, 0, len(d.Taints))
	for _, tn := range d.Taints {
		taints = append(taints, tn.String())
	}
	if !strings.Contains(strings.Join(taints, " "), "karpenter.sh/disrupted") {
		t.Fatalf("draining node has no disruption taint: %v", taints)
	}

	// A node that has left the cluster is an error rather than an empty pane.
	if _, err := c.DescribeNode(context.Background(), "ip-0-0-0-0", ""); err == nil {
		t.Fatal("describing an unknown node succeeded")
	}
}

// TestConsolidationVerdictReachesSnapshot covers the whole path the table column
// depends on: the simulation reports against the NodeClaim, exactly as Karpenter
// does, and the store has to join it back to the node.
func TestConsolidationVerdictReachesSnapshot(t *testing.T) {
	c, store := New(Options{Nodes: 6, Seed: 13})
	c.seed()

	verdicts := map[model.Consolidation]int{}
	for _, n := range store.Snapshot().Nodes {
		verdicts[n.Consolidatable]++
		if n.Consolidatable == model.ConsolidationUnknown {
			t.Fatalf("node %s has no verdict; the seeded cluster should judge every node", n.Name)
		}
		if n.ConsolidationReason == "" || n.ConsolidationAt.IsZero() {
			t.Fatalf("node %s has a verdict with no message or timestamp: %+v", n.Name, n)
		}
	}
	// The verdict has to be a judgement, not a constant: a demo where every node
	// says the same thing teaches nothing.
	if len(verdicts) < 2 {
		t.Fatalf("every seeded node got the same verdict: %v", verdicts)
	}

	// And it is recorded in the node's own history, where the pane shows it.
	name := store.Snapshot().Nodes[0].Name
	d, err := c.DescribeNode(context.Background(), name, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range d.Events {
		if ev.Reason == "ConsolidationCandidate" || ev.Reason == "Unconsolidatable" {
			found = ev.Kind == "NodeClaim"
		}
	}
	if !found {
		t.Fatal("no consolidation event on the node's claim in its history")
	}
}

func TestDescribeEventHistoryIsBounded(t *testing.T) {
	c, _ := New(Options{Nodes: 1, Seed: 2})
	c.seed()
	c.mu.Lock()
	var n *simNode
	for _, sn := range c.nodes {
		n = sn
	}
	for i := 0; i < maxSimEvents*3; i++ {
		c.recordLocked(n, "Node", "Normal", "Churn", "kubelet", "noise")
	}
	got := len(n.events)
	c.mu.Unlock()
	if got > maxSimEvents {
		t.Fatalf("history grew to %d events, cap is %d", got, maxSimEvents)
	}
}

// TestDescribeAProvisioningClaim covers the pane's first thirty seconds: the box
// on screen is a NodeClaim, the only name the pane has is the claim's, and there
// is no Node object to read. The claim still has to answer with something worth
// reading.
func TestDescribeAProvisioningClaim(t *testing.T) {
	c, store := New(Options{Nodes: 1, Seed: 3})
	c.seed()
	c.ScaleUp(1)

	claim := ""
	for _, n := range store.Snapshot().Nodes {
		if n.Phase == model.PhaseProvisioning {
			claim = n.Name
		}
	}
	if claim == "" {
		t.Fatal("scale-up produced no provisioning box")
	}

	d, err := c.DescribeNode(context.Background(), claim, claim)
	if err != nil {
		t.Fatalf("describing a claim failed: %v", err)
	}
	if d.Kind != "NodeClaim" {
		t.Fatalf("kind = %q, want NodeClaim", d.Kind)
	}
	if d.Allocatable.CPUMilli == 0 || d.Capacity.CPUMilli <= d.Allocatable.CPUMilli {
		t.Fatalf("claim carries no usable capacity: %+v", d)
	}
	if len(d.Conditions) == 0 {
		t.Fatal("claim has no conditions; the pane would have nothing to show for the wait")
	}
	if d.System.Kubelet != "" || len(d.Addresses) != 0 {
		t.Fatal("claim payload invented node-only fields")
	}
	if d.ProviderID == "" {
		t.Fatal("claim payload dropped the instance id")
	}
	if len(d.Events) == 0 {
		t.Fatal("claim payload has no events")
	}
}

// TestDescribeSurvivesTheClaimToNodeRename is the handover from the source's
// side: once kubelet registers, the same claim name must still resolve — the pane
// may not have seen the new snapshot yet — and the node's own name must answer
// with the node.
func TestDescribeSurvivesTheClaimToNodeRename(t *testing.T) {
	c, _ := New(Options{Nodes: 1, Seed: 4})
	c.seed()
	c.mu.Lock()
	var n *simNode
	for _, sn := range c.nodes {
		n = sn
	}
	claim, node := n.claim, n.node.Name
	c.mu.Unlock()

	byClaim, err := c.DescribeNode(context.Background(), claim, claim)
	if err != nil {
		t.Fatalf("claim name stopped resolving: %v", err)
	}
	if byClaim.Kind != "NodeClaim" {
		t.Fatalf("claim name described a %s", byClaim.Kind)
	}
	byNode, err := c.DescribeNode(context.Background(), node, claim)
	if err != nil {
		t.Fatal(err)
	}
	if byNode.Kind != "Node" || byNode.System.Kubelet == "" {
		t.Fatalf("node name did not describe the node: %+v", byNode)
	}
}
