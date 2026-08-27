package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// Manager owns zero or one active Recorder and can start and stop it while the
// source keeps running. Its wrappers remain installed for the lifetime of the
// UI and become no-ops whenever recording is inactive.
type Manager struct {
	mu         sync.Mutex
	recorder   *Recorder
	path       string
	created    []string
	defaultDir string
}

func NewManager() *Manager { return &Manager{} }

// Start begins a new recording and optionally writes the current source state
// as its first timeline anchor. An empty path chooses recordingNNN.knv in the
// default directory.
func (m *Manager) Start(path string, initial *model.Snapshot) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recorder != nil {
		return "", fmt.Errorf("already recording to %s", m.path)
	}
	resolved, err := m.resolvePath(path)
	if err != nil {
		return "", err
	}
	recorder, err := NewRecorder(resolved)
	if err != nil {
		return "", err
	}
	m.recorder, m.path = recorder, resolved
	m.created = append(m.created, resolved)
	if initial != nil {
		if err := recorder.RecordSnapshot(initial); err != nil {
			_ = recorder.Close()
			m.recorder, m.path = nil, ""
			return "", err
		}
	}
	return resolved, nil
}

func (m *Manager) resolvePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		if m.defaultDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("find home directory for recordings: %w", err)
			}
			m.defaultDir = filepath.Join(home, ".config", "knv")
		}
		if err := os.MkdirAll(m.defaultDir, 0o700); err != nil {
			return "", fmt.Errorf("create recording directory %s: %w", m.defaultDir, err)
		}
		for i := 1; i <= 999999; i++ {
			candidate := filepath.Join(m.defaultDir, fmt.Sprintf("recording%03d.knv", i))
			if _, err := os.Stat(candidate); errorsIsNotExist(err) {
				return candidate, nil
			} else if err != nil {
				return "", fmt.Errorf("inspect recording path %s: %w", candidate, err)
			}
		}
		return "", fmt.Errorf("no unused recording filename in %s", m.defaultDir)
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory for recording path: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve recording path %s: %w", path, err)
	}
	return filepath.Clean(abs), nil
}

// errorsIsNotExist is kept tiny so resolvePath reads as filename allocation,
// not as a negated os.Stat truth table.
func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }

// Stop closes the active recording and returns its absolute path.
func (m *Manager) Stop() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recorder == nil {
		return "", fmt.Errorf("not currently recording")
	}
	path, recorder := m.path, m.recorder
	err := recorder.Close()
	m.recorder, m.path = nil, ""
	return path, err
}

func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recorder != nil
}

func (m *Manager) Path() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.path
}

// Close stops an active recording and returns every path created during this
// process, including recordings the user already stopped from the UI.
func (m *Manager) Close() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if m.recorder != nil {
		err = m.recorder.Close()
		m.recorder, m.path = nil, ""
	}
	return append([]string(nil), m.created...), err
}

func (m *Manager) recordSnapshot(snap *model.Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recorder == nil {
		return nil
	}
	return m.recorder.RecordSnapshot(snap)
}

func (m *Manager) recordDetail(name, claim string, detail *model.NodeDetail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recorder == nil {
		return nil
	}
	return m.recorder.RecordDetail(name, claim, detail)
}

// WrapSnapshots records each source value before it reaches playback whenever
// recording is active.
func (m *Manager) WrapSnapshots(ctx context.Context, in <-chan *model.Snapshot) <-chan *model.Snapshot {
	out := make(chan *model.Snapshot, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case snap, ok := <-in:
				if !ok {
					return
				}
				if err := m.recordSnapshot(snap); err != nil {
					return
				}
				select {
				case out <- snap:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

type managedDescriber struct {
	manager *Manager
	source  Describer
}

func (m *Manager) WrapDescriber(source Describer) Describer {
	if source == nil {
		return nil
	}
	return &managedDescriber{manager: m, source: source}
}

func (d *managedDescriber) DescribeNode(ctx context.Context, name, claim string) (*model.NodeDetail, error) {
	detail, err := d.source.DescribeNode(ctx, name, claim)
	if err != nil || detail == nil {
		return detail, err
	}
	if err := d.manager.recordDetail(name, claim, detail); err != nil {
		return nil, err
	}
	return detail, nil
}
