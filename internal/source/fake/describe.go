package fake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// maxSimEvents caps a simulated node's history. A long demo would otherwise
// accumulate an unbounded event list per node, and no one scrolls back that far.
const maxSimEvents = 150

// DescribeNode returns the simulated node's describe payload, including the
// event history the simulation has been recording as it ran.
//
// The nodeClaim argument is ignored: the simulation records claim events into
// the same per-node history (tagged with Kind "NodeClaim"), because it knows
// which claim belongs to which node and the live source has to be told.
func (c *Cluster) DescribeNode(_ context.Context, name, claim string) (*model.NodeDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[name]
	if !ok {
		// The name may be a claim's rather than a node's: until kubelet registers,
		// the box on screen is the NodeClaim and that is the only name the pane has.
		n, ok = c.byClaimLocked(name, claim)
		if !ok {
			return nil, fmt.Errorf("node %s is no longer in the simulated cluster", name)
		}
		// Asked about the claim, and the claim is all there is to answer with: a
		// real cluster has no Node object under this name yet.
		return c.describeClaimLocked(n, name), nil
	}

	d := &model.NodeDetail{
		Name:        name,
		Kind:        "Node",
		ProviderID:  n.node.ProviderID,
		FetchedAt:   time.Now(),
		Allocatable: n.node.Allocatable,
		// Capacity is allocatable plus the reservation launchLocked subtracted,
		// so the gap between the two reads the way it does on a real node.
		Capacity: n.node.Allocatable.Add(model.Resources{CPUMilli: 200, MemBytes: 1 << 30}),
		Labels:   copyMap(n.node.Labels),
		Annotations: map[string]string{
			"karpenter.oxide.computer/hourly-price":                  fmt.Sprintf("%.3f", n.node.Price),
			"karpenter.sh/nodepool-hash":                             "8452743757904667476",
			"volumes.kubernetes.io/controller-managed-attach-detach": "true",
		},
		System: model.SystemInfo{
			OSImage:          "Amazon Linux 2",
			Kernel:           "5.10.219-208.866.amzn2.x86_64",
			ContainerRuntime: "containerd://1.7.11",
			Kubelet:          "v1.30.4-eks-a737599",
			KubeProxy:        "v1.30.4-eks-a737599",
			OS:               "linux",
			Arch:             n.node.Arch,
		},
		Addresses: []model.Address{
			{Type: "InternalIP", Address: fakeIP(name)},
			{Type: "Hostname", Address: name},
		},
		Events: append([]model.Event(nil), n.events...),
	}

	ready, reason, message := "True", "KubeletReady", "kubelet is posting ready status"
	if n.node.Phase == model.PhaseProvisioning {
		ready, reason, message = "Unknown", "KubeletNotReady", "container runtime status check may not have completed yet"
	}
	d.Conditions = []model.Condition{
		{Type: "Ready", Status: ready, Reason: reason, Message: message, Changed: n.readyAt},
		{Type: "MemoryPressure", Status: "False", Reason: "KubeletHasSufficientMemory",
			Message: "kubelet has sufficient memory available", Changed: n.readyAt},
		{Type: "DiskPressure", Status: "False", Reason: "KubeletHasNoDiskPressure",
			Message: "kubelet has no disk pressure", Changed: n.readyAt},
		{Type: "PIDPressure", Status: "False", Reason: "KubeletHasSufficientPID",
			Message: "kubelet has sufficient PID available", Changed: n.readyAt},
	}

	if n.draining {
		d.Taints = append(d.Taints, model.Taint{
			Key: "karpenter.sh/disrupted", Value: "underutilized", Effect: "NoSchedule"})
	}
	if !n.node.Schedulable {
		d.Taints = append(d.Taints, model.Taint{
			Key: "node.kubernetes.io/unschedulable", Effect: "NoSchedule"})
	}

	model.SortEvents(d.Events)
	return d, nil
}

// byClaimLocked finds the simulated node behind a NodeClaim name. Either
// argument may be the claim: the pane passes the box's name and the claim it was
// opened with, and while provisioning those are the same string.
func (c *Cluster) byClaimLocked(name, claim string) (*simNode, bool) {
	for _, sn := range c.nodes {
		if sn.claim == name || (claim != "" && sn.claim == claim) {
			return sn, true
		}
	}
	return nil, false
}

