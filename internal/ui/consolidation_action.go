package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
	"github.com/oxidecomputer/k8s-node-viewer/internal/theme"
)

// A consolidation action is not a Kubernetes object. Karpenter exposes the
// source side as disruption state and the destination side as a newly-created
// NodeClaim, but does not publish a durable source -> destination reference.
// The UI therefore groups observable facts conservatively: active consolidation
// sources and contemporaneous replacements in the same NodePool.
const (
	consolidationJoinSlack   = 30 * time.Second
	consolidationJoinWindow  = 10 * time.Minute
	consolidationSourceBurst = 45 * time.Second
	targetReadyHold          = 30 * time.Second
)

type consolidationRole uint8

const (
	consolidationOutgoing consolidationRole = iota + 1
	consolidationIncoming
)

type consolidationKind uint8

const (
	consolidationKindUnknown consolidationKind = iota
	consolidationKindEmpty
	consolidationKindDelete
	consolidationKindReplace
)

type consolidationMember struct {
	id    string
	role  consolidationRole
	kind  consolidationKind
	stage string
}

type consolidationAction struct {
	id            string
	pool          string
	sources       []*model.Node
	targets       []*model.Node
	started       time.Time
	kind          consolidationKind
	podsRemaining int
	savings       float64
	savingsKnown  bool
}

type consolidationView struct {
	actions []consolidationAction
	members map[string]consolidationMember
}

func detectConsolidations(snap *model.Snapshot, now time.Time) consolidationView {
	view := consolidationView{members: map[string]consolidationMember{}}
	if snap == nil {
		return view
	}

	byPool := map[string][]*model.Node{}
	sourceNames := map[string]bool{}
	for _, n := range snap.Nodes {
		if isConsolidationSource(n) {
			byPool[n.NodePool] = append(byPool[n.NodePool], n)
			sourceNames[n.Name] = true
		}
	}
	if len(byPool) == 0 {
		return view
	}

	pools := make([]string, 0, len(byPool))
	for pool := range byPool {
		pools = append(pools, pool)
	}
	sort.Strings(pools)
	for _, pool := range pools {
		sources := byPool[pool]
		sort.Slice(sources, func(i, j int) bool {
			ai, aj := consolidationStartedAt(sources[i], now), consolidationStartedAt(sources[j], now)
			if ai.Equal(aj) {
				return sources[i].Name < sources[j].Name
			}
			return ai.Before(aj)
		})
		for _, n := range sources {
			at := consolidationStartedAt(n, now)
			newBurst := len(view.actions) == 0 || view.actions[len(view.actions)-1].pool != pool ||
				at.Sub(view.actions[len(view.actions)-1].started) > consolidationSourceBurst
			if newBurst {
				view.actions = append(view.actions, consolidationAction{pool: pool, started: at, savingsKnown: true})
			}
			a := &view.actions[len(view.actions)-1]
			a.sources = append(a.sources, n)
			a.kind = mergeConsolidationKind(a.kind, sourceConsolidationKind(n, at))
			for _, p := range n.Pods {
				if !p.DaemonSet && p.Phase.Active() {
					a.podsRemaining++
				}
			}
			if n.HasPrice {
				a.savings += n.Price
			} else {
				a.savingsKnown = false
			}
		}
	}

	// A target can belong to at most one action. When a NodePool has overlapping
	// disruptions, creation time chooses the nearest source burst rather than
	// painting the same replacement with two action IDs.
	for _, n := range snap.Nodes {
		if sourceNames[n.Name] || !isReplacementTarget(n, now) || n.Created.IsZero() {
			continue
		}
		best, bestDistance := -1, time.Duration(1<<63-1)
		for i := range view.actions {
			a := &view.actions[i]
			// A known delete action cannot own a replacement. This is important in
			// busy pools where an unrelated scale-up may happen beside an empty-node
			// consolidation and otherwise look like its destination.
			if a.kind == consolidationKindEmpty || a.kind == consolidationKindDelete ||
				a.pool != n.NodePool || n.Created.Before(a.started.Add(-consolidationJoinSlack)) || n.Created.After(a.started.Add(consolidationJoinWindow)) {
				continue
			}
			distance := n.Created.Sub(a.started)
			if distance < 0 {
				distance = -distance
			}
			if distance < bestDistance {
				best, bestDistance = i, distance
			}
		}
		if best < 0 {
			continue
		}
		a := &view.actions[best]
		a.targets = append(a.targets, n)
		a.kind = consolidationKindReplace
		if n.HasPrice {
			a.savings -= n.Price
		} else {
			a.savingsKnown = false
		}
	}
	for i := range view.actions {
		sort.Slice(view.actions[i].sources, func(a, b int) bool { return view.actions[i].sources[a].Name < view.actions[i].sources[b].Name })
		sort.Slice(view.actions[i].targets, func(a, b int) bool { return view.actions[i].targets[a].Name < view.actions[i].targets[b].Name })
	}

	sort.SliceStable(view.actions, func(i, j int) bool {
		if view.actions[i].started.Equal(view.actions[j].started) {
			return view.actions[i].pool < view.actions[j].pool
		}
		return view.actions[i].started.Before(view.actions[j].started)
	})
	for i := range view.actions {
		a := &view.actions[i]
		a.id = fmt.Sprintf("C%d", i+1)
		waiting := hasProvisioningTarget(*a)
		for _, n := range a.sources {
			stage := "DRAINING"
			switch n.Phase {
			case model.PhaseTerminating, model.PhaseGone:
				stage = "TERMINATING"
			case model.PhaseDraining:
				if waiting {
					stage = "WAITING"
				}
			}
			if stage == "DRAINING" {
				switch a.kind {
				case consolidationKindEmpty:
					stage = "EMPTY"
				case consolidationKindDelete:
					stage = "MOVING"
				}
			}
			view.members[n.Name] = consolidationMember{id: a.id, role: consolidationOutgoing, kind: a.kind, stage: stage}
		}
		for _, n := range a.targets {
			stage := "RECEIVING"
			if n.Phase == model.PhaseProvisioning {
				stage = "STARTING"
			}
			view.members[n.Name] = consolidationMember{id: a.id, role: consolidationIncoming, kind: a.kind, stage: stage}
		}
	}
	return view
}

