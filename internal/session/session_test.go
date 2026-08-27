package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

func TestRecorderRoundTrip(t *testing.T) {
	for _, suffix := range []string{".knv", ".knv.gz"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session"+suffix)
			r, err := NewRecorder(path)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 8, 26, 12, 0, 0, 123, time.UTC)
			snapshot := &model.Snapshot{Generation: 7, Taken: at, Context: "prod",
				Nodes: []*model.Node{{Name: "node-a", Phase: model.PhaseReady,
					Pods: []*model.Pod{{Namespace: "default", Name: "web", NodeName: "node-a"}}}}}
			detail := &model.NodeDetail{Name: "node-a", Kind: "Node", ProviderID: "cloud:///i-1", FetchedAt: at,
				Events: []model.Event{{Kind: "Node", Object: "node-a", Reason: "Ready", First: at, Last: at}}}
			if err := r.RecordSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			if err := r.RecordDetail("node-a", "claim-a", detail); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}

			got, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Snapshots) != 1 || !reflect.DeepEqual(got.Snapshots[0], snapshot) {
				t.Fatalf("snapshot round trip = %#v", got.Snapshots)
			}
			if len(got.Details) != 1 || !reflect.DeepEqual(got.Details[0].Detail, detail) {
				t.Fatalf("detail round trip = %#v", got.Details)
			}
			if got.Details[0].Identity != "provider:cloud:///i-1" {
				t.Fatalf("detail identity = %q", got.Details[0].Identity)
			}
		})
	}
}

func TestRecorderRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.knv")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecorder(path); err == nil {
		t.Fatal("NewRecorder overwrote an existing file")
	}
}

func TestLoadKeepsCompleteRecordsBeforePartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.knv")
	r, err := NewRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &model.Snapshot{Generation: 1, Taken: time.Now()}
	if err := r.RecordSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := r.out.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(mustRead(t, path), []byte(`{"type":"snap`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Snapshots) != 1 || got.Snapshots[0].Generation != 1 {
		t.Fatalf("snapshots after partial tail = %#v", got.Snapshots)
	}
}

func TestStreamStaysOpenAtRecordingEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := Stream(ctx, []*model.Snapshot{{Generation: 1}})
	if snap := <-ch; snap.Generation != 1 {
		t.Fatalf("streamed snapshot = %#v", snap)
	}
	select {
	case <-ch:
		t.Fatal("stream closed at the end of the recording")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("stream stayed open after cancellation")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
