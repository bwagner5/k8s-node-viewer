package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// RecordingController is implemented by the session writer without coupling
// the UI package to its file-format implementation.
type RecordingController interface {
	Start(path string, initial *model.Snapshot) (string, error)
	Stop() (string, error)
	Active() bool
	Path() string
}

func (m *Model) runRecording(arg string) (string, error) {
	if m.recording == nil {
		return "", fmt.Errorf("recording is unavailable while replaying a session")
	}
	raw := strings.TrimSpace(arg)
	switch strings.ToLower(raw) {
	case "status":
		if m.recording.Active() {
			return "recording to " + m.recording.Path(), nil
		}
		return "not currently recording", nil
	case "stop", "off":
		path, err := m.recording.Stop()
		if err != nil {
			return "", err
		}
		return "recording saved to " + path, nil
	case "start", "on":
		if m.recording.Active() {
			return "", fmt.Errorf("already recording to %s", m.recording.Path())
		}
		raw = ""
	}

	if raw == "" && m.recording.Active() {
		path, err := m.recording.Stop()
		if err != nil {
			return "", err
		}
		return "recording saved to " + path, nil
	}
	path, err := m.recording.Start(raw, m.playback.latest)
	if err != nil {
		return "", err
	}
	// Do not wait up to one sampling interval before persisting node events.
	m.detailCaptureAt = time.Time{}
	return "recording to " + path, nil
}

func (m *Model) toggleRecording() {
	message, err := m.runRecording("")
	if err != nil {
		m.notify(err.Error(), true)
		return
	}
	m.notify(message, false)
}

func (m *Model) recordingActive() bool {
	return m.recording != nil && m.recording.Active()
}
