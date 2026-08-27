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
	// PhaseTerminating means a deletionTimestamp is set on the Node or NodeClaim.
	// Kubernetes is deleting the object, but "terminating" describes the
	// observable state and matches the vocabulary used for pods.
	PhaseTerminating
	// PhaseGone is a tombstone: the object is out of the informer cache and we
	// are holding it briefly so the UI can animate it away.
	PhaseGone
)

var phaseNames = [...]string{"Provisioning", "NotReady", "Ready", "Cordoned", "Draining", "Terminating", "Removed"}

func (p Phase) String() string {
	if int(p) < len(phaseNames) {
		return phaseNames[p]
	}
	return "Unknown"
}

// Terminal reports whether the node is on its way out. Used to pick animations
// and to exclude the node from "healthy capacity" totals.
func (p Phase) Terminal() bool { return p >= PhaseDraining }

// PhaseTransition is one phase the store observed and when it observed it.
// Transitions are recorded before snapshot coalescing, so a fast live sequence
// such as Cordoned -> Draining is still available to the renderer even when no
// frame was emitted for the intermediate state.
type PhaseTransition struct {
	Phase Phase
	At    time.Time
}

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
	Owner string
	// Unschedulable means the scheduler has actively refused this pod
	// (PodScheduled=False, reason Unschedulable) rather than simply not having
	// got round to it. That distinction is the whole point of tracking pending
	// pods: one waiting a beat is normal, one the scheduler has rejected is a
	// capacity problem, and only the second is what a provisioner reacts to.
	Unschedulable bool
	Requests      Resources
	Limits        Resources
	Usage         Resources
	HasUsage      bool
	Created       time.Time
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
	// ProviderID is the cloud instance ID ("aws:///us-west-2a/i-0abc"). It is the
	// only field a Node and its NodeClaim share from the moment each is created,
	// which makes it the key that joins a provisioning placeholder to the real
	// node without waiting on Karpenter to write status.nodeName.
	ProviderID  string
	Ready       bool
	Schedulable bool
	Phase       Phase
	// Transitions is a short, ordered observation history ending at Phase. It is
	// display evidence, not a delay: Phase always remains the latest cluster fact.
	Transitions []PhaseTransition
	// Message is a short human reason for the current phase, shown on the
	// selected node's detail line ("disrupted: underutilized").
	Message string
	// DisruptionReason is Karpenter's NodeClaim condition reason while the claim
	// is actively being disrupted (for example Underutilized or Drifted). Unlike
	// a consolidatable verdict, this is evidence that an action is underway.
	DisruptionReason string

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

	// Consolidatable is Karpenter's latest word on whether this node could be
	// removed, ConsolidationReason is the message behind it, and ConsolidationAt
	// is when it said so. See the Consolidation type: this is a reported fact
	// with an age, not a computed one.
	Consolidatable      Consolidation
	ConsolidationReason string
	ConsolidationAt     time.Time

	Labels map[string]string
	Pods   []*Pod
}

// Util returns the request fractions the UI draws. Requests are the scheduler's
// input, so the capacity view has one fixed meaning rather than a configurable
// interpretation.
func (n *Node) Util() (cpu, mem float64) {
	return n.Requests.Frac(n.Allocatable)
}

// Consolidation is whether Karpenter considers a node removable.
//
// There is no field for this anywhere in the API. The decision lives in
// Karpenter's disruption loop and surfaces only as an event — ConsolidationCandidate
// when a node is a candidate for removal, Unconsolidatable when it looked and
// could not. So this is a *reported* fact with an age rather than a computed one,
// which is why it carries a timestamp, expires after ConsolidationTTL, and has an
// unknown state that means "Karpenter has not said" rather than "no".
type Consolidation int

const (
	ConsolidationUnknown Consolidation = iota
	ConsolidationYes
	ConsolidationNo
)

// ConsolidationTTL is how long a verdict is trusted. Karpenter re-evaluates
// continuously and re-emits, so silence for this long means the last verdict is
// no longer evidence of anything — and a stale "yes" on a node that has since
// filled up would be worse than admitting we do not know.
const ConsolidationTTL = 30 * time.Minute

func (c Consolidation) String() string {
	switch c {
	case ConsolidationYes:
		return "yes"
	case ConsolidationNo:
		return "no"
	default:
		return "unknown"
	}
}

// Short is the single cell the dense table draws: y, n, or a dot for unknown.
func (c Consolidation) Short() string {
	switch c {
	case ConsolidationYes:
		return "y"
	case ConsolidationNo:
		return "n"
	default:
		return "·"
	}
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
	// Totals covers every non-tombstoned node, before UI filters.
	Totals       Totals
	HasKarpenter bool
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
	// Pending counts pods that exist but have no node, and Unschedulable the
	// subset the scheduler has refused outright. Neither is counted in Pods:
	// that is what is actually placed, and a backlog is precisely the thing
	// that is not.
	Pending       int
	Unschedulable int
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

// secondsUntil is where HumanAge stops counting seconds and starts counting
// minutes. kubectl switches at 60s; this tool holds on to seconds until 100
// because the thing it exists to show — an instance going from launched to
// Ready — takes about ninety of them. "1m" for the last ten seconds of that
// wait throws away the only resolution that matters, and by the time a node is
// past it nobody is reading the age to the second anyway.
const secondsUntil = 100 * time.Second

// HumanAge renders a duration in the two-character-ish style kubectl uses,
// except for the seconds range — see secondsUntil.
func HumanAge(d time.Duration) string {
	switch {
	case d < secondsUntil:
		if d < 0 {
			d = 0
		}
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
