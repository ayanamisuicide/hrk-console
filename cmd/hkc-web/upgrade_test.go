package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestHostHistoryPersistsAndFilters проверяет сохранение истории и фильтрацию по времени.
func TestHostHistoryPersistsAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-history.jsonl")
	history := newHostHistoryStore(path)
	now := time.Now()
	if err := history.add(hostPoint{At: now.Add(-2 * time.Hour), CPU: 5, PID: 10}); err != nil {
		t.Fatal(err)
	}
	if err := history.add(hostPoint{At: now, CPU: 30, PID: 11}); err != nil {
		t.Fatal(err)
	}
	reopened := newHostHistoryStore(path)
	if points := reopened.since(now.Add(-time.Hour)); len(points) != 1 || points[0].PID != 11 {
		t.Fatalf("unexpected points: %+v", points)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("history permissions: %v %v", info, err)
	}
}

// TestHostHistoryMigratesLegacyAndPreservesRestartMarkers проверяет чтение старого формата и границ смены
// PID.
func TestHostHistoryMigratesLegacyAndPreservesRestartMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host-history.jsonl")
	now := time.Now().UTC().Truncate(time.Second)
	legacy := []hostPoint{{At: now.Add(-time.Minute), CPU: 10, PID: 100}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "host-history.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	history := newHostHistoryStore(path)
	if err := history.add(hostPoint{At: now, CPU: 20, PID: 200}); err != nil {
		t.Fatal(err)
	}
	reopened := newHostHistoryStore(path)
	points, count := reopened.sampledSince(now.Add(-time.Hour), 1200)
	if count != 2 || len(points) != 2 || points[0].PID != 100 || points[1].PID != 200 {
		t.Fatalf("migration lost points: %+v", points)
	}
	if times := reopened.restartTimes(now.Add(-time.Hour)); len(times) != 1 {
		t.Fatalf("restart markers: %+v", times)
	}
}

// TestHostHistoryDownsamplesWithoutLosingRestart проверяет сокращение графика с сохранением перезапуска.
func TestHostHistoryDownsamplesWithoutLosingRestart(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	points := make([]hostPoint, 5000)
	for i := range points {
		points[i] = hostPoint{At: start.Add(time.Duration(i) * time.Second), CPU: float64(i % 100), PID: 1}
	}
	points[2500].PID = 2
	points[2501].PID = 2
	result := downsampleHostPoints(points, 1200)
	if len(result) < 1200 || len(result) > 1204 {
		t.Fatalf("unexpected sample size: %d", len(result))
	}
	found := false
	for _, point := range result {
		if point.PID == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("restart was lost during downsampling")
	}
}

// TestDetectIncidentsGroupsErrorsAndWarningBursts проверяет группировку ошибок и серий предупреждений.
func TestDetectIncidentsGroupsErrorsAndWarningBursts(t *testing.T) {
	lines := []string{
		"2026-10-03 10:00:00 [INFO] Core: starting",
		"2026-10-03 10:00:01 [ERROR] Core: first failure",
		"2026-10-03 10:00:02 [ERROR] Core: second failure",
		"2026-10-03 10:10:01 [WARNING] Net: retry 1",
		"2026-10-03 10:10:02 [WARNING] Net: retry 2",
		"2026-10-03 10:10:03 [WARNING] Net: retry 3",
	}
	got := detectIncidents(lines)
	if len(got) != 2 || got[0].Module != "Net" || got[0].Count != 3 || got[1].Count != 2 || got[1].Context == "" {
		t.Fatalf("incidents: %+v", got)
	}
}

// TestTerminalRequiresExplicitConfirmation проверяет отказ команды без отдельного подтверждения.
func TestTerminalRequiresExplicitConfirmation(t *testing.T) {
	t.Setenv("HKC_TERMINAL_ENABLED", "1")
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/terminal", strings.NewReader(`{"command":"pwd"}`))
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.adminTerminal(w, r)
	if w.Code != http.StatusPreconditionRequired && w.Code != http.StatusNotImplemented {
		t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
	}
}
