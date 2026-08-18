package kube

import (
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcehelper "k8s.io/component-helpers/resource"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// Well-known labels, taints and annotations we read. Collected here so adding
// support for another provisioner is a one-line change.
const (
	labelInstanceType = "node.kubernetes.io/instance-type"
	labelZone         = "topology.kubernetes.io/zone"
	labelRegion       = "topology.kubernetes.io/region"
	labelArch         = "kubernetes.io/arch"
	labelCapacityType = "karpenter.sh/capacity-type"
	labelNodePool     = "karpenter.sh/nodepool"

	taintKarpenterDisrupted = "karpenter.sh/disrupted"
	taintClusterAutoscaler  = "ToBeDeletedByClusterAutoscaler"
	taintUnschedulable      = "node.kubernetes.io/unschedulable"

	// annotationPrice lets a demo attach a cost to a node or NodeClaim without
	// this tool having to ship a cloud pricing table.
	annotationPrice = "node-viewer.oxide.computer/hourly-price"

	// gpuResource is the only accelerator name we count; extend as needed.
	gpuResource = "nvidia.com/gpu"

	// provisioningWindow is how long a not-yet-Ready node is shown as
	// provisioning rather than broken.
	provisioningWindow = 5 * time.Minute
)

// convertNode flattens a corev1.Node into the view model. It never retains a
// reference into the informer cache object.
func convertNode(n *corev1.Node) *model.Node {
	out := &model.Node{
		Name:         n.Name,
		InstanceType: n.Labels[labelInstanceType],
		Zone:         n.Labels[labelZone],
		Region:       n.Labels[labelRegion],
		Arch:         n.Labels[labelArch],
		CapacityType: n.Labels[labelCapacityType],
		NodePool:     n.Labels[labelNodePool],
		Schedulable:  !n.Spec.Unschedulable,
		Created:      n.CreationTimestamp.Time,
		Allocatable:  resourcesFromList(n.Status.Allocatable),
		Labels:       copyLabels(n.Labels),
	}
	if p, ok := parsePrice(n.Annotations); ok {
		out.Price, out.HasPrice = p, true
	}

	out.Ready, out.Message = readyState(n)
	out.Phase = derivePhase(n, out)
	return out
}

func readyState(n *corev1.Node) (ready bool, message string) {
	for i := range n.Status.Conditions {
		c := &n.Status.Conditions[i]
		if c.Type != corev1.NodeReady {
			continue
		}
		if c.Status == corev1.ConditionTrue {
			return true, ""
		}
		if c.Reason != "" {
			return false, c.Reason
		}
		return false, "NotReady"
	}
	return false, "no Ready condition"
}

// derivePhase is the single place taints, conditions and deletion timestamps are
// interpreted. The renderer only ever sees the result.
func derivePhase(n *corev1.Node, out *model.Node) model.Phase {
	if n.DeletionTimestamp != nil {
		out.Message = "terminating"
		return model.PhaseDeleting
	}
	for i := range n.Spec.Taints {
		t := &n.Spec.Taints[i]
		switch t.Key {
		case taintKarpenterDisrupted:
			out.Message = "disrupted: " + valueOr(t.Value, "unknown")
			return model.PhaseDraining
		case taintClusterAutoscaler:
			out.Message = "scale-down"
			return model.PhaseDraining
		}
	}
	if n.Spec.Unschedulable {
		out.Message = "cordoned"
		return model.PhaseCordoned
	}
	if out.Ready {
		return model.PhaseReady
	}
	if time.Since(n.CreationTimestamp.Time) < provisioningWindow {
		return model.PhaseProvisioning
	}
	return model.PhaseNotReady
}

// convertPod flattens a corev1.Pod. Returns nil for pods that will never be
// drawn (unscheduled, or already finished), which keeps them out of the store
// entirely rather than being filtered on every frame.
func convertPod(p *corev1.Pod) *model.Pod {
	if p.Spec.NodeName == "" {
		return nil
	}
	out := &model.Pod{
		Namespace: p.Namespace,
		Name:      p.Name,
		NodeName:  p.Spec.NodeName,
		Created:   p.CreationTimestamp.Time,
		Requests:  resourcesFromList(resourcehelper.PodRequests(p, resourcehelper.PodResourcesOptions{})),
		Limits:    resourcesFromList(resourcehelper.PodLimits(p, resourcehelper.PodResourcesOptions{})),
	}
	out.Requests.Pods, out.Limits.Pods = 1, 1
	out.Owner = ownerName(p)
	out.DaemonSet = isDaemonSet(p)
	out.Phase = podPhase(p)
	out.Ready = podReady(p)
	return out
}

func podPhase(p *corev1.Pod) model.PodPhase {
	// Terminating is not a phase in the API, but a pod holding capacity while
	// it drains is exactly what a draining-node demo needs to show.
	if p.DeletionTimestamp != nil {
		return model.PodTerminating
	}
	switch p.Status.Phase {
	case corev1.PodRunning:
		return model.PodRunning
	case corev1.PodSucceeded:
		return model.PodSucceeded
	case corev1.PodFailed:
		return model.PodFailed
	default:
		return model.PodPending
	}
}

func podReady(p *corev1.Pod) bool {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == corev1.PodReady {
			return p.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}

func ownerName(p *corev1.Pod) string {
	for i := range p.OwnerReferences {
		o := &p.OwnerReferences[i]
		if o.Controller != nil && !*o.Controller {
			continue
		}
		return model.TrimOwner(o.Kind, o.Name)
	}
	// Static and bare pods: strip a trailing random suffix so related pods
	// still share a colour.
	if i := strings.LastIndex(p.Name, "-"); i > 0 {
		return p.Name[:i]
	}
	return p.Name
}

func isDaemonSet(p *corev1.Pod) bool {
	for i := range p.OwnerReferences {
		if p.OwnerReferences[i].Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func resourcesFromList(rl corev1.ResourceList) model.Resources {
	var out model.Resources
	if q, ok := rl[corev1.ResourceCPU]; ok {
		out.CPUMilli = q.MilliValue()
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		out.MemBytes = q.Value()
	}
	if q, ok := rl[corev1.ResourcePods]; ok {
		out.Pods = q.Value()
	}
	if q, ok := rl[gpuResource]; ok {
		out.GPU = q.Value()
	}
	return out
}

func usageResources(cpu, mem resource.Quantity) model.Resources {
	return model.Resources{CPUMilli: cpu.MilliValue(), MemBytes: mem.Value()}
}

func parsePrice(ann map[string]string) (float64, bool) {
	v, ok := ann[annotationPrice]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func copyLabels(in map[string]string) map[string]string {
	// The informer cache object must never be mutated, and the UI holds these
	// across frames, so copy rather than alias.
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// stripForCache is a cache.TransformFunc that drops the fields we never read.
// managedFields and the last-applied annotation dominate object size on large
// clusters; dropping them at the cache boundary cuts informer memory
// substantially and costs nothing.
func stripForCache(obj interface{}) (interface{}, error) {
	if m, ok := obj.(metav1.ObjectMetaAccessor); ok {
		meta := m.GetObjectMeta()
		meta.SetManagedFields(nil)
		if ann := meta.GetAnnotations(); ann != nil {
			delete(ann, corev1.LastAppliedConfigAnnotation)
		}
	}
	return obj, nil
}
