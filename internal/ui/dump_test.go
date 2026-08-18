package ui

import (
	"os"
	"testing"
	"time"
)

// TestDumpFrames writes plain-text renders to KNV_DUMP for eyeballing layout.
// Skipped unless the variable is set.
func TestDumpFrames(t *testing.T) {
	path := os.Getenv("KNV_DUMP")
	if path == "" {
		t.Skip("set KNV_DUMP=/path to write sample frames")
	}
	var out []byte
	for _, tc := range []struct {
		name  string
		mode  Mode
		w, h  int
		nodes int
		zoom  int
	}{
		{"pods 160x44 x14", ModePods, 160, 44, 14, 0},
		{"pods 120x36 x8", ModePods, 120, 36, 8, 0},
		{"nodes 160x44 x24", ModeNodes, 160, 44, 24, 0},
		{"dense 160x30 x20", ModeDense, 160, 30, 20, 0},
		{"pods 100x30 x6", ModePods, 100, 30, 6, 0},
		{"pods 250x70 x3 (wide screen, few nodes)", ModePods, 250, 70, 3, 0},
		{"pods 160x44 x40 zoom -2 (whole fleet)", ModePods, 160, 44, 40, -2},
		{"pods 160x44 x40 zoom fit", ModePods, 160, 44, 40, 0},
		{"pods 160x44 x40 zoom +2", ModePods, 160, 44, 40, 2},
		{"pods 160x44 x40 zoom max (one node)", ModePods, 160, 44, 40, zoomMax},
		{"nodes 160x44 x40 zoom +3", ModeNodes, 160, 44, 40, 3},
	} {
		m := newTestModel(t, tc.w, tc.h, tc.nodes)
		m.setMode(tc.mode)
		m.setZoom(tc.zoom)
		for i := 0; i < 60; i++ {
			m.reg.Advance(25 * time.Millisecond)
		}
		out = append(out, ("\n===== " + tc.name + " =====\n")...)
		out = append(out, m.View()...)
		out = append(out, '\n')
	}

	m := newTestModel(t, 160, 44, 14)
	m.bar.pick(mustCmd(t, "nodepool"))
	m.bar.refresh(m)
	m.derive()
	out = append(out, "\n===== command bar: :nodepool picker =====\n"...)
	out = append(out, m.View()...)

	m2 := newTestModel(t, 160, 44, 14)
	m2.showHelp = true
	out = append(out, "\n===== help =====\n"...)
	out = append(out, m2.View()...)

	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustCmd(t *testing.T, name string) *command {
	t.Helper()
	c, ok := lookup(name)
	if !ok {
		t.Fatalf("no command %q", name)
	}
	return c
}
