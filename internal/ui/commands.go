package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	argMode
	argSort
	argTheme
	argOnOff
	argFree
	argCount
	argZoom
	argSpeed
	argRewind
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
		name: "speed", aliases: []string{"rate"}, arg: argSpeed, argHint: "<0x…1x|realtime>",
		help: "slow, pause, or return the cluster timeline to realtime",
		run: func(m *Model, arg string) (string, error) {
			if isRealtime(arg) {
				m.pendingCmd = m.goRealtime("")
				return "realtime", nil
			}
			speed, err := parsePlaybackSpeed(arg)
			if err != nil {
				return "", err
			}
			if err := m.setPlaybackSpeed(speed); err != nil {
				return "", err
			}
			if speed == 0 {
				return playbackStatus(m.playback, time.Now()), nil
			}
			return playbackStatus(m.playback, time.Now()), nil
		},
	},
	{
		name: "pause", arg: argNone,
		help: "pause the cluster timeline (p toggles pause)",
		run: func(m *Model, _ string) (string, error) {
			if m.playback.live || m.playback.speed != 0 {
				m.togglePause()
			}
			return "", nil
		},
	},
	{
		name: "resume", arg: argNone,
		help: "resume the paused timeline at its previous speed",
		run: func(m *Model, _ string) (string, error) {
			if !m.playback.live && m.playback.speed == 0 {
				m.togglePause()
			}
			if m.playback.live {
				return "realtime", nil
			}
			return "", nil
		},
	},
	{
		name: "rewind", aliases: []string{"back"}, arg: argRewind, argHint: "<duration>",
		help: "rewind buffered playback by a duration such as 5s or 20s",
		run: func(m *Model, arg string) (string, error) {
			amount, err := parseRewindDuration(arg)
			if err != nil {
				return "", err
			}
			cmd, moved := m.rewindPlayback(amount)
			m.pendingCmd = cmd
			if moved == 0 {
				return "no rewind history yet", nil
			}
			return fmt.Sprintf("rewound %s · %s", moved.Round(time.Millisecond),
				playbackStatus(m.playback, time.Now())), nil
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
		help: "simulate draining and terminating a node",
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
	{
		name: "burst", arg: argCount, argHint: "<n>", demoOnly: true,
		help: "submit n unscheduled pods",
		run: func(m *Model, arg string) (string, error) {
			n, err := strconv.Atoi(arg)
			if err != nil || n < 1 || n > 500 {
				return "", fmt.Errorf("expected a count between 1 and 500")
			}
			m.demo.Burst(n)
			return fmt.Sprintf("submitting %d pods", n), nil
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

// commandNames lists the visible canonical names for completion.
func commandNames(includeDemo bool) []string {
	out := make([]string, 0, len(registry))
	for i := range registry {
		if registry[i].demoOnly && !includeDemo {
			continue
		}
		out = append(out, registry[i].name)
	}
	sort.Strings(out)
	// Actions that affect the whole palette belong at its end, in the order a
	// user needs them: clear first, help as the final escape hatch.
	out = moveCommandToEnd(out, "clear")
	out = moveCommandToEnd(out, "help")
	return out
}

func moveCommandToEnd(names []string, want string) []string {
	for i, name := range names {
		if name == want {
			copy(names[i:], names[i+1:])
			names[len(names)-1] = want
			break
		}
	}
	return names
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
	case argMode:
		return modeNames[:]
	case argSort:
		return sortNames[:]
	case argTheme:
		return theme.Names()
	case argOnOff:
		return []string{"on", "off"}
	case argZoom:
		return []string{"in", "out", "fit", "max"}
	case argSpeed:
		return []string{"realtime", "1x", "0.75x", "0.5x", "0.25x", "0x"}
	case argRewind:
		return []string{"5s", "10s", "20s", "30s"}
	default:
		return nil
	}
}

func isRealtime(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "realtime", "real-time", "live", "rt":
		return true
	default:
		return false
	}
}

func parsePlaybackSpeed(s string) (float64, error) {
	raw := strings.TrimSpace(strings.ToLower(s))
	raw = strings.TrimSuffix(raw, "x")
	speed, err := strconv.ParseFloat(raw, 64)
	if err != nil || speed < 0 || speed > 1 {
		return 0, fmt.Errorf("expected a speed between 0x and 1x, or realtime")
	}
	return speed, nil
}

func parseRewindDuration(s string) (time.Duration, error) {
	raw := strings.TrimSpace(strings.ToLower(s))
	amount, err := time.ParseDuration(raw)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("expected a positive duration such as 5s or 30s")
	}
	return amount, nil
}

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
