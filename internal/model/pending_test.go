package model

import "testing"

func pendingPod(name string, unschedulable bool) *Pod {
	return &Pod{
		Namespace:     "shop",
		Name:          name,
		Phase:         PodPending,
		Unschedulable: unschedulable,
		Requests:      Resources{CPUMilli: 500, MemBytes: 1 << 30, Pods: 1},
	}
}

func TestPendingPodsAreCountedNotDropped(t *testing.T) {
	s := NewStore("test")
	s.UpsertNode(&Node{Name: "node-a", Phase: PhaseReady,
		Allocatable: Resources{CPUMilli: 4000, MemBytes: 8 << 30, Pods: 110}})
	s.UpsertPod(pendingPod("waiting", false))
	s.UpsertPod(pendingPod("refused", true))

	snap := s.Snapshot()
	if snap.Totals.Pending != 2 || snap.Totals.Unschedulable != 1 {
		t.Fatalf("pending=%d unschedulable=%d, want 2 and 1",
			snap.Totals.Pending, snap.Totals.Unschedulable)
	}
	// A pod with no node is not on any node, and must not inflate the placed
	// count or any node's requests.
	if snap.Totals.Pods != 0 {
		t.Errorf("placed pods = %d, want 0", snap.Totals.Pods)
	}
	if got := snap.Nodes[0].Requests.CPUMilli; got != 0 {
		t.Errorf("node requests = %dm, want 0: backlog belongs to no node", got)
	}
}

func TestSchedulingAPendingPodMovesItOntoTheNode(t *testing.T) {
	s := NewStore("test")
	s.UpsertNode(&Node{Name: "node-a", Phase: PhaseReady,
		Allocatable: Resources{CPUMilli: 4000, MemBytes: 8 << 30, Pods: 110}})

	p := pendingPod("waiting", true)
	s.UpsertPod(p)

	placed := *p
	placed.NodeName, placed.Unschedulable, placed.Phase = "node-a", false, PodRunning
	s.UpsertPod(&placed)

	snap := s.Snapshot()
	if snap.Totals.Pending != 0 || snap.Totals.Unschedulable != 0 {
		t.Errorf("pending=%d unschedulable=%d after scheduling, want 0 and 0",
			snap.Totals.Pending, snap.Totals.Unschedulable)
	}
	if snap.Totals.Pods != 1 || len(snap.Nodes[0].Pods) != 1 {
		t.Fatalf("placed pods = %d, node pods = %d, want 1 and 1",
			snap.Totals.Pods, len(snap.Nodes[0].Pods))
	}

	// And back again: a preempted pod is recreated with no node, and must leave
	// the box it was drawn in.
	s.UpsertPod(pendingPod("waiting", false))
	snap = s.Snapshot()
	if snap.Totals.Pending != 1 || snap.Totals.Pods != 0 || len(snap.Nodes[0].Pods) != 0 {
		t.Errorf("pending=%d placed=%d node pods=%d, want 1, 0, 0",
			snap.Totals.Pending, snap.Totals.Pods, len(snap.Nodes[0].Pods))
	}
}

func TestFinishedPodWithNoNodeIsNotBacklog(t *testing.T) {
	s := NewStore("test")
	done := pendingPod("finished", false)
	done.Phase = PodSucceeded
	s.UpsertPod(done)

	// A completed job pod keeps no node and is not waiting for one.
	if got := s.Snapshot().Totals.Pending; got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}

	s.UpsertPod(pendingPod("finished", false))
	if got := s.Snapshot().Totals.Pending; got != 1 {
		t.Errorf("pending = %d after the pod went back to pending, want 1", got)
	}
	s.DeletePod("shop/finished")
	if got := s.Snapshot().Totals.Pending; got != 0 {
		t.Errorf("pending = %d after delete, want 0", got)
	}
}
