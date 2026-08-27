package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// consolidationReasons maps the Karpenter event reasons that carry a disruption
// verdict onto the verdict itself. Adding another reason — a future Karpenter
// spelling, or another provisioner's equivalent — is one line here and nothing
// else, because one watch is started per entry.
var consolidationReasons = map[string]model.Consolidation{
	"ConsolidationCandidate": model.ConsolidationYes,
	"Unconsolidatable":       model.ConsolidationNo,
}

// startConsolidationWatch watches the disruption-verdict events, and only those.
//
// This is the one place the viewer watches events, and it is affordable only
// because of the field selector: the API server sends events matching one reason
// and nothing else, so the cache holds a handful of objects per node instead of
// the cluster's entire event stream. A watch is what the column needs — unlike
// the detail pane, it wants a verdict for every node, continuously, and polling
// every node's events to build a table column would be absurd.
//
// One informer per reason, because field selectors cannot express "reason is one
// of these": there is no set operator, and a client-side filter over all events
// is the very thing being avoided.
func (s *Source) startConsolidationWatch(ctx context.Context) error {
	for reason, verdict := range consolidationReasons {
		verdict := verdict
		factory := informers.NewSharedInformerFactoryWithOptions(s.clients.kube, Resync,
			informers.WithTweakListOptions(func(o *metav1.ListOptions) {
				o.FieldSelector = fields.OneTermEqualSelector("reason", reason).String()
			}))
		inf := factory.Core().V1().Events().Informer()
		if err := inf.SetTransform(stripForCache); err != nil {
			return fmt.Errorf("%s transform: %w", reason, err)
		}
		apply := func(obj interface{}) { s.applyConsolidation(obj, verdict) }
		if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    apply,
			UpdateFunc: func(_, newObj interface{}) { apply(newObj) },
			// No DeleteFunc: an event ageing out of the API server is not Karpenter
			// changing its mind. The verdict expires on its own TTL instead.
		}); err != nil {
			return fmt.Errorf("%s handler: %w", reason, err)
		}
		factory.Start(ctx.Done())
	}
	return nil
}

// applyConsolidation routes one verdict event into the store. Events about
// anything other than a Node or a NodeClaim are ignored: the reasons are
// Karpenter's, but nothing stops another controller reusing the word.
func (s *Source) applyConsolidation(obj interface{}, verdict model.Consolidation) {
	e, ok := obj.(*corev1.Event)
	if !ok {
		return
	}
	switch e.InvolvedObject.Kind {
	case "Node", "NodeClaim":
	default:
		return
	}
	ev := convertEvent(e)
	s.store.SetConsolidation(e.InvolvedObject.Name, verdict, ev.Message, ev.When())
}
