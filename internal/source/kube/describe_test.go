package kube

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// Events reach the core/v1 type from two generations of API, and reading only the
// legacy fields dates every modern controller's event to the zero time — which
// sorts them all to the top of the pane and makes the ordering worthless. These
// cover each shape.
func TestConvertEventTimestamps(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	legacy := convertEvent(&corev1.Event{
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-10-0-0-1"},
		Type:           "Warning",
		Reason:         "FailedMount",
		Source:         corev1.EventSource{Component: "kubelet"},
		Message:        "  spaced  ",
		Count:          3,
		FirstTimestamp: metav1.NewTime(base),
		LastTimestamp:  metav1.NewTime(base.Add(10 * time.Minute)),
	})
	if !legacy.First.Equal(base) || !legacy.Last.Equal(base.Add(10*time.Minute)) {
		t.Fatalf("legacy timestamps lost: %+v", legacy)
	}
	if legacy.When() != legacy.Last {
		t.Fatal("When must order by the most recent occurrence")
	}
	if legacy.Count != 3 || !legacy.Warning() || legacy.Component != "kubelet" || legacy.Message != "spaced" {
		t.Fatalf("legacy fields wrong: %+v", legacy)
	}

	// events.k8s.io shape: EventTime plus a series aggregate, no legacy stamps.
	series := convertEvent(&corev1.Event{
		InvolvedObject:      corev1.ObjectReference{Kind: "NodeClaim", Name: "general-abc12"},
		Reason:              "DisruptionBlocked",
		ReportingController: "karpenter",
		EventTime:           metav1.NewMicroTime(base),
		Series: &corev1.EventSeries{
			Count:            7,
			LastObservedTime: metav1.NewMicroTime(base.Add(30 * time.Minute)),
		},
	})
	if !series.First.Equal(base) {
		t.Fatalf("EventTime not used as first occurrence: %+v", series)
	}
	if !series.Last.Equal(base.Add(30 * time.Minute)) {
		t.Fatalf("series last-observed not used: %+v", series)
	}
	if series.Count != 7 || series.Component != "karpenter" || series.Kind != "NodeClaim" {
		t.Fatalf("series fields wrong: %+v", series)
	}
	if series.Type != "Normal" {
		t.Fatalf("an event with no type must default to Normal, got %q", series.Type)
	}

	// Nothing but a creation timestamp: still has to land somewhere sensible.
	bare := convertEvent(&corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(base)},
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-10-0-0-1"},
		Reason:         "Rebooted",
	})
	if !bare.First.Equal(base) || !bare.Last.Equal(base) || bare.Count != 1 {
		t.Fatalf("bare event not repaired: %+v", bare)
	}
}

// TestApplyClaimReadsTheProvisioningFields covers the payload the pane shows for
// the ninety seconds before a Node exists: everything the claim knows, read out
// of unstructured JSON with no Karpenter API types in the build.
func TestApplyClaimReadsTheProvisioningFields(t *testing.T) {
	changed := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodeClaim",
		"metadata": map[string]interface{}{
			"name":        "general-abc12",
			"labels":      map[string]interface{}{"karpenter.sh/nodepool": "general"},
			"annotations": map[string]interface{}{"karpenter.sh/nodepool-hash": "845"},
		},
		"spec": map[string]interface{}{
			"taints": []interface{}{
				map[string]interface{}{"key": "gpu", "value": "true", "effect": "NoSchedule"},
				map[string]interface{}{"value": "keyless"}, // dropped: no key
			},
		},
		"status": map[string]interface{}{
			"providerID":  "aws:///us-west-2a/i-0abc",
			"capacity":    map[string]interface{}{"cpu": "16", "memory": "64Gi", "pods": "110"},
			"allocatable": map[string]interface{}{"cpu": "15800m", "memory": "63Gi", "pods": "110"},
			"conditions": []interface{}{
				map[string]interface{}{"type": "Launched", "status": "True", "reason": "Launched",
					"message": "  Launched instance  ", "lastTransitionTime": changed.Format(time.RFC3339)},
				map[string]interface{}{"type": "Ready", "status": "Unknown", "reason": "NotReady"},
				map[string]interface{}{"status": "True"}, // dropped: no type
			},
		},
	}}

	d := &model.NodeDetail{Kind: "NodeClaim"}
	applyClaim(d, u)

	if d.ProviderID != "aws:///us-west-2a/i-0abc" {
		t.Fatalf("providerID = %q", d.ProviderID)
	}
	if d.Capacity.CPUMilli != 16000 || d.Allocatable.CPUMilli != 15800 {
		t.Fatalf("capacity/allocatable = %+v / %+v", d.Capacity, d.Allocatable)
	}
	if d.Labels["karpenter.sh/nodepool"] != "general" || d.Annotations["karpenter.sh/nodepool-hash"] != "845" {
		t.Fatalf("labels/annotations lost: %v %v", d.Labels, d.Annotations)
	}
	if len(d.Conditions) != 2 {
		t.Fatalf("got %d conditions, want the two with a type: %+v", len(d.Conditions), d.Conditions)
	}
	launched := d.Conditions[0]
	if launched.Type != "Launched" || launched.Message != "Launched instance" || !launched.Changed.Equal(changed) {
		t.Fatalf("launched condition = %+v", launched)
	}
	if launched.Bad() {
		t.Fatal("Launched=True read as a fault; claim conditions are milestones")
	}
	if !d.Conditions[1].Bad() {
		t.Fatal("Ready=Unknown read as healthy")
	}
	if len(d.Taints) != 1 || d.Taints[0].String() != "gpu=true:NoSchedule" {
		t.Fatalf("taints = %+v", d.Taints)
	}
}

// A claim with nothing in its status yet — the first second of its life — must
// read as empty rather than panic on the missing paths.
func TestApplyClaimToleratesAnEmptyStatus(t *testing.T) {
	d := &model.NodeDetail{Kind: "NodeClaim"}
	applyClaim(d, &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "general-abc12"},
	}})
	if len(d.Conditions) != 0 || len(d.Taints) != 0 || d.ProviderID != "" || d.Capacity.CPUMilli != 0 {
		t.Fatalf("invented facts from an empty claim: %+v", d)
	}
}