func isConsolidationSource(n *model.Node) bool {
	if n == nil || (n.Phase != model.PhaseDraining && n.Phase != model.PhaseTerminating && n.Phase != model.PhaseGone) {
		return false
	}
	if n.Phase != model.PhaseDraining && !hasPhaseTransition(n, model.PhaseDraining) {
		return false
	}
	reason := strings.ToLower(n.Message + " " + n.ConsolidationReason + " " + n.DisruptionReason)
	return n.Consolidatable == model.ConsolidationYes ||
		strings.Contains(reason, "underutilized") || strings.Contains(reason, "underutilised") ||
		strings.Contains(reason, "consolidat") || strings.Contains(reason, "empty")
}

func consolidationStartedAt(n *model.Node, now time.Time) time.Time {
	at := phaseTransitionAt(n, model.PhaseDraining)
	if at.IsZero() {
		at = n.ConsolidationAt
	}
	if at.IsZero() {
		return now
	}
	return at
}

func sourceConsolidationKind(n *model.Node, started time.Time) consolidationKind {
	if n == nil {
		return consolidationKindUnknown
	}
	if strings.EqualFold(strings.TrimSpace(n.DisruptionReason), "empty") {
		// The active disruption condition is newer and more authoritative than a
		// candidate verdict, which may still describe an earlier replacement
		// considered for the same node.
		return consolidationKindEmpty
	}
	if n.ConsolidationAt.IsZero() || durationAbs(n.ConsolidationAt.Sub(started)) > 2*time.Minute {
		return consolidationKindUnknown
	}
	decision := strings.ToLower(strings.TrimSpace(n.ConsolidationReason))
	switch {
	case strings.HasPrefix(decision, "replace:") || strings.Contains(decision, ": replace:"):
		return consolidationKindReplace
	case strings.HasPrefix(decision, "delete:") || strings.Contains(decision, ": delete:"):
		return consolidationKindDelete
	default:
		return consolidationKindUnknown
	}
}

func mergeConsolidationKind(current, next consolidationKind) consolidationKind {
	if current == consolidationKindUnknown {
		return next
	}
	if next == consolidationKindUnknown || current == next {
		return current
	}
	// Conflicting source evidence is safer to present as generic consolidation
	// than to assert either a deletion or a replacement for the whole group.
	return consolidationKindUnknown
}

func durationAbs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func isReplacementTarget(n *model.Node, now time.Time) bool {
	if n == nil {
		return false
	}
	if n.Phase == model.PhaseProvisioning && n.NodeClaim != "" {
		return true
	}
	if n.Phase != model.PhaseReady {
		return false
	}
	at := phaseTransitionAt(n, model.PhaseReady)
	age := now.Sub(at)
	return hasPhaseTransition(n, model.PhaseProvisioning) && !at.IsZero() && age >= 0 && age <= targetReadyHold
}

func hasPhaseTransition(n *model.Node, phase model.Phase) bool {
	return !phaseTransitionAt(n, phase).IsZero()
}

