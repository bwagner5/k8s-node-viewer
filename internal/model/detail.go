package model

import (
	"sort"
	"time"
)

// NodeDetail is the "describe" payload for a single node: the fields that are
// too large, too static, or too rarely looked at to carry in every snapshot,
// plus the node's event stream.
//
// It is fetched on demand rather than watched. Events are the one thing here
// that is unbounded — a busy cluster's event stream dwarfs its node list — and
// watching all of it to fill a pane that is usually closed would cost far more
// than it is worth. The trade is that the pane can be a few seconds stale,
// which is why it carries FetchedAt and refreshes itself while open.
type NodeDetail struct {
	Name      string
	FetchedAt time.Time

	// Kind is the object the payload was actually read from: "Node" normally,
	// "NodeClaim" for a node that has not registered yet and therefore has no
	// Node object at all. The pane has to say which, because a claim's payload
	// is missing things — kubelet version, addresses — that are absent rather
	// than empty, and because the two objects carry different conditions.
	Kind string
	// ProviderID is the cloud instance ID. On a claim it is the first hard fact
	// about the machine that exists, and the only one you can take to a cloud
	// console while it is still booting.
	ProviderID string

	// Capacity is what the machine has; Allocatable is what is left after
	// kube/system reservations. Showing both is the only way the gap between
	// them — which is where "why won't my 16-core pod schedule" lives — is
	// visible at all.
	Capacity    Resources
	Allocatable Resources

	Conditions  []Condition
	Taints      []Taint
	Addresses   []Address
	System      SystemInfo
	Labels      map[string]string
	Annotations map[string]string

	// Events are sorted newest first: what a node just did is what you opened the
	// pane for, and it should be on the first line rather than at the end of an
	// hour of history.
	Events []Event
	// EventsErr records a failure to read events — RBAC, most often — without
	// failing the whole describe. Everything else is still worth showing, and
	// silently returning an empty list would read as "nothing happened".
	EventsErr string
	// EventsCapped is set when the event list hit the fetch limit, so the pane
	// can say so rather than implying it is complete.
	EventsCapped bool
}

// Condition is one node condition.
type Condition struct {
	Type    string
	Status  string
	Reason  string
	Message string
	Changed time.Time
}

// positiveConditions are the condition types whose healthy value is True. Node
// conditions are almost all pressure or unavailability signals where True is the
// problem, so the exceptions are worth listing rather than the rule — and a
// NodeClaim's conditions, which are milestones on the way to Ready, are all
// exceptions.
var positiveConditions = map[string]bool{
	"Ready":       true,
	"Launched":    true,
	"Registered":  true,
	"Initialized": true,
	"Consistent":  true,
}

// Bad reports whether a condition is in the state an operator should look at.
func (c Condition) Bad() bool {
	if positiveConditions[c.Type] {
		return c.Status != "True"
	}
	return c.Status == "True"
}

// Taint is a node taint in its kubectl spelling (key=value:Effect).
type Taint struct {
	Key    string
	Value  string
	Effect string
}

func (t Taint) String() string {
	s := t.Key
	if t.Value != "" {
		s += "=" + t.Value
	}
	if t.Effect != "" {
		s += ":" + t.Effect
	}
	return s
}

// Address is one entry from a node's status.addresses.
type Address struct {
	Type    string
	Address string
}

// SystemInfo is the subset of status.nodeInfo worth a line on screen.
type SystemInfo struct {
	OSImage          string
	Kernel           string
	ContainerRuntime string
	Kubelet          string
	KubeProxy        string
	OS               string
	Arch             string
}

// Event is one API event about a node or its NodeClaim, flattened across the
// two event APIs: the legacy core/v1 shape (FirstTimestamp/LastTimestamp/Count)
// and the events.k8s.io series shape (EventTime plus a series aggregate).
type Event struct {
	// Kind and Object identify what the event was recorded against. A Karpenter
	// cluster records the interesting half of a node's life — the provisioning
	// decision, the disruption decision — against the NodeClaim, not the Node,
	// so the pane shows both and has to say which is which.
	Kind      string
	Object    string
	Type      string // "Normal" or "Warning"
	Reason    string
	Component string
	Message   string
	Count     int32
	First     time.Time
	Last      time.Time
}

// When is the time the event is ordered by: its most recent occurrence.
func (e Event) When() time.Time {
	if !e.Last.IsZero() {
		return e.Last
	}
	return e.First
}

// Warning reports whether the event is one the API marked as a warning.
func (e Event) Warning() bool { return e.Type == "Warning" }

// SortEvents puts events in reverse chronological order — newest first — with a
// stable tiebreak so a repeated fetch of the same events cannot reshuffle the
// pane under the reader.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if at, bt := a.When(), b.When(); !at.Equal(bt) {
			return at.After(bt)
		}
		if !a.First.Equal(b.First) {
			return a.First.After(b.First)
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Message < b.Message
	})
}
