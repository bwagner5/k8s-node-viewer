package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

func playbackSnapshot(gen uint64, at time.Time) *model.Snapshot {
	return &model.Snapshot{Generation: gen, Taken: at}
}

func TestPlaybackHalfSpeedThenOneSpeedStaysBehind(t *testing.T) {
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	p := newPlayback(1, time.Hour, 1<<30)
	if err := p.SetSpeed(.5, start); err != nil {
		t.Fatal(err)
	}
	if got := p.Ingest(playbackSnapshot(1, start), start); len(got) != 1 {
		t.Fatalf("first snapshot should paint immediately, got %d", len(got))
	}
	p.Ingest(playbackSnapshot(2, start.Add(2*time.Second)), start.Add(2*time.Second))
	if got := p.Advance(2*time.Second, start.Add(2*time.Second)); len(got) != 0 {
		t.Fatalf("half speed released snapshot after 2s: %v", got)
	}
	if got := p.Advance(2*time.Second, start.Add(4*time.Second)); len(got) != 1 || got[0].Generation != 2 {
		t.Fatalf("half speed did not release snapshot after 4s: %v", got)
	}

	if err := p.SetSpeed(1, start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	p.Ingest(playbackSnapshot(3, start.Add(6*time.Second)), start.Add(6*time.Second))
	if got := p.Advance(3*time.Second, start.Add(7*time.Second)); len(got) != 0 {
		t.Fatalf("delayed 1x jumped ahead: %v", got)
	}
	if got := p.Advance(time.Second, start.Add(8*time.Second)); len(got) != 1 || got[0].Generation != 3 {
		t.Fatalf("delayed 1x did not preserve cadence: %v", got)
	}
}

func TestPlaybackPauseResumeAndRealtime(t *testing.T) {
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	p := newPlayback(1, time.Hour, 1<<30)
	p.Ingest(playbackSnapshot(1, start), start)
	p.TogglePause(start)
	p.Ingest(playbackSnapshot(2, start.Add(time.Second)), start.Add(time.Second))
	if got := p.Advance(10*time.Second, start.Add(10*time.Second)); len(got) != 0 {
		t.Fatalf("paused playback advanced: %v", got)
	}
	p.TogglePause(start.Add(10 * time.Second))
	if got := p.Advance(time.Second, start.Add(11*time.Second)); len(got) != 1 || got[0].Generation != 2 {
		t.Fatalf("resume did not continue from paused frame: %v", got)
	}

	p.Ingest(playbackSnapshot(3, start.Add(12*time.Second)), start.Add(12*time.Second))
	if got := p.GoLive(start.Add(12 * time.Second)); got == nil || got.Generation != 3 {
		t.Fatalf("realtime did not return freshest snapshot: %#v", got)
	}
	if !p.live || len(p.queue) != 0 || len(p.history) != 1 || p.bytes == 0 {
		t.Fatalf("realtime did not clear the backlog and seed a new anchor: live=%v queue=%d history=%d bytes=%d",
			p.live, len(p.queue), len(p.history), p.bytes)
	}
	if p.history[0].Generation != 3 || p.history[0].Taken != start.Add(12*time.Second) {
		t.Fatalf("realtime anchor = %#v, want generation 3 at realtime boundary", p.history[0])
	}

	// No source snapshot arrives during these five idle seconds. The realtime
	// anchor alone must still make that elapsed interval rewindable.
	snap, moved := p.Rewind(5*time.Second, start.Add(17*time.Second))
	if snap == nil || snap.Generation != 3 || moved != 5*time.Second {
		t.Fatalf("idle rewind after realtime = snapshot %#v moved %s; want generation 3 moved 5s", snap, moved)
	}
}

func TestPlaybackKeepsRollingHistoryForPauseThenRewind(t *testing.T) {
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	p := newPlayback(1, time.Hour, 1<<30)
	for i, offset := range []time.Duration{0, 5 * time.Second, 10 * time.Second} {
		at := start.Add(offset)
		p.Ingest(playbackSnapshot(uint64(i+1), at), at)
	}

	p.TogglePause(start.Add(10 * time.Second))
	snap, moved := p.Rewind(6*time.Second, start.Add(10*time.Second))
	if snap == nil || snap.Generation != 1 || moved != 6*time.Second {
		t.Fatalf("rewind = snapshot %#v, moved %s; want generation 1 moved 6s", snap, moved)
	}
	if p.live || p.speed != 0 || len(p.queue) != 2 {
		t.Fatalf("rewind did not preserve pause and rebuild queue: live=%v speed=%v queue=%d",
			p.live, p.speed, len(p.queue))
	}
	if got := p.Advance(time.Second, start.Add(11*time.Second)); len(got) != 0 {
		t.Fatalf("paused rewind advanced: %v", got)
	}
	p.TogglePause(start.Add(11 * time.Second))
	if got := p.Advance(time.Second, start.Add(12*time.Second)); len(got) != 1 || got[0].Generation != 2 {
		t.Fatalf("replay did not release the next historical snapshot: %v", got)
	}
}

func TestPlaybackHalfSpeedAfterRewindUsesTheSeekPointAsItsEpoch(t *testing.T) {
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	p := newPlayback(1, time.Hour, 1<<30)
	for i, offset := range []time.Duration{0, 5 * time.Second, 10 * time.Second} {
		at := start.Add(offset)
		p.Ingest(playbackSnapshot(uint64(i+1), at), at)
	}
	p.TogglePause(start.Add(10 * time.Second))
	if _, moved := p.Rewind(6*time.Second, start.Add(10*time.Second)); moved != 6*time.Second {
		t.Fatalf("rewound %s, want 6s", moved)
	}
	if err := p.SetSpeed(.5, start.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}

	// Two wall-clock seconds at 0.5x move exactly one timeline second, from the
	// seek point at 12:00:04 to the snapshot at 12:00:05.
	if got := p.Advance(2*time.Second, start.Add(12*time.Second)); len(got) != 1 || got[0].Generation != 2 {
		t.Fatalf("first half-speed step released %v, want generation 2", snapshotGenerations(got))
	}
	if want := start.Add(5 * time.Second); p.now != want {
		t.Fatalf("timeline after 2s at half speed = %s, want %s", p.now, want)
	}
	if got := p.Behind(start.Add(12 * time.Second)); got != 7*time.Second {
		t.Fatalf("lag after first step = %s, want 7s", got)
	}

	if got := p.Advance(10*time.Second, start.Add(22*time.Second)); len(got) != 1 || got[0].Generation != 3 {
		t.Fatalf("second half-speed step released %v, want generation 3", snapshotGenerations(got))
	}
	if want := start.Add(10 * time.Second); p.now != want {
		t.Fatalf("timeline after another 10s = %s, want %s", p.now, want)
	}
	if got := p.Behind(start.Add(22 * time.Second)); got != 12*time.Second {
		t.Fatalf("lag after second step = %s, want 12s", got)
	}
}

func TestPlaybackStatusUnifiesRateTimestampAndPreciseLag(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 5, 0, 0, time.Local)
	p := newPlayback(1, time.Hour, 1<<30)
	p.live = false
	p.speed = .5
	p.now = now.Add(-2*time.Minute - 13*time.Second)
	got := playbackStatus(p, now)
	for _, want := range []string{"0.5x", "at 12:02:47", "2m13s behind"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q is missing %q", got, want)
		}
	}
}

