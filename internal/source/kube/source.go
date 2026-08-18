// Package kube feeds a model.Store from a live cluster using shared informers.
//
// Shape of the pipeline:
//
//	SharedInformerFactory ──► Node informer  ──┐
//	                      └─► Pod informer  ───┼─► handlers convert + Upsert ──► model.Store
//	dynamic factory ──────────► NodePool/NodeClaim informers
//	metrics poller ───────────────────────────────┘
//
// Design notes worth keeping:
//   - One shared cache per resource, watched once, regardless of how many
//     screens or filters the UI has. Filtering happens in the UI, not by
//     re-listing the API.
//   - Handlers convert to model types immediately and never hand a cache object
//     onward, so nothing downstream can mutate shared state.
//   - Every optional API (metrics, Karpenter) is discovered at startup and its
//     absence degrades a feature instead of failing the program.
package kube

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// Resync is the informer resync period. Informers are watch-driven, so this is
// only a safety net against a missed event; it is deliberately long.
const Resync = 10 * time.Minute

var (
	nodePoolGVR  = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	nodeClaimGVR = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
)

// Options configures the cluster connection.
type Options struct {
	Kubeconfig  string
	Context     string
	MetricsRate time.Duration
	// QPS/Burst are raised over client-go's timid defaults because the initial
	// LIST of pods on a large cluster is otherwise throttled into a slow start.
	QPS   float32
	Burst int
}

// Source owns the informers and clients for one cluster.
type Source struct {
	opts    Options
	store   *model.Store
	clients *clients
}

type clients struct {
	kube         kubernetes.Interface
	dynamic      dynamic.Interface
	metrics      metricsclient.Interface
	contextName  string
	hasMetrics   bool
	hasKarpenter bool
}

// New connects to the cluster and probes for optional APIs. It does not start
// watching; call Run for that.
func New(opts Options) (*Source, *model.Store, error) {
	if opts.QPS == 0 {
		opts.QPS, opts.Burst = 50, 100
	}
	if opts.MetricsRate == 0 {
		opts.MetricsRate = 5 * time.Second
	}
	c, err := connect(opts)
	if err != nil {
		return nil, nil, err
	}
	store := model.NewStore(c.contextName)
	store.SetCapabilities(c.hasKarpenter, c.hasMetrics)
	return &Source{opts: opts, store: store, clients: c}, store, nil
}

func connect(opts Options) (*clients, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		rules.ExplicitPath = opts.Kubeconfig
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: opts.Context})

	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cfg.QPS, cfg.Burst = opts.QPS, opts.Burst
	cfg.UserAgent = "k8s-node-viewer"

	name := opts.Context
	if name == "" {
		if raw, err := cc.RawConfig(); err == nil {
			name = raw.CurrentContext
		}
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	metricsClient, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}

	c := &clients{kube: kubeClient, dynamic: dynClient, metrics: metricsClient, contextName: name}
	c.probe(cfg)
	return c, nil
}

// probe checks for metrics-server and Karpenter once, at startup. A cluster
// that installs either later needs a restart, which is an acceptable trade for
// not re-running discovery on a timer.
func (c *clients) probe(cfg *rest.Config) {
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return
	}
	c.hasMetrics = hasGroupVersion(disco, "metrics.k8s.io/v1beta1")
	c.hasKarpenter = hasGroupVersion(disco, "karpenter.sh/v1")
}

func hasGroupVersion(disco discovery.DiscoveryInterface, gv string) bool {
	_, err := disco.ServerResourcesForGroupVersion(gv)
	return err == nil
}

// HasKarpenter reports whether Karpenter CRDs were found.
func (s *Source) HasKarpenter() bool { return s.clients.hasKarpenter }

// HasMetrics reports whether metrics.k8s.io was found.
func (s *Source) HasMetrics() bool { return s.clients.hasMetrics }

// ContextName is the kube context in use, for the status bar.
func (s *Source) ContextName() string { return s.clients.contextName }

// Run starts every watcher and blocks until ctx is cancelled. It returns once
// all informers have stopped.
func (s *Source) Run(ctx context.Context) error {
	factory := informers.NewSharedInformerFactoryWithOptions(s.clients.kube, Resync)

	nodeInformer := factory.Core().V1().Nodes().Informer()
	if err := nodeInformer.SetTransform(stripForCache); err != nil {
		return fmt.Errorf("node transform: %w", err)
	}
	if _, err := nodeInformer.AddEventHandler(s.nodeHandler()); err != nil {
		return fmt.Errorf("node handler: %w", err)
	}

	podInformer := factory.Core().V1().Pods().Informer()
	if err := podInformer.SetTransform(stripForCache); err != nil {
		return fmt.Errorf("pod transform: %w", err)
	}
	if _, err := podInformer.AddEventHandler(s.podHandler()); err != nil {
		return fmt.Errorf("pod handler: %w", err)
	}

	factory.Start(ctx.Done())

	var dynFactory dynamicinformer.DynamicSharedInformerFactory
	if s.clients.hasKarpenter {
		dynFactory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(s.clients.dynamic, Resync, metav1.NamespaceAll, nil)
		if err := s.startKarpenter(ctx, dynFactory); err != nil {
			return err
		}
		dynFactory.Start(ctx.Done())
	}

	// Wait for the initial LIST of every cache before the metrics poller starts,
	// so the first samples land on nodes that already exist.
	factory.WaitForCacheSync(ctx.Done())
	if dynFactory != nil {
		dynFactory.WaitForCacheSync(ctx.Done())
	}

	if s.clients.hasMetrics {
		go s.pollMetrics(ctx)
	}

	<-ctx.Done()
	factory.Shutdown()
	if dynFactory != nil {
		dynFactory.Shutdown()
	}
	return nil
}

func (s *Source) nodeHandler() cache.ResourceEventHandler {
	upsert := func(obj interface{}) {
		if n, ok := obj.(*corev1.Node); ok {
			s.store.UpsertNode(convertNode(n))
		}
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    upsert,
		UpdateFunc: func(_, newObj interface{}) { upsert(newObj) },
		DeleteFunc: func(obj interface{}) {
			if name, ok := deletedName(obj); ok {
				s.store.DeleteNode(name)
			}
		},
	}
}

func (s *Source) podHandler() cache.ResourceEventHandler {
	upsert := func(obj interface{}) {
		p, ok := obj.(*corev1.Pod)
		if !ok {
			return
		}
		if converted := convertPod(p); converted != nil {
			s.store.UpsertPod(converted)
		} else {
			s.store.DeletePod(p.Namespace + "/" + p.Name)
		}
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    upsert,
		UpdateFunc: func(_, newObj interface{}) { upsert(newObj) },
		DeleteFunc: func(obj interface{}) {
			if p, ok := unwrap(obj).(*corev1.Pod); ok {
				s.store.DeletePod(p.Namespace + "/" + p.Name)
			}
		},
	}
}

// unwrap resolves the DeletedFinalStateUnknown tombstone the informer delivers
// when it missed the delete watch event.
func unwrap(obj interface{}) interface{} {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return t.Obj
	}
	return obj
}

func deletedName(obj interface{}) (string, bool) {
	if m, ok := unwrap(obj).(metav1.Object); ok {
		return m.GetName(), true
	}
	return "", false
}
