package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// startKarpenter wires informers for NodePools and NodeClaims.
//
// These use the dynamic client with unstructured objects rather than importing
// the Karpenter API module. That keeps a heavyweight dependency (and its own
// pinned client-go) out of the build, and it means a cluster running a slightly
// different Karpenter patch version still works — we only read a handful of
// well-known paths.
func (s *Source) startKarpenter(ctx context.Context, factory dynamicinformer.DynamicSharedInformerFactory) error {
	pools := factory.ForResource(nodePoolGVR).Informer()
	if _, err := pools.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { s.upsertNodePool(obj) },
		UpdateFunc: func(_, newObj interface{}) { s.upsertNodePool(newObj) },
		DeleteFunc: func(obj interface{}) {
			if name, ok := deletedName(obj); ok {
				s.store.DeleteNodePool(name)
			}
		},
	}); err != nil {
		return fmt.Errorf("nodepool handler: %w", err)
	}

	claims := factory.ForResource(nodeClaimGVR).Informer()
	if _, err := claims.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { s.upsertNodeClaim(obj) },
		UpdateFunc: func(_, newObj interface{}) { s.upsertNodeClaim(newObj) },
		DeleteFunc: func(obj interface{}) {
			if name, ok := deletedName(obj); ok {
				s.store.DeleteClaim(name)
			}
		},
	}); err != nil {
		return fmt.Errorf("nodeclaim handler: %w", err)
	}
	return nil
}

func (s *Source) upsertNodePool(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	pool := &model.NodePool{
		Name:    u.GetName(),
		Created: u.GetCreationTimestamp().Time,
	}
	if limits, found, _ := unstructured.NestedStringMap(u.Object, "spec", "limits"); found {
		pool.Limits = quantityMap(limits)
	}
	if w, found, _ := unstructured.NestedInt64(u.Object, "spec", "weight"); found {
		pool.Weight = int32(w)
	}
	s.store.UpsertNodePool(pool)
}

// upsertNodeClaim registers the claim so a scale-up is visible before the Node
// object exists. Once status.nodeName is set the store hands ownership to the
// real Node and the placeholder disappears.
func (s *Source) upsertNodeClaim(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	nodeName, _, _ := unstructured.NestedString(u.Object, "status", "nodeName")
	labels := u.GetLabels()

	placeholder := &model.Node{
		Name:         valueOr(nodeName, u.GetName()),
		NodeClaim:    u.GetName(),
		NodePool:     labels[labelNodePool],
		InstanceType: labels[labelInstanceType],
		Zone:         labels[labelZone],
		Arch:         labels[labelArch],
		CapacityType: labels[labelCapacityType],
		Created:      u.GetCreationTimestamp().Time,
		Phase:        model.PhaseProvisioning,
		Message:      claimMessage(u),
		Labels:       copyLabels(labels),
	}
	// A claim's status.capacity is the instance's real capacity, which lets the
	// placeholder box be drawn to scale before kubelet registers.
	if cap, found, _ := unstructured.NestedStringMap(u.Object, "status", "capacity"); found {
		placeholder.Allocatable = quantityMap(cap)
	}
	if alloc, found, _ := unstructured.NestedStringMap(u.Object, "status", "allocatable"); found {
		placeholder.Allocatable = quantityMap(alloc)
	}
	if p, ok := parsePrice(u.GetAnnotations()); ok {
		placeholder.Price, placeholder.HasPrice = p, true
	}

	s.store.UpsertClaim(u.GetName(), placeholder)
}

// claimMessage surfaces why a claim is not yet a node — "Launched",
// "Registered", or the first unready condition's reason.
func claimMessage(u *unstructured.Unstructured) string {
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return "requested"
	}
	for _, raw := range conds {
		c, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(c, "type")
		status, _, _ := unstructured.NestedString(c, "status")
		if typ == "Ready" && status != "True" {
			if reason, _, _ := unstructured.NestedString(c, "reason"); reason != "" {
				return reason
			}
		}
	}
	return "launching"
}

// quantityMap parses a map of resource-name to quantity string, as it appears in
// unstructured Karpenter objects.
func quantityMap(in map[string]string) model.Resources {
	rl := corev1.ResourceList{}
	for k, v := range in {
		if q, err := resource.ParseQuantity(v); err == nil {
			rl[corev1.ResourceName(k)] = q
		}
	}
	return resourcesFromList(rl)
}