// describeClaimLocked is the provisioning payload: what a NodeClaim can say
// before its Node exists. It mirrors the live source's claim read — capacity,
// labels, the provisioning conditions, the events — and deliberately leaves the
// kubelet and address blocks empty, because on a real cluster there is nothing
// yet to fill them with.
func (c *Cluster) describeClaimLocked(n *simNode, name string) *model.NodeDetail {
	d := &model.NodeDetail{
		Name:        name,
		Kind:        "NodeClaim",
		ProviderID:  n.node.ProviderID,
		FetchedAt:   time.Now(),
		Allocatable: n.node.Allocatable,
		Capacity:    n.node.Allocatable.Add(model.Resources{CPUMilli: 200, MemBytes: 1 << 30}),
		Labels:      copyMap(n.node.Labels),
		Annotations: map[string]string{
			"karpenter.oxide.computer/hourly-price": fmt.Sprintf("%.3f", n.node.Price),
			"karpenter.sh/nodepool-hash":            "8452743757904667476",
		},
		Events: append([]model.Event(nil), n.events...),
	}
	// Launched is the milestone the instance has already passed by the time a box
	// appears; the rest are what the wait is for.
	d.Conditions = []model.Condition{
		{Type: "Launched", Status: "True", Reason: "Launched",
			Message: "Launched instance", Changed: n.born},
		{Type: "Registered", Status: "Unknown", Reason: "NotRegistered",
			Message: "Node not registered with cluster", Changed: n.born},
		{Type: "Initialized", Status: "Unknown", Reason: "NotInitialized",
			Message: "Node not initialized", Changed: n.born},
		{Type: "Ready", Status: "Unknown", Reason: "NotReady",
			Message: "Registered, Initialized", Changed: n.born},
	}
	model.SortEvents(d.Events)
	return d
}

// recordLocked appends one event to a node's history, stamped now.
func (c *Cluster) recordLocked(n *simNode, kind, typ, reason, component, message string) {
	c.recordAtLocked(n, time.Now(), kind, typ, reason, component, message)
}

// recordAtLocked appends one event at a given time, so a seeded node can be
// given a history that predates the process.
func (c *Cluster) recordAtLocked(n *simNode, at time.Time, kind, typ, reason, component, message string) {
	object := n.node.Name
	if kind == "NodeClaim" {
		object = n.claim
	}
	n.events = append(n.events, model.Event{
		Kind:      kind,
		Object:    object,
		Type:      typ,
		Reason:    reason,
		Component: component,
		Message:   message,
		Count:     1,
		First:     at,
		Last:      at,
	})
	if over := len(n.events) - maxSimEvents; over > 0 {
		n.events = append(n.events[:0], n.events[over:]...)
	}
}

// consolidationVerdictLocked gives one node Karpenter's disruption verdict, as
// both an event in its history and a signal in the store — the same two places a
// real cluster's verdict shows up, so the table column and the detail pane are
// driven by the simulation exactly as they are by a cluster.
//
// The verdict follows the node's own utilisation, because a demo where the empty
// nodes are the consolidatable ones is the only version of this that teaches
// anything.
func (c *Cluster) consolidationVerdictLocked(n *simNode) {
	if n.draining || n.node.Phase != model.PhaseReady {
		return
	}
	var req model.Resources
	for _, p := range n.pods {
		if p.Phase.Active() {
			req = req.Add(p.Requests)
		}
	}
	cpu, _ := req.Frac(n.node.Allocatable)

	state, reason, message := model.ConsolidationNo, "Unconsolidatable", "Can't remove without creating 1 candidate"
	if cpu < consolidatableBelow {
		state, reason, message = model.ConsolidationYes, "ConsolidationCandidate",
			fmt.Sprintf("Consolidation candidate: %s underutilized at %.0f%% cpu", n.node.InstanceType, cpu*100)
	}
	// Karpenter reports against the NodeClaim it is reasoning about, not the Node.
	// Doing the same here keeps the store's claim-to-node join under test.
	c.recordLocked(n, "NodeClaim", "Normal", reason, "karpenter", message)
	c.store.SetConsolidation(n.claim, state, message, time.Now())
}

// consolidatableBelow is the CPU-request fraction under which the simulation
// calls a node a consolidation candidate.
const consolidatableBelow = 0.4

// launchHistoryLocked records the provisioning sequence for a node whose birth
// the caller has backdated. seed() pre-ages its nodes so the first frame looks
// like a running cluster, and events stamped with the wall clock would show a
// ten-hour-old node registering with kubelet a second ago.
func (c *Cluster) launchHistoryLocked(n *simNode) {
	n.events = nil
	born := n.node.Created
	c.recordAtLocked(n, born, "NodeClaim", "Normal", "Launched", "karpenter",
		fmt.Sprintf("Launched instance: %s, %s, %s", n.node.InstanceType, n.node.CapacityType, n.node.Zone))
	c.recordAtLocked(n, born.Add(24*time.Second), "NodeClaim", "Normal", "Registered", "karpenter",
		fmt.Sprintf("Status condition transitioned, Type: Registered, Status: Unknown -> True, Reason: Registered, Node: %s", n.node.Name))
	c.recordAtLocked(n, born.Add(25*time.Second), "Node", "Normal", "Starting", "kubelet", "Starting kubelet.")
	c.recordAtLocked(n, born.Add(31*time.Second), "Node", "Normal", "NodeReady", "kubelet",
		fmt.Sprintf("Node %s status is now: NodeReady", n.node.Name))
	c.recordAtLocked(n, born.Add(32*time.Second), "Node", "Normal", "RegisteredNode", "node-controller",
		fmt.Sprintf("Node %s event: Registered Node %s in Controller", n.node.Name, n.node.Name))
}

// fakeIP turns "ip-10-3-42-7" back into the address the name encodes, which is
// what makes the addresses block look like a real node's rather than filler.
func fakeIP(name string) string {
	return strings.ReplaceAll(strings.TrimPrefix(name, "ip-"), "-", ".")
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
