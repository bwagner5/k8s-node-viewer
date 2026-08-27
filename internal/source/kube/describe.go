package kube

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// maxEvents bounds one describe. A node that has been flapping for a week can
// have thousands of events, and a pane can only show so many; asking for a
// bounded page keeps the request cheap and predictable on a large cluster.
const maxEvents = 300

// DescribeNode reads one node and its events on demand.
//
// This is the only read path in the package that is not informer-backed, and
// deliberately so: adding an Event informer would watch every event in the
// cluster — by far the highest-volume resource there is — to serve a pane that
// is open for a few seconds at a time.
//
// nodeClaim may be empty. When it is set, its events are fetched too and merged
// into the same stream, because on a Karpenter cluster the launch and
// disruption decisions are recorded against the NodeClaim; a node's own events
// tell you only what kubelet did about them.
func (s *Source) DescribeNode(ctx context.Context, name, nodeClaim string) (*model.NodeDetail, error) {
	n, err := s.clients.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) || nodeClaim == "" {
			return nil, fmt.Errorf("get node %s: %w", name, err)
		}
		// No Node object: this is a NodeClaim that has not registered yet, which is
		// the half-minute this whole tool exists to make visible. The claim itself
		// carries most of what the pane wants — capacity, labels, the provisioning
		// conditions — plus the only account of what is happening, its events.
		return s.describeClaim(ctx, name, nodeClaim), nil
	}

	d := &model.NodeDetail{
		Name:        n.Name,
		Kind:        "Node",
		ProviderID:  n.Spec.ProviderID,
		FetchedAt:   time.Now(),
		Capacity:    resourcesFromList(n.Status.Capacity),
		Allocatable: resourcesFromList(n.Status.Allocatable),
		Labels:      copyLabels(n.Labels),
		Annotations: copyLabels(n.Annotations),
		System: model.SystemInfo{
			OSImage:          n.Status.NodeInfo.OSImage,
			Kernel:           n.Status.NodeInfo.KernelVersion,
			ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
			Kubelet:          n.Status.NodeInfo.KubeletVersion,
			KubeProxy:        n.Status.NodeInfo.KubeProxyVersion,
			OS:               n.Status.NodeInfo.OperatingSystem,
			Arch:             n.Status.NodeInfo.Architecture,
		},
	}
	for i := range n.Status.Conditions {
		c := &n.Status.Conditions[i]
		d.Conditions = append(d.Conditions, model.Condition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: strings.TrimSpace(c.Message),
			Changed: c.LastTransitionTime.Time,
		})
	}
	for i := range n.Spec.Taints {
		t := &n.Spec.Taints[i]
		d.Taints = append(d.Taints, model.Taint{Key: t.Key, Value: t.Value, Effect: string(t.Effect)})
	}
	for i := range n.Status.Addresses {
		a := &n.Status.Addresses[i]
		d.Addresses = append(d.Addresses, model.Address{Type: string(a.Type), Address: a.Address})
	}

	// Event failures degrade rather than fail: a cluster that lets you read
	// nodes but not events is common, and everything above is still useful.
	events, capped, err := s.objectEvents(ctx, "Node", n.Name)
	if err != nil {
		d.EventsErr = err.Error()
	}
	d.Events, d.EventsCapped = events, capped

	if nodeClaim != "" && s.clients.hasKarpenter {
		claimEvents, claimCapped, err := s.objectEvents(ctx, "NodeClaim", nodeClaim)
		switch {
		case err != nil && d.EventsErr == "":
			d.EventsErr = err.Error()
		case err == nil:
			d.Events = append(d.Events, claimEvents...)
			d.EventsCapped = d.EventsCapped || claimCapped
		}
	}
	model.SortEvents(d.Events)
	return d, nil
}

// describeClaim is the provisioning case: everything the pane can say about a
// node whose only representation is its NodeClaim.
//
// Most of a claim's payload is the same shape as the Node's that will replace it
// — capacity, labels, annotations, taints, conditions — which is the point: the
// pane fills in from the claim now and swaps to the Node when there is one,
// rather than showing an event list on its own for the first ninety seconds.
func (s *Source) describeClaim(ctx context.Context, name, nodeClaim string) *model.NodeDetail {
	d := &model.NodeDetail{Name: name, Kind: "NodeClaim", FetchedAt: time.Now()}

	// A claim read failure is not fatal: the events below are fetched by name and
	// do not depend on it, and they are the half of the pane that matters most.
	if s.clients.hasKarpenter {
		if u, err := s.clients.dynamic.Resource(nodeClaimGVR).
			Get(ctx, nodeClaim, metav1.GetOptions{}); err == nil {
			applyClaim(d, u)
		}
	}

	events, capped, err := s.objectEvents(ctx, "NodeClaim", nodeClaim)
	if err != nil {
		d.EventsErr = err.Error()
	}
	d.Events, d.EventsCapped = events, capped
	model.SortEvents(d.Events)
	return d
}

