package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// argKind tells the command bar what to offer as completions for a command's
// argument. Adding a new kind of completion is a case in candidates(); adding a
// command is one entry in the registry below. That is deliberately the entire
// extension surface.
type argKind int

const (
	argNone argKind = iota
	argNodePool
	argNamespace
	argOwner
	argMode
	argSort
	argBasis
	argTheme
	argPhase
	argInstanceType
	argCapacity
	argOnOff
	argFree
	argCount
	argZoom
)

// command is one entry in the palette.
type command struct {
	name    string
	aliases []string
	arg     argKind
	// argHint is shown in the prompt and help, e.g. "<name|all>".
	argHint string
	help    string
	// demoOnly commands are hidden and rejected unless a simulated cluster is
	// driving the view; they mutate the world, and the real cluster is read-only.
	demoOnly bool
	run      func(m *Model, arg string) (string, error)
}

// registry is the single source of truth for commands, their completions and
// their help text.
var registry = []command{
	{
		name: "nodepool", aliases: []string{"np", "pool"}, arg: argNodePool, argHint: "<name|all>",
		help: "show only nodes from a Karpenter NodePool",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				m.filter.NodePool = ""
				return "nodepool filter cleared", nil
			}
			if !m.knownNodePool(arg) {
				return "", fmt.Errorf("no nodepool %q", arg)
			}
			m.filter.NodePool = arg
			return "nodepool: " + arg, nil
		},
	},
	{
		name: "namespace", aliases: []string{"ns"}, arg: argNamespace, argHint: "<name|all>",
		help: "highlight pods in a namespace (grey out the rest)",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				m.filter.Namespace = ""
				return "namespace filter cleared", nil
			}
			m.filter.Namespace = arg
			return "namespace: " + arg, nil
		},
	},
	{
		name: "owner", aliases: []string{"app", "workload"}, arg: argOwner, argHint: "<substring|all>",
		help: "highlight pods whose controller name matches",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				m.filter.Owner = ""
				return "owner filter cleared", nil
			}
			m.filter.Owner = arg
			return "owner: " + arg, nil
		},
	},
	{
		name: "node", aliases: []string{"n"}, arg: argFree, argHint: "<regex|all>",
		help: "show only nodes whose name matches a regex",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				return "node filter cleared", m.filter.SetNodeQuery("")
			}
			if err := m.filter.SetNodeQuery(arg); err != nil {
				return "", fmt.Errorf("bad pattern: %w", err)
			}
			return "node: " + arg, nil
		},
	},
	{
		name: "type", aliases: []string{"instance"}, arg: argInstanceType, argHint: "<instance-type|all>",
		help: "show only nodes of an instance type",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				m.filter.InstanceType = ""
				return "instance-type filter cleared", nil
			}
			m.filter.InstanceType = arg
			return "type: " + arg, nil
		},
	},
	{
		name: "capacity", aliases: []string{"cap"}, arg: argCapacity, argHint: "<spot|on-demand|all>",
		help: "show only spot or on-demand capacity",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				m.filter.CapacityType = ""
				return "capacity filter cleared", nil
			}
			m.filter.CapacityType = arg
			return "capacity: " + arg, nil
		},
	},
	{
		name: "phase", aliases: []string{"state"}, arg: argPhase, argHint: "<ready|draining|…|all>",
		help: "show only nodes in given phases (repeat to add)",
		run: func(m *Model, arg string) (string, error) {
			if isAll(arg) {
				m.filter.Phases = nil
				return "phase filter cleared", nil
			}
			p, ok := parsePhase(arg)
			if !ok {
				return "", fmt.Errorf("unknown phase %q", arg)
			}
			if m.filter.Phases == nil {
				m.filter.Phases = map[model.Phase]bool{}
			}
			// Toggling means ":phase draining" twice returns you to everything,
			// which is faster than remembering ":phase all".
			if m.filter.Phases[p] {
				delete(m.filter.Phases, p)
			} else {
				m.filter.Phases[p] = true
			}
			if len(m.filter.Phases) == 0 {
				m.filter.Phases = nil
				return "phase filter cleared", nil
			}
			return "phase: " + strings.Join(m.filter.Describe(), " "), nil
		},
	},
	{
		name: "mode", aliases: []string{"m"}, arg: argMode, argHint: "<pods|nodes|dense>",
		help: "pods = show pod cells, nodes = utilisation only, dense = one row each",
		run: func(m *Model, arg string) (string, error) {
			mode, ok := ParseMode(arg)
			if !ok {
				return "", fmt.Errorf("unknown mode %q", arg)
			}
			m.setMode(mode)
			return "mode: " + arg, nil
		},
	},
	{
		name: "pods", arg: argOnOff, argHint: "<on|off>",
		help: "shorthand for mode pods / mode nodes",
		run: func(m *Model, arg string) (string, error) {
			on, ok := parseOnOff(arg)
			if !ok {
				return "", fmt.Errorf("expected on or off")
			}
			if on {
				m.setMode(ModePods)
				return "pods: on", nil
			}
			m.setMode(ModeNodes)
			return "pods: off (utilisation only)", nil
		},
	},
	{
		name: "daemonsets", aliases: []string{"ds"}, arg: argOnOff, argHint: "<on|off>",
		help: "include DaemonSet pods in the cells",
		run: func(m *Model, arg string) (string, error) {
			on, ok := parseOnOff(arg)
			if !ok {
				return "", fmt.Errorf("expected on or off")
			}
			m.filter.HideDaemonSets = !on
			return fmt.Sprintf("daemonsets: %s", onOff(on)), nil
		},
	},
	{
		name: "only", arg: argOnOff, argHint: "<on|off>",
		help: "hide nodes with no pod matching the namespace/owner filter",
		run: func(m *Model, arg string) (string, error) {
			on, ok := parseOnOff(arg)
			if !ok {
				return "", fmt.Errorf("expected on or off")
			}
			m.filter.Only = on
			return "only: " + onOff(on), nil
		},
	},
	{
		name: "sort", aliases: []string{"s"}, arg: argSort, argHint: "<key>",
		help: "order the grid by name, cpu, mem, pods, age, nodepool or type",
		run: func(m *Model, arg string) (string, error) {
			key, ok := ParseSort(arg)
			if !ok {
				return "", fmt.Errorf("unknown sort key %q", arg)
			}
			m.sortKey = key
			return "sort: " + key.String(), nil
		},
	},
	{
		name: "util", aliases: []string{"basis"}, arg: argBasis, argHint: "<requests|usage>",
		help: "drive the meters from pod requests or metrics-server usage",
		run: func(m *Model, arg string) (string, error) {
			switch arg {
			case "requests", "req":
				m.basis = model.BasisRequests
			case "usage", "actual":
				if !m.hasMetrics {
					return "", fmt.Errorf("metrics.k8s.io not available in this cluster")
				}
				m.basis = model.BasisUsage
			default:
				return "", fmt.Errorf("expected requests or usage")
			}
			return "meters: " + m.basis.String(), nil
		},
	},
	{
		name: "theme", arg: argTheme, argHint: "<dark|light>",
		help: "switch palette",
		run: func(m *Model, arg string) (string, error) {
			if !theme.Use(arg) {
				return "", fmt.Errorf("unknown theme %q", arg)
			}
			return "theme: " + arg, nil
		},
	},
	{
		name: "zoom", aliases: []string{"z"}, arg: argZoom, argHint: "<in|out|fit|max|level>",
		help: "grow or shrink the node cards; fit is the automatic layout",
		run: func(m *Model, arg string) (string, error) {
			if m.mode == ModeDense {
				return "", fmt.Errorf("dense mode is one row per node — zoom applies to pods and nodes modes")
			}
			target := m.zoom
			switch strings.ToLower(arg) {
			case "in", "+":
				target = m.zoom + 1
			case "out", "-":
				target = m.zoom - 1
			case "fit", "reset", "auto", "0":
				target = 0
			case "max", "node", "one":
				// "as far in as it goes" — one card, the selected one, filling the
				// screen. Worth a word of its own: it is the thing you want when
				// someone in the room asks about a specific node.
				target = zoomMax
			case "min", "all":
				target = zoomMin
			default:
				n, err := strconv.Atoi(arg)
				if err != nil {
					return "", fmt.Errorf("expected in, out, fit, max or a level between %d and %d", zoomMin, zoomMax)
				}
				if n < zoomMin || n > zoomMax {
					return "", fmt.Errorf("zoom level must be between %d and %d", zoomMin, zoomMax)
				}
				target = n
			}
			if !m.setZoom(target) {
				return "zoom: " + zoomLabel(m.lay.scale) + " (unchanged)", nil
			}
			return fmt.Sprintf("zoom: %s · %d per row", zoomLabel(m.lay.scale), m.lay.cols), nil
		},
	},
	{
		name: "legend", aliases: []string{"l"}, arg: argOnOff, argHint: "<on|off>",
		help: "show the colour legend",
		run: func(m *Model, arg string) (string, error) {
			on, ok := parseOnOff(arg)
			if !ok {
				return "", fmt.Errorf("expected on or off")
			}
			m.showLegend = on
			return "legend: " + onOff(on), nil
		},
	},
	{
		name: "clear", aliases: []string{"reset"}, arg: argNone,
		help: "clear every filter",
		run: func(m *Model, _ string) (string, error) {
			m.filter.Clear()
			return "filters cleared", nil
		},
	},
	{
		name: "help", aliases: []string{"h", "?"}, arg: argNone,
		help: "show the help overlay",
		run: func(m *Model, _ string) (string, error) {
			m.showHelp = true
			return "", nil
		},
	},
	{
		name: "quit", aliases: []string{"q", "exit"}, arg: argNone,
		help: "exit",
		run: func(m *Model, _ string) (string, error) {
			m.quitting = true
			return "", nil
		},
	},
	{
		name: "scale", arg: argCount, argHint: "<n>", demoOnly: true,
		help: "simulate a scale-up of n nodes",
		run: func(m *Model, arg string) (string, error) {
			n, err := strconv.Atoi(arg)
			if err != nil || n < 1 || n > 50 {
				return "", fmt.Errorf("expected a count between 1 and 50")
			}
			m.demo.ScaleUp(n)
			return fmt.Sprintf("provisioning %d nodes", n), nil
		},
	},
	{
		name: "drain", arg: argNone, demoOnly: true,
		help: "simulate draining and deleting a node",
		run: func(m *Model, _ string) (string, error) {
			m.demo.DrainOne()
			return "draining a node", nil
		},
	},
	{
		name: "churn", arg: argNone, demoOnly: true,
		help: "simulate pod churn",
		run: func(m *Model, _ string) (string, error) {
			m.demo.Churn()
			return "churning pods", nil
		},
	},
}

