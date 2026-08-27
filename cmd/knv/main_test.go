package main

import (
	"testing"
	"time"
)

func validFlags() flags {
	return flags{mode: "pods", sortKey: "name", playbackSpeed: 1,
		historyDuration: time.Minute, historyMemoryMiB: 1}
}

func TestBuildConfigRejectsRecordingAReplay(t *testing.T) {
	f := validFlags()
	f.recordFile, f.replayFile = "out.knv", "in.knv"
	if _, err := buildConfig(&f); err == nil {
		t.Fatal("--record and --replay were accepted together")
	}
}

func TestBuildConfigAllowsFastArchivedPlaybackOnly(t *testing.T) {
	f := validFlags()
	f.playbackSpeed = 4
	if _, err := buildConfig(&f); err == nil {
		t.Fatal("4x live playback was accepted")
	}
	f.replayFile = "in.knv"
	if _, err := buildConfig(&f); err != nil {
		t.Fatalf("4x replay was rejected: %v", err)
	}
}
