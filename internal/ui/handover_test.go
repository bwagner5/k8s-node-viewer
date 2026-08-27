package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

const (
	testClaim    = "general-abc12"
	testProvider = "aws:///us-west-2a/i-0abc12"
	testNodeName = "ip-10-9-9-9"
)

// claimDetail is the payload the live source builds from a NodeClaim: capacity
// and conditions, no kubelet and no addresses, because there is no Node yet.
func claimDetail(name string) *model.NodeDetail {
	born := time.Now().Add(-40 * time.Second)
	return &model.NodeDetail{
		Name:        name,
		Kind:        "NodeClaim",
		ProviderID:  testProvider,
		FetchedAt:   time.Now(),
		Capacity:    model.Resources{CPUMilli: 16000, MemBytes: 64 << 30, Pods: 110},
		Allocatable: model.Resources{CPUMilli: 15800, MemBytes: 63 << 30, Pods: 110},
		Labels:      map[string]string{"karpenter.sh/nodepool": "general"},
		Conditions: []model.Condition{
			{Type: "Launched", Status: "True", Reason: "Launched", Changed: born},
			{Type: "Registered", Status: "Unknown", Reason: "NotRegistered", Changed: born},
		},
		Events: []model.Event{{Kind: "NodeClaim", Object: testClaim, Type: "Normal",
			Reason: "LaunchedByClaim", Component: "karpenter", Message: "Launched instance: m5.4xlarge",
			Count: 1, First: born, Last: born}},
	}
}

// kindDescriber answers with the claim payload for a claim name and the node
// payload for a node name, which is what a real source does — and is the whole
// behaviour under test, since the pane has to change which one it is asking for.
type kindDescriber struct {
	names []string
}

func (k *kindDescriber) DescribeNode(_ context.Context, name, _ string) (*model.NodeDetail, error) {
	k.names = append(k.names, name)
	if name == testClaim {
		return claimDetail(name), nil
	}
	d := sampleDetail(name)
	d.Kind = "Node"
	return d, nil
}

// claimModel is a grid with a provisioning NodeClaim placeholder in it, named
// after the claim exactly as the store emits one.
func claimModel(t *testing.T) *Model {
	t.Helper()
	m := agedTestModel(t, 150, 44, 6)
	snap := m.snap
	snap.Nodes = append(snap.Nodes, &model.Node{
		Name:         testClaim,
		NodeClaim:    testClaim,
		ProviderID:   testProvider,
		InstanceType: "m5.4xlarge",
		NodePool:     "general",
		Phase:        model.PhaseProvisioning,
		Message:      "Launched",
		Created:      time.Now().Add(-40 * time.Second),
		Allocatable:  model.Resources{CPUMilli: 15800, MemBytes: 63 << 30, Pods: 110},
	})
	m.applySnapshot(snap)
	selectNode(t, m, testClaim)
	return m
}

func selectNode(t *testing.T, m *Model, name string) {
	t.Helper()
	for i, v := range m.vis {
		if v.node.Name == name {
			m.setCursor(i)
			return
		}
	}
	t.Fatalf("%s is not in the visible grid", name)
}

// registered is the snapshot after kubelet checks in: the placeholder is gone
// and a Node with a different name, joined to the same claim, has taken its
// place. This is the transition the pane has to survive.
func registered() *model.Snapshot {
	snap := testSnapshot(6)
	snap.Nodes = append(snap.Nodes, &model.Node{
		Name:         testNodeName,
		NodeClaim:    testClaim,
		ProviderID:   testProvider,
		InstanceType: "m5.4xlarge",
		NodePool:     "general",
		Phase:        model.PhaseReady,
		Ready:        true,
		Schedulable:  true,
		Created:      time.Now().Add(-40 * time.Second),
		Allocatable:  model.Resources{CPUMilli: 15800, MemBytes: 63 << 30, Pods: 110},
	})
	return snap
}

