// Command knv is a full-screen visualiser for Kubernetes nodes and pods.
//
// It draws every node as a box whose interior is filled to show utilisation,
// with each pod as a proportionally-sized cell, and animates the lifecycle
// events that matter during a demo: provisioning, draining, deletion.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/source/fake"
	"github.com/oxidecomputer/k8s-node-viewer/internal/source/kube"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
	"github.com/oxidecomputer/k8s-node-viewer/internal/ui"
)

// snapshotInterval bounds how often the store rebuilds a snapshot. 100ms is
// below the threshold where a human notices lag, and well above the rate at
// which a rollout would otherwise rebuild thousands of times a second.
const snapshotInterval = 100 * time.Millisecond

type flags struct {
	kubeconfig  string
	kubeContext string
	mode        string
	basis       string
	sortKey     string
	themeName   string
	nodePool    string
	namespace   string
	nodeQuery   string
	capacity    string
	fps         int
	metricsRate time.Duration
	legend      bool
	hideDS      bool

	demo      bool
	demoNodes int
	demoSpeed float64
	demoSeed  int64
	demoAuto  bool
	demoDrain time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "knv:", err)
		os.Exit(1)
	}
}

func run() error {
	var f flags
	pflag.StringVar(&f.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: standard loading rules)")
	pflag.StringVar(&f.kubeContext, "context", "", "kubeconfig context to use")
	pflag.StringVar(&f.mode, "mode", "pods", "initial view: pods, nodes, or dense")
	pflag.StringVar(&f.basis, "util", "requests", "drive meters from 'requests' or 'usage' (metrics-server)")
	pflag.StringVar(&f.sortKey, "sort", "name", "initial sort: name, cpu, mem, pods, age, nodepool, type")
	pflag.StringVar(&f.themeName, "theme", "dark", "palette: dark or light")
	pflag.StringVar(&f.nodePool, "nodepool", "", "start filtered to a Karpenter NodePool")
	pflag.StringVar(&f.namespace, "namespace", "", "start with a namespace highlighted")
	pflag.StringVar(&f.nodeQuery, "node", "", "start filtered to nodes matching a regex")
	pflag.StringVar(&f.capacity, "capacity", "", "start filtered to 'spot' or 'on-demand'")
	pflag.IntVar(&f.fps, "fps", 20, "animation frame rate")
	pflag.DurationVar(&f.metricsRate, "metrics-interval", 5*time.Second, "how often to poll metrics.k8s.io")
	pflag.BoolVar(&f.legend, "legend", true, "show the colour legend")
	pflag.BoolVar(&f.hideDS, "hide-daemonsets", false, "omit DaemonSet pods from the cells")

	pflag.BoolVar(&f.demo, "demo", false, "run against a simulated cluster instead of a real one")
	pflag.IntVar(&f.demoNodes, "demo-nodes", 12, "initial node count in demo mode")
	pflag.Float64Var(&f.demoSpeed, "demo-speed", 1, "event rate multiplier in demo mode")
	pflag.Int64Var(&f.demoSeed, "demo-seed", 1, "RNG seed, so a rehearsed demo replays identically")
	pflag.BoolVar(&f.demoAuto, "demo-autopilot", true, "let demo mode scale and drain on its own")
	pflag.DurationVar(&f.demoDrain, "demo-drain", 4*time.Second, "how long a simulated drain takes; raise it to talk over the animation")

	help := pflag.BoolP("help", "h", false, "show usage")
	pflag.Parse()
	if *help {
		fmt.Fprintf(os.Stderr, "knv — Kubernetes node visualiser\n\nUsage:\n")
		pflag.PrintDefaults()
		return nil
	}

	cfg, err := buildConfig(&f)
	if err != nil {
		return err
	}
	if !theme.Use(f.themeName) {
		return fmt.Errorf("unknown theme %q (want dark or light)", f.themeName)
	}

	// Signals cancel the context, which stops the informers, the store's watch
	// goroutine and the TUI in that order.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, runSource, demo, hasMetrics, err := startSource(ctx, &f)
	if err != nil {
		return err
	}
	cfg.Snapshots = store.Watch(ctx, snapshotInterval)
	cfg.Demo = demo
	cfg.HasMetrics = hasMetrics

	// The source runs alongside the UI; a source failure should tear the UI down
	// rather than leave a frozen screen, so it cancels the shared context.
	sourceErr := make(chan error, 1)
	sourceCtx, cancelSource := context.WithCancel(ctx)
	defer cancelSource()
	go func() { sourceErr <- runSource(sourceCtx) }()

	uiErr := ui.Run(ctx, cfg)
	cancelSource()

	if uiErr != nil && !errors.Is(uiErr, context.Canceled) {
		return uiErr
	}
	select {
	case err := <-sourceErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	case <-time.After(2 * time.Second):
		// Informer shutdown can take a moment; do not hang the exit on it.
	}
	return nil
}

func buildConfig(f *flags) (ui.Config, error) {
	cfg := ui.Config{FPS: f.fps, Legend: f.legend}

	mode, ok := ui.ParseMode(f.mode)
	if !ok {
		return cfg, fmt.Errorf("unknown mode %q (want pods, nodes or dense)", f.mode)
	}
	cfg.Mode = mode

	sortKey, ok := ui.ParseSort(f.sortKey)
	if !ok {
		return cfg, fmt.Errorf("unknown sort key %q", f.sortKey)
	}
	cfg.Sort = sortKey

	switch f.basis {
	case "requests", "req":
		cfg.Basis = model.BasisRequests
	case "usage", "actual":
		cfg.Basis = model.BasisUsage
	default:
		return cfg, fmt.Errorf("unknown --util %q (want requests or usage)", f.basis)
	}

	cfg.Filter = ui.Filter{
		NodePool:       f.nodePool,
		Namespace:      f.namespace,
		CapacityType:   f.capacity,
		HideDaemonSets: f.hideDS,
	}
	if err := cfg.Filter.SetNodeQuery(f.nodeQuery); err != nil {
		return cfg, fmt.Errorf("bad --node pattern: %w", err)
	}
	return cfg, nil
}

// startSource builds either the live or simulated source. Both return a store
// and a blocking run function, so the caller does not care which it got.
func startSource(ctx context.Context, f *flags) (*model.Store, func(context.Context) error, ui.Demo, bool, error) {
	if f.demo {
		cluster, store := fake.New(fake.Options{
			Nodes:     f.demoNodes,
			Speed:     f.demoSpeed,
			Seed:      f.demoSeed,
			Autopilot: f.demoAuto,
			DrainFor:  f.demoDrain,
		})
		return store, cluster.Run, cluster, true, nil
	}

	src, store, err := kube.New(kube.Options{
		Kubeconfig:  f.kubeconfig,
		Context:     f.kubeContext,
		MetricsRate: f.metricsRate,
	})
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("%w\n\nno cluster? try --demo for a simulated one", err)
	}
	if !src.HasMetrics() && f.basis == "usage" {
		return nil, nil, nil, false, errors.New("--util usage requires metrics.k8s.io, which this cluster does not serve")
	}
	return store, src.Run, nil, src.HasMetrics(), nil
}