// applyClaim copies the describable parts of a NodeClaim onto a detail payload.
// Everything it reads is optional: a claim seconds after creation has status
// conditions and nothing else, and one whose instance never launched may not
// even have those.
func applyClaim(d *model.NodeDetail, u *unstructured.Unstructured) {
	d.Labels = copyLabels(u.GetLabels())
	d.Annotations = copyLabels(u.GetAnnotations())
	d.ProviderID, _, _ = unstructured.NestedString(u.Object, "status", "providerID")
	if cap, found, _ := unstructured.NestedStringMap(u.Object, "status", "capacity"); found {
		d.Capacity = quantityMap(cap)
	}
	if alloc, found, _ := unstructured.NestedStringMap(u.Object, "status", "allocatable"); found {
		d.Allocatable = quantityMap(alloc)
	}

	// A claim's conditions are the provisioning milestones — Launched, Registered,
	// Initialized, then Ready — so they read as a progress report on exactly the
	// wait the pane was opened during.
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(c, "type")
		if typ == "" {
			continue
		}
		status, _, _ := unstructured.NestedString(c, "status")
		reason, _, _ := unstructured.NestedString(c, "reason")
		message, _, _ := unstructured.NestedString(c, "message")
		changed := ""
		if s, found, _ := unstructured.NestedString(c, "lastTransitionTime"); found {
			changed = s
		}
		cond := model.Condition{
			Type:    typ,
			Status:  status,
			Reason:  reason,
			Message: strings.TrimSpace(message),
		}
		if t, err := time.Parse(time.RFC3339, changed); err == nil {
			cond.Changed = t
		}
		d.Conditions = append(d.Conditions, cond)
	}

	taints, _, _ := unstructured.NestedSlice(u.Object, "spec", "taints")
	for _, raw := range taints {
		t, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		key, _, _ := unstructured.NestedString(t, "key")
		if key == "" {
			continue
		}
		value, _, _ := unstructured.NestedString(t, "value")
		effect, _, _ := unstructured.NestedString(t, "effect")
		d.Taints = append(d.Taints, model.Taint{Key: key, Value: value, Effect: effect})
	}
}

// objectEvents lists the events recorded against one object, newest page first.
// The bool reports that the server had more to give.
func (s *Source) objectEvents(ctx context.Context, kind, name string) ([]model.Event, bool, error) {
	// Field-selected across every namespace: node events land in whichever
	// namespace their reporter chose (usually "default", but Karpenter reports
	// from its own), and hard-coding one silently loses the rest.
	sel := fields.SelectorFromSet(fields.Set{
		"involvedObject.kind": kind,
		"involvedObject.name": name,
	}).String()

	list, err := s.clients.kube.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: sel,
		Limit:         maxEvents,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list %s events: %w", strings.ToLower(kind), err)
	}
	out := make([]model.Event, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, convertEvent(&list.Items[i]))
	}
	return out, list.Continue != "", nil
}

// convertEvent flattens a core/v1 Event, which by now carries two generations of
// schema: the original FirstTimestamp/LastTimestamp/Count fields, and the
// events.k8s.io shape (EventTime plus an optional series aggregate) that
// controllers using the newer recorder write through the same type. An event
// from a modern controller has zero legacy timestamps, so reading only those
// yields events dated to the zero time — which sorts them all to the top and
// makes the pane useless.
func convertEvent(e *corev1.Event) model.Event {
	ev := model.Event{
		Kind:      e.InvolvedObject.Kind,
		Object:    e.InvolvedObject.Name,
		Type:      e.Type,
		Reason:    e.Reason,
		Component: e.Source.Component,
		Message:   strings.TrimSpace(e.Message),
		Count:     e.Count,
		First:     e.FirstTimestamp.Time,
		Last:      e.LastTimestamp.Time,
	}
	if ev.Component == "" {
		ev.Component = e.ReportingController
	}
	if ev.First.IsZero() {
		ev.First = e.EventTime.Time
	}
	if e.Series != nil {
		if e.Series.Count > 0 {
			ev.Count = e.Series.Count
		}
		if !e.Series.LastObservedTime.IsZero() {
			ev.Last = e.Series.LastObservedTime.Time
		}
	}
	if ev.First.IsZero() {
		ev.First = e.CreationTimestamp.Time
	}
	if ev.Last.IsZero() {
		ev.Last = ev.First
	}
	if ev.Count == 0 {
		ev.Count = 1
	}
	if ev.Type == "" {
		ev.Type = "Normal"
	}
	return ev
}