// TestClaimPaneShowsClaimData covers the provisioning half: the pane opened on a
// NodeClaim must say what it is looking at and show the claim's own fields, not
// an event list on its own.
func TestClaimPaneShowsClaimData(t *testing.T) {
	m := claimModel(t)
	m.describe = &kindDescriber{}
	openPane(t, m)

	out := m.View()
	for _, want := range []string{"claim", "nodeclaim", testProvider, "Launched", "Registered", "LaunchedByClaim"} {
		if !strings.Contains(out, want) {
			t.Fatalf("claim pane is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "left the cluster") {
		t.Fatalf("claim pane reported the node as gone\n%s", out)
	}
}

// TestDetailPaneFollowsClaimIntoNode is the bug this all exists for: the claim's
// name stops existing the instant the Node registers, and the pane must switch to
// the Node rather than announcing that it has gone.
func TestDetailPaneFollowsClaimIntoNode(t *testing.T) {
	m := claimModel(t)
	src := &kindDescriber{}
	m.describe = src
	openPane(t, m)

	_, cmd := m.Update(snapshotMsg{registered()})

	if m.detail.name != testNodeName {
		t.Fatalf("pane still points at %q, want %q", m.detail.name, testNodeName)
	}
	if !m.detail.loading {
		t.Fatal("pane did not start reading the node it moved to")
	}
	// Esc has to land on the node, not on whatever inherited the claim's slot.
	if m.cursorName != testNodeName {
		t.Fatalf("cursor stayed on %q", m.cursorName)
	}
	// Until the node's read lands, the claim's payload is what is on screen —
	// nearly all of it is still true, and blanking the pane here would flicker.
	out := m.View()
	if !strings.Contains(out, "LaunchedByClaim") {
		t.Fatalf("pane blanked during the handover\n%s", out)
	}
	if strings.Contains(out, "left the cluster") {
		t.Fatalf("pane reported the node as gone mid-handover\n%s", out)
	}

	if cmd == nil {
		t.Fatal("the handover started no fetch")
	}
	msg, ok := detailFrom(cmd)
	if !ok {
		t.Fatal("the handover's command produced no detail fetch")
	}
	if msg.name != testNodeName {
		t.Fatalf("fetched %q, want %q", msg.name, testNodeName)
	}
	m.Update(msg)

	if m.detail.detail == nil || m.detail.detail.Kind != "Node" {
		t.Fatalf("pane did not switch to the node's payload: %+v", m.detail.detail)
	}
	out = m.View()
	if strings.Contains(out, "nodeclaim —") {
		t.Fatalf("pane still describes itself as a claim\n%s", out)
	}
	if !strings.Contains(out, testNodeName) {
		t.Fatalf("pane does not name the node\n%s", out)
	}
	if got := src.names; len(got) != 2 || got[0] != testClaim || got[1] != testNodeName {
		t.Fatalf("source was asked for %v, want [claim node]", got)
	}
}

// TestHandoverIgnoresAnUnrelatedNode makes sure the follow is keyed on identity
// rather than on "some node appeared": a node that leaves for good must still
// report that it has gone.
func TestHandoverIgnoresAnUnrelatedNode(t *testing.T) {
	m := claimModel(t)
	m.describe = &kindDescriber{}
	openPane(t, m)

	// The claim vanishes without ever becoming a node — a launch failure.
	m.Update(snapshotMsg{testSnapshot(6)})

	if m.detail.name != testClaim {
		t.Fatalf("pane followed something unrelated: %q", m.detail.name)
	}
	if out := m.View(); !strings.Contains(out, "left the cluster") {
		t.Fatalf("pane did not report the claim disappearing\n%s", out)
	}
}

// detailFrom runs a command, walking into a batch, and returns the detail fetch
// inside it. Update batches the handover's fetch with the next snapshot wait, so
// the shape depends on what else the model wanted at the time.
func detailFrom(cmd tea.Cmd) (detailMsg, bool) {
	switch msg := cmd().(type) {
	case detailMsg:
		return msg, true
	case tea.BatchMsg:
		for _, c := range msg {
			if c == nil {
				continue
			}
			if got, ok := detailFrom(c); ok {
				return got, true
			}
		}
	}
	return detailMsg{}, false
}
