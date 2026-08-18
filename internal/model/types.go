// Package model holds the UI-facing domain types and the snapshot store.
//
// Nothing in this package imports client-go. Informers convert Kubernetes API
// objects into these types at the edge, which keeps cache objects (which must
// never be mutated) out of the renderer and makes the whole pipeline testable
// with the fake source.
package model

import (
	"fmt"
	"math"
	"time"
)

// Phase is the visual lifecycle state of a node. It is derived once, in
// DerivePhase, so the renderer never re-interprets taints and conditions.
type Phase int

const (
	// PhaseProvisioning is a Karpenter NodeClaim that has no Node object yet,
	// or a Node that has not yet reported Ready and is still young.
	PhaseProvisioning Phase = iota
	PhaseNotReady
	PhaseReady
	// PhaseCordoned is schedulable=false without any sign of deletion.
	PhaseCordoned
	// PhaseDraining is cordoned plus an active disruption/deletion signal.
	PhaseDraining
	// PhaseDeleting means a deletionTimestamp is set on the Node or NodeClaim.
	PhaseDeleting
	// PhaseGone is a tombstone: the object is out of the informer cache and we
	// are holding it briefly so the UI can animate it away.
	PhaseGone
)

var phaseNames = [...]string{"Provisioning", "NotReady", "Ready", "Cordoned", "Draining", "Deleting", "Gone"}

func (p Phase) String() string {
	if int(p) < len(phaseNames) {
		return phaseNames[p]
	}
	return "Unknown"
}

// Terminal reports whether the node is on its way out. Used to pick animations
// and to exclude the node from "healthy capacity" totals.
func (p Phase) Terminal() bool { return p >= PhaseDraining }

// PodPhase is a flattened pod state; Terminating is not a real Kubernetes pod
// phase but is what an operator actually wants to see.
type PodPhase int

const (
	PodPending PodPhase = iota
	PodRunning
	PodSucceeded
	PodFailed
	PodTerminating
)

var podPhaseNames = [...]string{"Pending", "Running", "Succeeded", "Failed", "Terminating"}

func (p PodPhase) String() string {
	if int(p) < len(podPhaseNames) {
		return podPhaseNames[p]
	}
	return "Unknown"
}

// Active reports whether the pod is occupying capacity on its node.
func (p PodPhase) Active() bool { return p == PodPending || p == PodRunning || p == PodTerminating }

// Resources is a resource vector in canonical integer units. Using milli-cores
// and bytes (rather than resource.Quantity) keeps arithmetic cheap enough to
// run on every frame.
type Resources struct {
	CPUMilli int64
	MemBytes int64
	GPU      int64
	Pods     int64
}

func (r Resources) Add(o Resources) Resources {
	return Resources{r.CPUMilli + o.CPUMilli, r.MemBytes + o.MemBytes, r.GPU + o.GPU, r.Pods + o.Pods}
}

// Frac returns r/of per dimension, clamped to [0,1]; a zero denominator yields 0.
func (r Resources) Frac(of Resources) (cpu, mem float64) {
	return ratio(r.CPUMilli, of.CPUMilli), ratio(r.MemBytes, of.MemBytes)
}

func ratio(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return math.Max(0, math.Min(1, float64(a)/float64(b)))
}

// Pod is a scheduled pod as the viewer cares about it.
type Pod struct {
	Namespace string
	Name      string
	NodeName  string
	Phase     PodPhase
	Ready     bool
	DaemonSet bool
	// Owner is the controller name (ReplicaSet collapsed to its Deployment)
	// and is what the pod-cell colours hash on, so one workload reads as one
	// colour across the cluster.
	Owner    string
	Requests Resources
	Limits   Resources
	Usage    Resources
	HasUsage bool
	Created  time.Time
}

func (p *Pod) Key() string { return p.Namespace + "/" + p.Name }

// Node is a rendered node. Pods is populated at snapshot time.
type Node struct {
	Name         string
	InstanceType string
	Zone         string
	Region       string
	Arch         string
	CapacityType string // "spot" / "on-demand"
	NodePool     string // karpenter.sh/nodepool
	NodeClaim    string
	Ready        bool
	Schedulable  bool
	Phase        Phase
	// Message is a short human reason for the current phase, shown on the
	// selected node's detail line ("disrupted: underutilized").
	Message string

	Created   time.Time
	DeletedAt time.Time // zero unless tombstoned

	Allocatable Resources
	Requests    Resources
	Limits      Resources
	Usage       Resources
	HasUsage    bool

	// Price is the hourly cost if Karpenter published one on the NodeClaim.
	Price    float64
	HasPrice bool

	Labels map[string]string
	Pods   []*Pod
}

// Util returns the fractions the UI draws, honouring the requested basis and
// silently falling back to requests when metrics-server has not reported yet.
func (n *Node) Util(basis Basis) (cpu, mem float64) {
	if basis == BasisUsage && n.HasUsage {
		return n.Usage.Frac(n.Allocatable)
	}
	return n.Requests.Frac(n.Allocatable)
}

// Basis selects which numbers drive the meters.
type Basis int

const (
	BasisRequests Basis = iota
	BasisUsage
)

func (b Basis) String() string {
	if b == BasisUsage {
		return "usage"
	}
	return "requests"
}

// NodePool is the subset of a Karpenter NodePool we display and filter on.
type NodePool struct {
	Name     string
	Limits   Resources
	Weight   int32
	Created  time.Time
	NodeRefs int // observed nodes, filled at snapshot time
}

// Snapshot is an immutable view of the cluster. The store hands a fresh one to
// the UI; the UI may hold it across frames without locking.
type Snapshot struct {
	Generation uint64
	Taken      time.Time
	Nodes      []*Node
	NodePools  []*NodePool
	Namespaces []string
	// Totals covers every non-tombstoned node, before UI filters.
	Totals       Totals
	HasKarpenter bool
	HasMetrics   bool
	Context      string
}

// Totals are cluster-wide aggregates.
type Totals struct {
	Nodes       int
	Pods        int
	Allocatable Resources
	Requests    Resources
	Usage       Resources
	HourlyCost  float64
}

// HumanMem renders bytes with binary units at demo-legible precision.
func HumanMem(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	format := "%.1f%ci"
	if v >= 100 {
		format = "%.0f%ci"
	}
	return fmt.Sprintf(format, v, "KMGTP"[exp])
}

// HumanCPU renders milli-cores as cores.
func HumanCPU(m int64) string {
	if m < 1000 {
		return fmt.Sprintf("%dm", m)
	}
	v := float64(m) / 1000
	if v >= 100 || v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// HumanAge renders a duration in the two-character-ish style kubectl uses.
func HumanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
