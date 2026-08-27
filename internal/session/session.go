// Package session records and loads the immutable model values consumed by the
// UI. Recording at this boundary preserves source time: presentation controls
// can pause, seek, or change speed without changing the file.
package session

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

const (
	Format  = "k8s-node-viewer-session"
	Version = 1
)

type record struct {
	Type       string            `json:"type"`
	Format     string            `json:"format,omitempty"`
	Version    int               `json:"version,omitempty"`
	CreatedAt  time.Time         `json:"created_at,omitzero"`
	EndedAt    time.Time         `json:"ended_at,omitzero"`
	Snapshot   *model.Snapshot   `json:"snapshot,omitempty"`
	At         time.Time         `json:"at,omitzero"`
	Identity   string            `json:"identity,omitempty"`
	Name       string            `json:"name,omitempty"`
	NodeClaim  string            `json:"node_claim,omitempty"`
	ProviderID string            `json:"provider_id,omitempty"`
	Detail     *model.NodeDetail `json:"detail,omitempty"`
}

// Detail is one node-detail sample on the recorded source timeline.
type Detail struct {
	At         time.Time
	Identity   string
	Name       string
	NodeClaim  string
	ProviderID string
	Detail     *model.NodeDetail
}

// Data is a completely loaded session. Keeping all snapshots in memory is
// intentional: archived playback promises unrestricted seeking within the file.
type Data struct {
	CreatedAt time.Time
	EndedAt   time.Time
	Snapshots []*model.Snapshot
	Details   []Detail
}

type syncWriteCloser struct {
	writer io.Writer
	flush  func() error
	close  func() error
}

// Recorder appends independent JSON values to a file. A mutex serializes the
// snapshot stream and concurrent detail fetches without coupling their callers.
type Recorder struct {
	mu         sync.Mutex
	path       string
	out        *syncWriteCloser
	enc        *json.Encoder
	closed     bool
	err        error
	detailHash map[string][sha256.Size]byte
}

// NewRecorder creates a new recording. It refuses to overwrite an existing
// path; an accidental second run should not destroy the session it was meant to
// preserve. A .gz suffix selects gzip-compressed JSON Lines.
func NewRecorder(path string) (*Recorder, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("recording path is empty")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create recording %s: %w", path, err)
	}

	out := &syncWriteCloser{writer: f, flush: func() error { return nil }, close: f.Close}
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz := gzip.NewWriter(f)
		out.writer = gz
		out.flush = gz.Flush
		out.close = func() error {
			gzErr := gz.Close()
			fileErr := f.Close()
			return errors.Join(gzErr, fileErr)
		}
	}
	r := &Recorder{path: path, out: out, enc: json.NewEncoder(out.writer), detailHash: map[string][sha256.Size]byte{}}
	if err := r.writeLocked(record{Type: "header", Format: Format, Version: Version, CreatedAt: time.Now()}); err != nil {
		_ = out.close()
		return nil, err
	}
	return r, nil
}

func (r *Recorder) writeLocked(value record) error {
	if r.err != nil {
		return r.err
	}
	if r.closed {
		return fmt.Errorf("recording %s is closed", r.path)
	}
	if err := r.enc.Encode(value); err != nil {
		r.err = fmt.Errorf("write recording %s: %w", r.path, err)
	} else if err := r.out.flush(); err != nil {
		r.err = fmt.Errorf("flush recording %s: %w", r.path, err)
	}
	return r.err
}

// RecordSnapshot writes exactly the snapshot received from the source.
func (r *Recorder) RecordSnapshot(snap *model.Snapshot) error {
	if snap == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeLocked(record{Type: "snapshot", At: snap.Taken, Snapshot: snap})
}

// RecordDetail writes a describe sample only when its content changes. Fetch
// time is excluded from the comparison so periodic polling does not repeat a
// large, unchanged event list every five seconds.
func (r *Recorder) RecordDetail(name, nodeClaim string, detail *model.NodeDetail) error {
	if detail == nil {
		return nil
	}
	at := detail.FetchedAt
	if at.IsZero() {
		at = time.Now()
	}
	identity := detailIdentity(name, nodeClaim, detail.ProviderID)
	comparison := *detail
	comparison.FetchedAt = time.Time{}
	b, err := json.Marshal(&comparison)
	if err != nil {
		return fmt.Errorf("encode detail for %s: %w", name, err)
	}
	hash := sha256.Sum256(b)

	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, ok := r.detailHash[identity]; ok && previous == hash {
		return nil
	}
	if err := r.writeLocked(record{Type: "detail", At: at, Identity: identity, Name: name,
		NodeClaim: nodeClaim, ProviderID: detail.ProviderID, Detail: detail}); err != nil {
		return err
	}
	r.detailHash[identity] = hash
	return nil
}

