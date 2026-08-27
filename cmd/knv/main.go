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

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

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
	kubeconfig       string
	kubeContext      string
	mode             string
	sortKey          string
	themeName        string
	nodePool         string
	nodeQuery        string
	fps              int
	metricsRate      time.Duration
	priceAnnotation  string
	playbackSpeed    float64
	historyDuration  time.Duration
	historyMemoryMiB int64
	legend           bool

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
	pflag.StringVar(&f.sortKey, "sort", "name", "initial sort: name, cpu, mem, pods, age, nodepool, type")
	pflag.StringVar(&f.themeName, "theme", "dark", "palette: dark or light")
	pflag.StringVar(&f.nodePool, "nodepool", "", "start filtered to a Karpenter NodePool")
	pflag.StringVar(&f.nodeQuery, "node", "", "start filtered to nodes matching a regex")
	pflag.IntVar(&f.fps, "fps", 20, "animation frame rate")
	pflag.DurationVar(&f.metricsRate, "metrics-interval", 5*time.Second, "how often to poll metrics.k8s.io")
	pflag.StringVar(&f.priceAnnotation, "price-annotation", kube.DefaultPriceAnnotation, "annotation containing a node or NodeClaim's numeric hourly price")
	pflag.Float64Var(&f.playbackSpeed, "playback-speed", 1, "initial cluster playback speed from 0 (paused) to 1 (realtime)")
	pflag.DurationVar(&f.historyDuration, "history-duration", 10*time.Minute, "maximum buffered playback history")
	pflag.Int64Var(&f.historyMemoryMiB, "history-memory", 256, "approximate playback history memory limit in MiB")
	pflag.BoolVar(&f.legend, "legend", true, "show the colour legend")

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
	silenceClientGoLogging()
	if !theme.Use(f.themeName) {
		return fmt.Errorf("unknown theme %q (want dark or light)", f.themeName)
	}

	// Signals cancel the context, which stops the informers, the store's watch
	// goroutine and the TUI in that order.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	src, err := startSource(ctx, &f)
	if err != nil {
		return err
	}
	cfg.Snapshots = src.store.Watch(ctx, snapshotInterval)
	cfg.Demo = src.demo
	cfg.Describe = src.describe
	runSource := src.run

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

// silenceClientGoLogging stops client-go writing over the screen.
//
// Informers log through klog, which writes to stderr — and stderr, in a
// full-screen alt-screen program, is the screen. A reflector that cannot list a
// resource retries forever, so one missing permission turns into a stream of
// "events is forbidden" scribbled across the frame at seconds' interval. The
// informers already degrade quietly when a resource is unreadable; this is what
// makes them do it *silently*, which is the behaviour the rest of the program
// assumes. It matters most for events, the permission most often left out of a
// read-only role, and it is why a forbidden CONS column costs you a dim dot
// rather than a ruined display.
func silenceClientGoLogging() { klog.SetLogger(logr.Discard()) }

func buildConfig(f *flags) (ui.Config, error) {
	if f.playbackSpeed < 0 || f.playbackSpeed > 1 {
		return ui.Config{}, fmt.Errorf("--playback-speed must be between 0 and 1")
	}
	if f.historyDuration <= 0 {
		return ui.Config{}, fmt.Errorf("--history-duration must be positive")
	}
	if f.historyMemoryMiB <= 0 {
		return ui.Config{}, fmt.Errorf("--history-memory must be positive")
	}
	cfg := ui.Config{FPS: f.fps, Legend: f.legend, PlaybackSpeed: f.playbackSpeed,
		PlaybackSet: true, HistoryDuration: f.historyDuration, HistoryMemory: f.historyMemoryMiB << 20}

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

	cfg.Filter = ui.Filter{NodePool: f.nodePool}
	if err := cfg.Filter.SetNodeQuery(f.nodeQuery); err != nil {
		return cfg, fmt.Errorf("bad --node pattern: %w", err)
	}
	return cfg, nil
}

// source is a started source, live or simulated, in the terms the UI needs it.
// Bundling them keeps the wiring one assignment per capability instead of a
// five-value return that has to be read against its signature.
type source struct {
	store *model.Store
	run   func(context.Context) error
	// demo is nil against a real cluster: the viewer never mutates one.
	demo ui.Demo
	// describe backs the node detail pane. Both sources provide it — reading a
	// node and its events is a read, and safe against production.
	describe ui.Describer
}

// startSource builds either the live or simulated source.
func startSource(ctx context.Context, f *flags) (*source, error) {
	if f.demo {
		cluster, store := fake.New(fake.Options{
			Nodes:     f.demoNodes,
			Speed:     f.demoSpeed,
			Seed:      f.demoSeed,
			Autopilot: f.demoAuto,
			DrainFor:  f.demoDrain,
		})
		return &source{store: store, run: cluster.Run, demo: cluster, describe: cluster}, nil
	}

	src, store, err := kube.New(kube.Options{
		Kubeconfig:      f.kubeconfig,
		Context:         f.kubeContext,
		MetricsRate:     f.metricsRate,
		PriceAnnotation: f.priceAnnotation,
	})
	if err != nil {
		return nil, fmt.Errorf("%w\n\nno cluster? try --demo for a simulated one", err)
	}
	return &source{store: store, run: src.Run, describe: src}, nil
}
