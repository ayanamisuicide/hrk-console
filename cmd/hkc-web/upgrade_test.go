package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// TestHostHistoryBucketsKeepPeaksAndAverages проверяет сжатие графика: интервалы усредняются,
// пик CPU сохраняется, а число точек не превышает лимит.
func TestHostHistoryBucketsKeepPeaksAndAverages(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	points := make([]hostPoint, 5000)
	for i := range points {
		points[i] = hostPoint{At: start.Add(time.Duration(i) * time.Second), CPU: 10, Memory: 40, BotRSS: 1000, PID: 1}
	}
	points[2500].CPU = 95
	result := bucketHostPoints(points, 1200)
	if len(result) == 0 || len(result) > 1200 {
		t.Fatalf("unexpected sample size: %d", len(result))
	}
	peak := 0.0
	for _, point := range result {
		peak = max(peak, point.CPUMax)
		if point.Memory != 40 || point.BotRSS != 1000 {
			t.Fatalf("average lost: %+v", point)
		}
	}
	if peak != 95 {
		t.Fatalf("peak lost: %v", peak)
	}
	if !result[len(result)-1].At.Equal(points[len(points)-1].At) {
		t.Fatal("last bucket must end at the newest point")
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