// lookup resolves a name or alias.
func lookup(name string) (*command, bool) {
	for i := range registry {
		if registry[i].name == name {
			return &registry[i], true
		}
		for _, a := range registry[i].aliases {
			if a == name {
				return &registry[i], true
			}
		}
	}
	return nil, false
}

// commandNames lists every invocable name (canonical only) for completion.
func commandNames(includeDemo bool) []string {
	out := make([]string, 0, len(registry))
	for i := range registry {
		if registry[i].demoOnly && !includeDemo {
			continue
		}
		out = append(out, registry[i].name)
	}
	sort.Strings(out)
	return out
}

// Run parses and executes a command line. It returns the status message to show.
func (m *Model) Run(line string) (string, error) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ":"))
	if line == "" {
		return "", nil
	}
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)

	cmd, ok := lookup(strings.ToLower(name))
	if !ok {
		return "", fmt.Errorf("unknown command %q — press ? for help", name)
	}
	if cmd.demoOnly && m.demo == nil {
		return "", fmt.Errorf(":%s only works against a simulated cluster (--demo)", cmd.name)
	}
	if cmd.arg != argNone && arg == "" {
		// An argument-taking command with no argument opens the picker rather
		// than erroring: ":nodepool<enter>" should show you the nodepools.
		return "", errNeedsArg
	}
	return cmd.run(m, arg)
}