func TestPlaybackLiveHistoryIsBoundedButKeepsACutoffAnchor(t *testing.T) {
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	p := newPlayback(1, time.Hour, 1<<30)
	for i, offset := range []time.Duration{0, 5 * time.Second, 35 * time.Second, 40 * time.Second} {
		at := start.Add(offset)
		p.Ingest(playbackSnapshot(uint64(i+1), at), at)
	}
	if len(p.history) != 3 || p.history[0].Generation != 2 {
		t.Fatalf("rolling history generations = %v; want [2 3 4]", snapshotGenerations(p.history))
	}
	snap, moved := p.Rewind(30*time.Second, start.Add(40*time.Second))
	if snap == nil || snap.Generation != 2 || moved != 30*time.Second {
		t.Fatalf("oldest rewind = snapshot %#v moved %s; want generation 2 moved 30s", snap, moved)
	}
}

func snapshotGenerations(snaps []*model.Snapshot) []uint64 {
	out := make([]uint64, len(snaps))
	for i, snap := range snaps {
		out[i] = snap.Generation
	}
	return out
}

func TestPlaybackHistoryLimit(t *testing.T) {
	start := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	p := newPlayback(0, 10*time.Minute, 1<<30)
	p.Ingest(playbackSnapshot(1, start), start)
	if p.OverLimit(start.Add(10 * time.Minute)) {
		t.Fatal("limit should be inclusive")
	}
	if !p.OverLimit(start.Add(10*time.Minute + time.Nanosecond)) {
		t.Fatal("paused playback did not exceed history limit")
	}
}

func TestPlaybackRejectsUnsupportedSpeeds(t *testing.T) {
	p := newPlayback(1, time.Minute, 1<<20)
	for _, speed := range []float64{-0.1, 1.1} {
		if err := p.SetSpeed(speed, time.Now()); err == nil {
			t.Errorf("SetSpeed(%v) succeeded", speed)
		}
	}
}
