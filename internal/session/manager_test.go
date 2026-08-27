package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

func TestManagerAllocatesDefaultPathAndAnchorsCurrentState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "knv")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recording001.knv"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{defaultDir: dir}
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	initial := &model.Snapshot{Generation: 42, Taken: at}
	path, err := m.Start("", initial)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "recording002.knv"); path != want {
		t.Fatalf("default path = %q, want %q", path, want)
	}
	if !m.Active() || m.Path() != path {
		t.Fatalf("manager state active=%v path=%q", m.Active(), m.Path())
	}
	stopped, err := m.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if stopped != path || m.Active() {
		t.Fatalf("stopped path=%q active=%v", stopped, m.Active())
	}
	data, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Snapshots) != 1 || data.Snapshots[0].Generation != 42 {
		t.Fatalf("runtime recording snapshots = %#v", data.Snapshots)
	}
	paths, err := m.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != path {
		t.Fatalf("created paths = %v", paths)
	}
}

func TestManagerCanStartAnotherRecordingAfterStop(t *testing.T) {
	m := &Manager{defaultDir: t.TempDir()}
	first, err := m.Start("", &model.Snapshot{Generation: 1, Taken: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	second, err := m.Start("", &model.Snapshot{Generation: 2, Taken: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := m.Close()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(paths) != 2 {
		t.Fatalf("recording paths first=%q second=%q all=%v", first, second, paths)
	}
}