// WrapSnapshots copies a source channel and records each value before making it
// visible to the UI. Backpressure here is deliberate: the on-disk sequence and
// the sequence the playback engine receives must be identical.
func (r *Recorder) WrapSnapshots(ctx context.Context, in <-chan *model.Snapshot) <-chan *model.Snapshot {
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
				if err := r.RecordSnapshot(snap); err != nil {
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

// Close appends a clean end marker and closes the underlying file.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.err
	}
	writeErr := r.writeLocked(record{Type: "end", EndedAt: time.Now()})
	r.closed = true
	closeErr := r.out.close()
	return errors.Join(writeErr, closeErr)
}

// Error reports a write failure that stopped a recording.
func (r *Recorder) Error() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Describer is the source capability the recording wrapper needs.
type Describer interface {
	DescribeNode(context.Context, string, string) (*model.NodeDetail, error)
}

type recordingDescriber struct {
	recorder *Recorder
	source   Describer
}

// RecordDescriber records successful node-detail reads while preserving the
// source's result and error behavior.
func RecordDescriber(recorder *Recorder, source Describer) Describer {
	if recorder == nil || source == nil {
		return source
	}
	return &recordingDescriber{recorder: recorder, source: source}
}

func (d *recordingDescriber) DescribeNode(ctx context.Context, name, claim string) (*model.NodeDetail, error) {
	detail, err := d.source.DescribeNode(ctx, name, claim)
	if err != nil || detail == nil {
		return detail, err
	}
	if recordErr := d.recorder.RecordDetail(name, claim, detail); recordErr != nil {
		return nil, recordErr
	}
	return detail, nil
}

// Load parses a complete or abruptly terminated recording. A partial final JSON
// value is ignored so a session remains usable after a killed process; malformed
// complete records and unsupported versions are rejected.
func Load(path string) (*Data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recording %s: %w", path, err)
	}
	defer f.Close()

	var reader io.Reader = f
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("open gzip recording %s: %w", path, err)
		}
		defer gz.Close()
		reader = gz
	}

	data := &Data{}
	buffered := bufio.NewReaderSize(reader, 256<<10)
	lineNumber := 0
	header := false
	for {
		line, readErr := buffered.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			var value record
			if err := json.Unmarshal(line, &value); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("parse recording %s line %d: %w", path, lineNumber, err)
			}
			if !header && value.Type != "header" {
				return nil, fmt.Errorf("parse recording %s line %d: first record is not a header", path, lineNumber)
			}
			switch value.Type {
			case "header":
				if header {
					return nil, fmt.Errorf("parse recording %s: duplicate header", path)
				}
				if value.Format != Format || value.Version != Version {
					return nil, fmt.Errorf("unsupported recording format %q version %d", value.Format, value.Version)
				}
				header, data.CreatedAt = true, value.CreatedAt
			case "snapshot":
				if value.Snapshot == nil {
					return nil, fmt.Errorf("parse recording %s line %d: snapshot payload is missing", path, lineNumber)
				}
				data.Snapshots = append(data.Snapshots, value.Snapshot)
			case "detail":
				if value.Detail == nil {
					return nil, fmt.Errorf("parse recording %s line %d: detail payload is missing", path, lineNumber)
				}
				data.Details = append(data.Details, Detail{At: value.At, Identity: value.Identity,
					Name: value.Name, NodeClaim: value.NodeClaim, ProviderID: value.ProviderID, Detail: value.Detail})
			case "end":
				data.EndedAt = value.EndedAt
			default:
				return nil, fmt.Errorf("parse recording %s line %d: unknown record type %q", path, lineNumber, value.Type)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !(errors.Is(readErr, io.ErrUnexpectedEOF) && len(data.Snapshots) > 0) {
				return nil, fmt.Errorf("read recording %s: %w", path, readErr)
			}
			break
		}
	}
	if !header {
		return nil, fmt.Errorf("recording %s has no header", path)
	}
	if len(data.Snapshots) == 0 {
		return nil, fmt.Errorf("recording %s has no snapshots", path)
	}
	return data, nil
}

// Stream emits the already-loaded snapshot sequence, then stays open until the
// viewer exits. A closed source channel means "quit" to the live UI, while the
// end of a recording means "the playhead reached the end".
func Stream(ctx context.Context, snapshots []*model.Snapshot) <-chan *model.Snapshot {
	out := make(chan *model.Snapshot, 1)
	go func() {
		defer close(out)
		for _, snap := range snapshots {
			select {
			case out <- snap:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return out
}

func detailIdentity(name, claim, providerID string) string {
	switch {
	case providerID != "":
		return "provider:" + providerID
	case claim != "":
		return "claim:" + claim
	default:
		return "node:" + name
	}
}
