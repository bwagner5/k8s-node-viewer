package model

import (
	"testing"
	"time"
)

// Karpenter reports its disruption verdict against the NodeClaim it is reasoning
// about, and the event often lands before the Node does. Both join paths and the
// ordering rules are what the table column depends on.

func consolidationOf(t *testing.T, s *Store, name string) *Node {
	t.Helper()
	for _, n := range s.Snapshot().Nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("node %q not in the snapshot", name)
	return nil
}

func TestConsolidationJoinsByNodeAndByClaim(t *testing.T) {
	s := NewStore("test")

	// Keyed by the node's own name.
	s.UpsertNode(registeredNode("node-a"))
	s.SetConsolidation("node-a", ConsolidationYes, "underutilized", time.Now())
	if got := consolidationOf(t, s, "node-a"); got.Consolidatable != ConsolidationYes {
		t.Fatalf("node-a verdict %v, want yes", got.Consolidatable)
	}

	// Keyed by the claim, arriving before the Node exists — the usual order.
	s.SetConsolidation("claim-b", ConsolidationNo, "Can't remove without creating 1 candidate", time.Now())
	s.UpsertClaim("claim-b", claimPlaceholder("claim-b", "node-b"))
	s.UpsertNode(registeredNode("node-b"))
	got := consolidationOf(t, s, "node-b")
	if got.Consolidatable != ConsolidationNo {
		t.Fatalf("node-b verdict %v, want no", got.Consolidatable)
	}
	if got.ConsolidationReason == "" || got.ConsolidationAt.IsZero() {
		t.Fatalf("verdict lost its message or timestamp: %+v", got)
	}

	// A node nobody has judged says so, rather than defaulting to either answer.
	s.UpsertNode(registeredNode("node-c"))
	if got := consolidationOf(t, s, "node-c"); got.Consolidatable != ConsolidationUnknown {
		t.Fatalf("unjudged node reports %v, want unknown", got.Consolidatable)
	}
}

func TestConsolidationIgnoresOlderVerdicts(t *testing.T) {
	s := NewStore("test")
	s.UpsertNode(registeredNode("node-a"))

	now := time.Now()
	s.SetConsolidation("node-a", ConsolidationNo, "newer", now)
	// An informer resync replays its cache in map order, so an older event
	// arriving after a newer one is routine. It must not win, or the column
	// flickers on every resync.
	s.SetConsolidation("node-a", ConsolidationYes, "older", now.Add(-10*time.Minute))

	got := consolidationOf(t, s, "node-a")
	if got.Consolidatable != ConsolidationNo || got.ConsolidationReason != "newer" {
		t.Fatalf("older verdict won: %v %q", got.Consolidatable, got.ConsolidationReason)
	}

	// The most recent of a node's two possible keys is the one that counts.
	s.UpsertClaim("claim-a", claimPlaceholder("claim-a", "node-a"))
	s.SetConsolidation("claim-a", ConsolidationYes, "from the claim, later", now.Add(time.Minute))
	if got := consolidationOf(t, s, "node-a"); got.Consolidatable != ConsolidationYes {
		t.Fatalf("later claim verdict ignored: %v %q", got.Consolidatable, got.ConsolidationReason)
	}
}

func TestConsolidationExpires(t *testing.T) {
	s := NewStore("test")
	s.UpsertNode(registeredNode("node-a"))
	s.SetConsolidation("node-a", ConsolidationYes, "stale", time.Now().Add(-2*ConsolidationTTL))

	// A verdict Karpenter has not renewed is not evidence of anything: a stale
	// "yes" on a node that has since filled up is worse than admitting ignorance.
	if got := consolidationOf(t, s, "node-a"); got.Consolidatable != ConsolidationUnknown {
		t.Fatalf("expired verdict still reported: %v", got.Consolidatable)
	}
	// And reap clears it, reporting the change so the column repaints.
	if !s.reap(time.Now()) {
		t.Fatal("reap did not report the expiry")
	}
	if len(s.consolidation) != 0 {
		t.Fatalf("expired verdict left behind: %v", s.consolidation)
	}
}

func TestConsolidationShortCells(t *testing.T) {
	// The dense column is one glyph wide, so every state needs exactly one.
	for _, c := range []Consolidation{ConsolidationUnknown, ConsolidationYes, ConsolidationNo} {
		if got := len([]rune(c.Short())); got != 1 {
			t.Fatalf("%v renders %d cells: %q", c, got, c.Short())
		}
	}
	if ConsolidationYes.Short() == ConsolidationNo.Short() {
		t.Fatal("yes and no render the same glyph")
	}
}
