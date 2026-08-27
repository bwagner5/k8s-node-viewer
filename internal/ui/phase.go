package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// recentPhaseWindow is long enough for someone presenting the cluster to point
// out a transition after it happened, without making old state look current.
const recentPhaseWindow = 10 * time.Second

// removedHold keeps the truthful REMOVED tombstone expanded long enough to be
// seen before its existing exit animation begins. The store retains tombstones
// for three seconds, so this adds observability without delaying cluster state.
const removedHold = 2 * time.Second

func removalReadyToCollapse(n *model.Node, now time.Time) bool {
	return n.Phase == model.PhaseGone &&
		(n.DeletedAt.IsZero() || !now.Before(n.DeletedAt.Add(removedHold)))
}

func phaseLabel(p model.Phase) string {
	switch p {
	case model.PhaseNotReady:
		return "not ready"
	default:
		return strings.ToLower(p.String())
	}
}

func phaseIcon(p model.Phase) string {
	switch p {
	case model.PhaseProvisioning:
		return "◇"
	case model.PhaseReady:
		return "✓"
	case model.PhaseCordoned:
		return "⏸"
	case model.PhaseDraining:
		return "▼"
	case model.PhaseTerminating, model.PhaseGone:
		return "✕"
	case model.PhaseNotReady:
		return "!"
	default:
		return "?"
	}
}

func phaseChipLabel(p model.Phase) string {
	return phaseIcon(p) + " " + strings.ToUpper(phaseLabel(p))
}

func phaseShortLabel(p model.Phase) string {
	switch p {
	case model.PhaseProvisioning:
		return "new"
	case model.PhaseNotReady:
		return "down"
	case model.PhaseCordoned:
		return "cord"
	case model.PhaseDraining:
		return "drain"
	case model.PhaseTerminating:
		return "term"
	case model.PhaseGone:
		return "removed"
	default:
		return phaseLabel(p)
	}
}

// phaseDescription keeps the primary state and its supporting facts separate.
// A draining node is still unschedulable, but that property does not compete
// with DRAINING as a second primary badge.
func phaseDescription(n *model.Node) string {
	if n == nil {
		return ""
	}
	var facts []string
	if !n.Schedulable && (n.Phase == model.PhaseDraining || n.Phase == model.PhaseTerminating) {
		facts = append(facts, "scheduling disabled")
	}
	if n.Message != "" && !strings.EqualFold(n.Message, phaseLabel(n.Phase)) {
		facts = append(facts, n.Message)
	}
	return strings.Join(facts, " · ")
}

// phaseBadgeForWidth always retains at least the phase icon. Ready is the one
// exception: steady state stays quiet so exceptional nodes own the picture.
func phaseBadgeForWidth(p model.Phase, maxWidth int) string {
	if p == model.PhaseReady || maxWidth <= 0 {
		return ""
	}
	for _, label := range []string{
		phaseChipLabel(p),
		phaseIcon(p) + " " + strings.ToUpper(phaseShortLabel(p)),
		phaseIcon(p),
	} {
		if runewidth.StringWidth(label) <= maxWidth {
			return label
		}
	}
	return ""
}

// recentTransitions returns the tail worth showing as an observational
// breadcrumb. It never changes the node's current Phase.
func recentTransitions(n *model.Node, now time.Time) []model.PhaseTransition {
	if n == nil || len(n.Transitions) < 2 {
		return nil
	}
	cutoff := now.Add(-recentPhaseWindow)
	if n.Transitions[len(n.Transitions)-1].At.Before(cutoff) {
		return nil
	}
	start := len(n.Transitions) - 1
	for start > 0 && !n.Transitions[start-1].At.Before(cutoff) {
		start--
	}
	// Include one predecessor so the first recent change has context.
	if start > 0 {
		start--
	}
	out := n.Transitions[start:]
	if len(out) > 3 {
		out = out[len(out)-3:]
	}
	return out
}

func recentPhaseTrail(n *model.Node, now time.Time, compact bool) string {
	ts := recentTransitions(n, now)
	if len(ts) < 2 {
		return ""
	}
	parts := make([]string, 0, len(ts))
	for _, tr := range ts {
		if compact {
			parts = append(parts, phaseShortLabel(tr.Phase))
		} else {
			parts = append(parts, phaseLabel(tr.Phase))
		}
	}
	return strings.Join(parts, " → ")
}

func densePhaseState(n *model.Node, now time.Time) string {
	state := phaseIcon(n.Phase) + " " + phaseLabel(n.Phase)
	ts := recentTransitions(n, now)
	if len(ts) >= 2 {
		state += " ← " + phaseShortLabel(ts[len(ts)-2].Phase)
	}
	return state
}

func phaseHistoryLabel(n *model.Node, now time.Time) string {
	if n == nil || len(n.Transitions) < 2 {
		return ""
	}
	ts := n.Transitions
	if len(ts) > 5 {
		ts = ts[len(ts)-5:]
	}
	parts := make([]string, 0, len(ts))
	for _, tr := range ts {
		age := now.Sub(tr.At)
		when := "now"
		if age >= time.Second {
			when = fmt.Sprintf("%s ago", model.HumanAge(age))
		}
		parts = append(parts, fmt.Sprintf("%s %s", phaseLabel(tr.Phase), when))
	}
	return strings.Join(parts, " → ")
}
