package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

type stubRecordingController struct {
	active  bool
	path    string
	initial *model.Snapshot
}

func (s *stubRecordingController) Start(path string, initial *model.Snapshot) (string, error) {
	s.active, s.initial = true, initial
	if path == "" {
		path = "/tmp/recording001.knv"
	}
	s.path = path
	return path, nil
}

func (s *stubRecordingController) Stop() (string, error) {
	s.active = false
	return s.path, nil
}

func (s *stubRecordingController) Active() bool { return s.active }
func (s *stubRecordingController) Path() string { return s.path }

func TestRecordCommandStartsWithLatestSourceSnapshotAndStops(t *testing.T) {
	controller := &stubRecordingController{}
	m := New(Config{Recording: controller, CaptureDetails: true})
	snap := &model.Snapshot{Generation: 9}
	m.playback.Ingest(snap, m.last)

	message, err := m.Run(":record")
	if err != nil {
		t.Fatal(err)
	}
	if !controller.active || controller.initial != snap || !strings.Contains(message, "recording to") {
		t.Fatalf("start active=%v initial=%#v message=%q", controller.active, controller.initial, message)
	}
	message, err = m.Run(":record stop")
	if err != nil {
		t.Fatal(err)
	}
	if controller.active || !strings.Contains(message, "recording saved to /tmp/recording001.knv") {
		t.Fatalf("stop active=%v message=%q", controller.active, message)
	}
}

func TestRecordingKeyTogglesAndShowsSavedPathToast(t *testing.T) {
	controller := &stubRecordingController{}
	m := New(Config{Recording: controller})
	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}}
	m.handleKey(key)
	if !controller.active || !strings.Contains(m.msg, "recording to") {
		t.Fatalf("start toast=%q active=%v", m.msg, controller.active)
	}
	m.handleKey(key)
	if controller.active || !strings.Contains(m.msg, "recording saved to /tmp/recording001.knv") {
		t.Fatalf("stop toast=%q active=%v", m.msg, controller.active)
	}
}

func TestRuntimeDetailSamplingOnlyRunsWhileRecording(t *testing.T) {
	controller := &stubRecordingController{}
	describer := &stubDescriber{detail: sampleDetail("")}
	m := New(Config{Recording: controller, CaptureDetails: true, Describe: describer})
	m.playback.Ingest(testSnapshot(1), m.last)
	if cmd := m.captureDetailHistory(m.last); cmd != nil {
		t.Fatal("inactive recorder triggered cluster-wide detail sampling")
	}
	controller.active = true
	if cmd := m.captureDetailHistory(m.last); cmd == nil {
		t.Fatal("active recorder did not trigger detail sampling")
	}
}