// errNeedsArg signals the command bar to stay open and show completions.
var errNeedsArg = fmt.Errorf("argument required")

// candidates returns the completion list for a command's argument.
func (m *Model) candidates(cmd *command) []string {
	switch cmd.arg {
	case argNodePool:
		out := []string{"all"}
		for _, np := range m.snapshotPools() {
			out = append(out, np)
		}
		return out
	case argNamespace:
		return append([]string{"all"}, m.snapshotNamespaces()...)
	case argOwner:
		return append([]string{"all"}, m.snapshotOwners()...)
	case argInstanceType:
		return append([]string{"all"}, m.snapshotInstanceTypes()...)
	case argCapacity:
		return []string{"all", "spot", "on-demand"}
	case argMode:
		return modeNames[:]
	case argSort:
		return sortNames[:]
	case argBasis:
		return []string{"requests", "usage"}
	case argTheme:
		return theme.Names()
	case argPhase:
		out := []string{"all"}
		for _, p := range []model.Phase{model.PhaseReady, model.PhaseProvisioning, model.PhaseNotReady,
			model.PhaseCordoned, model.PhaseDraining, model.PhaseDeleting} {
			out = append(out, strings.ToLower(p.String()))
		}
		return out
	case argOnOff:
		return []string{"on", "off"}
	case argZoom:
		return []string{"in", "out", "fit", "max"}
	default:
		return nil
	}
}

func parsePhase(s string) (model.Phase, bool) {
	s = strings.ToLower(s)
	for i, name := range phaseCommandNames {
		if name == s {
			return model.Phase(i), true
		}
	}
	return 0, false
}

// phaseCommandNames must stay index-aligned with model.Phase.
var phaseCommandNames = []string{"provisioning", "notready", "ready", "cordoned", "draining", "deleting", "gone"}

func isAll(s string) bool {
	switch strings.ToLower(s) {
	case "all", "*", "clear", "none", "":
		return true
	}
	return false
}

func parseOnOff(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1", "show":
		return true, true
	case "off", "false", "no", "0", "hide":
		return false, true
	}
	return false, false
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
