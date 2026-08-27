package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

func TestConfiguredPriceAnnotationIsUsedForNodesAndNodeClaims(t *testing.T) {
	const custom = "pricing.example.com/hourly"
	annotations := map[string]string{
		DefaultPriceAnnotation: "1.25",
		custom:                 "2.5",
	}
	node := convertNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        "node-a",
		Annotations: annotations,
	}}, custom)
	if !node.HasPrice || node.Price != 2.5 {
		t.Fatalf("node price = (%v, %v), want (true, 2.5)", node.HasPrice, node.Price)
	}

	store := model.NewStore("test")
	source := &Source{opts: Options{PriceAnnotation: custom}, store: store}
	claim := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodeClaim",
		"metadata": map[string]interface{}{
			"name": "claim-a",
		},
	}}
	claim.SetAnnotations(annotations)
	source.upsertNodeClaim(claim)
	snapshot := store.Snapshot()
	if len(snapshot.Nodes) != 1 {
		t.Fatalf("snapshot has %d nodes, want one NodeClaim placeholder", len(snapshot.Nodes))
	}
	if got := snapshot.Nodes[0]; !got.HasPrice || got.Price != 2.5 {
		t.Fatalf("NodeClaim price = (%v, %v), want (true, 2.5)", got.HasPrice, got.Price)
	}
}

func TestParsePriceRejectsMissingOrInvalidConfiguredAnnotation(t *testing.T) {
	annotations := map[string]string{
		"pricing.example.com/invalid": "not-a-number",
		"pricing.example.com/valid":   " 0.75 ",
	}
	if _, ok := parsePrice(annotations, "pricing.example.com/missing"); ok {
		t.Fatal("missing configured annotation was accepted as a price")
	}
	if _, ok := parsePrice(annotations, "pricing.example.com/invalid"); ok {
		t.Fatal("invalid configured annotation was accepted as a price")
	}
	if got, ok := parsePrice(annotations, "pricing.example.com/valid"); !ok || got != 0.75 {
		t.Fatalf("valid configured annotation = (%v, %v), want (0.75, true)", got, ok)
	}
}

// TestConvertPodSchedulingState pins the distinction the pending meter is built
// on: a pod nobody has looked at yet is waiting, and only the scheduler's own
// verdict makes it unschedulable.
func TestConvertPodSchedulingState(t *testing.T) {
	cases := []struct {
		name              string
		pod               *corev1.Pod
		wantPhase         model.PodPhase
		wantUnschedulable bool
	}{
		{
			name:      "unassigned and untouched",
			pod:       &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}},
			wantPhase: model.PodPending,
		},
		{
			name: "scheduler refused it",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionFalse,
					Reason: corev1.PodReasonUnschedulable,
				}},
			}},
			wantPhase:         model.PodPending,
			wantUnschedulable: true,
		},
		{
			name: "scheduled but not started",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{NodeName: "node-a"},
				Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{
					{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				}},
			},
			wantPhase: model.PodPending,
		},
		{
			name: "waiting for another reason is not a capacity problem",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionFalse,
					Reason: "SchedulerError",
				}},
			}},
			wantPhase: model.PodPending,
		},
		{
			name: "a refused pod being deleted is on its way out, not waiting",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{}},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					Conditions: []corev1.PodCondition{{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonUnschedulable,
					}},
				},
			},
			wantPhase: model.PodTerminating,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := convertPod(tc.pod)
			if got == nil {
				t.Fatal("convertPod dropped the pod; an unassigned pod is the backlog")
			}
			if got.Phase != tc.wantPhase {
				t.Errorf("phase = %v, want %v", got.Phase, tc.wantPhase)
			}
			if got.Unschedulable != tc.wantUnschedulable {
				t.Errorf("unschedulable = %v, want %v", got.Unschedulable, tc.wantUnschedulable)
			}
		})
	}
}
