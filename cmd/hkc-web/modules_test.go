package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModulesSnapshotLifecycle(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	snapshot := modulesSnapshot{Schema: 1, PID: 42, Session: "run", SampledAt: float64(now.UnixMilli()) / 1000, Modules: []moduleEntry{{ID: "core", State: "ready"}}}
	data, _ := json.Marshal(snapshot)
	if err := os.WriteFile(filepath.Join(dir, ".hkc-modules.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		pid    int
		at     time.Time
		status string
		rows   int
	}{
		{"live", 42, now, "live", 1},
		{"stale", 42, now.Add(6 * time.Second), "stale", 1},
		{"restart", 43, now, "waiting", 0},
		{"stopped", 0, now, "stopped", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readModulesSnapshot(dir, tc.pid, tc.at)
			if got.Status != tc.status || len(got.Modules) != tc.rows {
				t.Fatalf("got %+v", got)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(dir, ".hkc-modules.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readModulesSnapshot(dir, 42, now); got.Status != "unavailable" || len(got.Modules) != 0 {
		t.Fatalf("broken snapshot: %+v", got)
	}
}

func TestModulesSnapshotDetectsBlockedMainLoop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	snapshot := modulesSnapshot{Schema: 1, PID: 42, SampledAt: float64(now.UnixMilli()) / 1000, LoopAt: float64(now.Add(-time.Minute).UnixMilli()) / 1000, Modules: []moduleEntry{{ID: "core", State: "ready"}}}
	data, _ := json.Marshal(snapshot)
	if err := os.WriteFile(filepath.Join(dir, ".hkc-modules.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := readModulesSnapshot(dir, 42, now); got.Status != "stale" || len(got.Modules) != 1 {
		t.Fatalf("hung loop: %+v", got)
	}
}