func phaseTransitionAt(n *model.Node, phase model.Phase) time.Time {
	if n == nil {
		return time.Time{}
	}
	for i := len(n.Transitions) - 1; i >= 0; i-- {
		if n.Transitions[i].Phase == phase {
			return n.Transitions[i].At
		}
	}
	return time.Time{}
}

func hasProvisioningTarget(a consolidationAction) bool {
	for _, n := range a.targets {
		if n.Phase == model.PhaseProvisioning {
			return true
		}
	}
	return false
}

func actionBadgeForWidth(member consolidationMember, maxWidth int) string {
	if maxWidth <= 0 || member.id == "" {
		return ""
	}
	direction := "↓"
	if member.role == consolidationIncoming {
		direction = "↑"
	}
	for _, label := range []string{
		member.id + " " + direction + " " + member.stage,
		member.id + " " + direction,
		direction,
	} {
		if runewidth.StringWidth(label) <= maxWidth {
			return label
		}
	}
	return ""
}

func renderConsolidationRibbon(w int, view consolidationView) string {
	t := theme.Current
	bg := theme.Mix(t.Panel, t.Accent, 0.18)
	c := newCanvas(w, 1, bg, t.PanelFg)
	c.rect(0, 0, w, 1, bg)
	if len(view.actions) == 0 {
		return c.String()
	}

	text := actionSummary(view.actions[0])
	if len(view.actions) > 1 {
		sources, targets, pods := 0, 0, 0
		kinds := map[consolidationKind]int{}
		hasWorkloadMove := false
		for _, a := range view.actions {
			sources += len(a.sources)
			targets += len(a.targets)
			pods += a.podsRemaining
			kinds[a.kind]++
			hasWorkloadMove = hasWorkloadMove || a.kind != consolidationKindEmpty
		}
		var breakdown []string
		for _, kind := range []consolidationKind{consolidationKindEmpty, consolidationKindDelete, consolidationKindReplace, consolidationKindUnknown} {
			if kinds[kind] > 0 {
				breakdown = append(breakdown, fmt.Sprintf("%d %s", kinds[kind], strings.ToLower(consolidationKindLabel(kind))))
			}
		}
		flow := fmt.Sprintf("%d out", sources)
		if targets > 0 {
			flow += fmt.Sprintf(" → %d in", targets)
		}
		text = fmt.Sprintf("↻ %d ACTIONS  %s · %s", len(view.actions), strings.Join(breakdown, " · "), flow)
		if hasWorkloadMove {
			text += fmt.Sprintf(" · %d pods remaining", pods)
		}
	}
	c.text(1, 0, shorten(text, max(1, w-2)), t.Accent, true)
	return c.String()
}

func actionSummary(a consolidationAction) string {
	destination := ""
	switch a.kind {
	case consolidationKindDelete:
		destination = " → existing capacity"
	case consolidationKindReplace:
		if len(a.targets) > 0 {
			destination = fmt.Sprintf(" → %d in", len(a.targets))
		} else {
			destination = " → replacement pending"
		}
	case consolidationKindUnknown:
		if len(a.targets) > 0 {
			destination = fmt.Sprintf(" → %d in", len(a.targets))
		}
	}
	stage := "draining"
	switch {
	case hasProvisioningTarget(a):
		stage = "waiting for replacement"
	case actionHasTerminalSource(a):
		stage = "terminating"
	case a.kind == consolidationKindEmpty:
		stage = "removing empty node"
	case a.kind == consolidationKindDelete:
		stage = "moving pods"
	}

	typeLabel := consolidationKindLabel(a.kind)
	text := fmt.Sprintf("↻ %s  %s  %d out%s · %s", a.id, typeLabel, len(a.sources), destination, stage)
	if a.kind != consolidationKindEmpty {
		text += fmt.Sprintf(" · %d pods remaining", a.podsRemaining)
	}
	if a.savingsKnown && a.savings > 0 {
		text += fmt.Sprintf(" · est −$%.2f/hr", a.savings)
	}
	return text
}

func consolidationKindLabel(kind consolidationKind) string {
	switch kind {
	case consolidationKindEmpty:
		return "EMPTY SCALE-DOWN"
	case consolidationKindDelete:
		return "BIN-PACK SCALE-DOWN"
	case consolidationKindReplace:
		return "REPLACEMENT"
	default:
		return "CONSOLIDATING"
	}
}

func consolidationKindCode(kind consolidationKind) string {
	switch kind {
	case consolidationKindEmpty:
		return "E"
	case consolidationKindDelete:
		return "B"
	case consolidationKindReplace:
		return "R"
	default:
		return "C"
	}
}

func actionHasTerminalSource(a consolidationAction) bool {
	for _, n := range a.sources {
		if n.Phase == model.PhaseTerminating || n.Phase == model.PhaseGone {
			return true
		}
	}
	return false
}
