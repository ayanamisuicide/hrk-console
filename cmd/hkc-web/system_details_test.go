package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessTreeSumsDescendants(t *testing.T) {
	samples := []procSample{
		{PID: 1, PPID: 0, CPUPercent: 50, RSS: 999},
		{PID: 10, PPID: 1, CPUPercent: 2, RSS: 100, Threads: 4},
		{PID: 11, PPID: 10, CPUPercent: 1.5, RSS: 50, Threads: 1},
		{PID: 12, PPID: 10, CPUPercent: 0.5, RSS: 25, Threads: 2},
		{PID: 20, PPID: 1, CPUPercent: 9, RSS: 500},
	}
	tree, ok := processTree(samples, 10)
	if !ok || tree.CPUPercent != 4 || tree.RSS != 175 || tree.Threads != 7 || tree.Children != 2 {
		t.Fatalf("tree: %+v %v", tree, ok)
	}
	if _, ok := processTree(samples, 99); ok {
		t.Fatal("missing root must not be running")
	}
}

func TestNetCounterRates(t *testing.T) {
	var counter netCounter
	now := time.Now()
	if rates := counter.observe(now, 1000, 500); rates.RxRate != 0 {
		t.Fatal("first sample has no rate")
	}
	rates := counter.observe(now.Add(2*time.Second), 3000, 1500)
	if rates.RxRate != 1000 || rates.TxRate != 500 || rates.RxTotal != 3000 {
		t.Fatalf("rates: %+v", rates)
	}
	// Сброс счётчиков интерфейса (перезапуск сети) не даёт отрицательной скорости.
	if rates := counter.observe(now.Add(3*time.Second), 10, 10); rates.RxRate != 0 || rates.TxRate != 0 {
		t.Fatalf("counter reset: %+v", rates)
	}
}

func TestForecastDiskNeedsSteadyGrowth(t *testing.T) {
	const total = 100 << 30
	start := time.Now().Add(-6 * time.Hour)
	points := []hostPoint{}
	for minute := 0; minute <= 360; minute++ {
		// 1 ГБ за 6 часов = 4 ГБ в сутки.
		used := 50<<30 + float64(minute)/360*(1<<30)
		points = append(points, hostPoint{At: start.Add(time.Duration(minute) * time.Minute), Disk: used / total * 100})
	}
	forecast := forecastDisk(points, total, 20<<30)
	if forecast.DaysToFull < 4.9 || forecast.DaysToFull > 5.1 || forecast.Fit < 0.99 {
		t.Fatalf("forecast: %+v", forecast)
	}
	flat := make([]hostPoint, len(points))
	for index := range flat {
		flat[index] = hostPoint{At: points[index].At, Disk: 50}
	}
	if got := forecastDisk(flat, total, 20<<30); got.DaysToFull != 0 {
		t.Fatalf("flat disk must have no forecast: %+v", got)
	}
	if got := forecastDisk(points[:30], total, 20<<30); got.DaysToFull != 0 {
		t.Fatal("less than an hour of data must not forecast")
	}
}

func TestRSSTrendOnlyCurrentProcess(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	points := []hostPoint{}
	for second := 0; second < 3600; second += 2 {
		points = append(points, hostPoint{At: start.Add(time.Duration(second) * time.Second), PID: 7, BotRSS: uint64(100<<20 + second*1000)})
		points = append(points, hostPoint{At: start.Add(time.Duration(second) * time.Second), PID: 8, BotRSS: 1})
	}
	perHour, fit := rssTrend(points, 7)
	if perHour < 3.5e6 || perHour > 3.7e6 || fit < 0.99 {
		t.Fatalf("trend: %v %v", perHour, fit)
	}
	if perHour, _ := rssTrend(points[:100], 7); perHour != 0 {
		t.Fatal("short history must not report a trend")
	}
}

func TestHistoryStatsSkipIdleBot(t *testing.T) {
	points := []hostPoint{{CPU: 10}, {CPU: 20, PID: 3, BotCPU: 4, BotRSS: 100}, {CPU: 90, PID: 3, BotCPU: 6, BotRSS: 300}}
	stats := historyStats(points)
	if stats["cpu"].Max != 90 || stats["cpu"].Min != 10 || stats["cpu"].Avg != 40 {
		t.Fatalf("cpu: %+v", stats["cpu"])
	}
	if stats["botRss"].Avg != 200 || stats["botCpu"].Min != 4 {
		t.Fatalf("bot stats include idle points: %+v %+v", stats["botRss"], stats["botCpu"])
	}
	if _, ok := stats["tgMs"]; ok {
		t.Fatal("series without data must be omitted")
	}
}

func TestEventTimelineKeepsDay(t *testing.T) {
	timeline := &eventTimeline{}
	timeline.add(alertEvent{Kind: "old", Time: time.Now().Add(-25 * time.Hour)})
	timeline.add(alertEvent{Kind: "new", Time: time.Now()})
	events := timeline.since(time.Now().Add(-time.Hour))
	if len(events) != 1 || events[0].Kind != "new" {
		t.Fatalf("events: %+v", events)
	}
	for index := 0; index < timelineLimit+20; index++ {
		timeline.add(alertEvent{Kind: "spam", Time: time.Now()})
	}
	if got := len(timeline.since(time.Time{})); got != timelineLimit {
		t.Fatalf("limit: %d", got)
	}
}

func TestDispatcherRecordsWithoutChannels(t *testing.T) {
	timeline := &eventTimeline{}
	dispatcher := &alertDispatcher{record: timeline.add, settings: func() alertSettings { return alertSettings{} }}
	dispatcher.notify(alertEvent{Kind: "bot.stopped", Title: "x"})
	if events := timeline.since(time.Time{}); len(events) != 1 || events[0].Time.IsZero() {
		t.Fatalf("timeline must record filtered events: %+v", events)
	}
}

func TestBuildSummaryLevels(t *testing.T) {
	status := systemStatus{Supported: true, MemoryTotal: 100, MemoryUsed: 50, DiskTotal: 100, DiskUsed: 10}
	healthy := buildSummary(status, botProcess{Running: true, RSS: 100}, watchdogStatus{State: "healthy"},
		[]probeResult{{OK: true}}, diskForecast{}, nil)
	if healthy.Level != "ok" || healthy.Title != "Всё в порядке" || len(healthy.Items) != 0 {
		t.Fatalf("healthy: %+v", healthy)
	}
	status.DiskUsed = 90
	warn := buildSummary(status, botProcess{Running: true}, watchdogStatus{}, nil, diskForecast{DaysToFull: 2}, nil)
	if warn.Level != "warn" || len(warn.Items) != 2 {
		t.Fatalf("warn: %+v", warn)
	}
	bad := buildSummary(status, botProcess{}, watchdogStatus{State: "suspended"}, []probeResult{{OK: false}}, diskForecast{},
		map[string]time.Duration{"cpu": 10 * time.Minute})
	if bad.Level != "bad" || !strings.Contains(bad.Items[0].Text, "остановлен") {
		t.Fatalf("bad: %+v", bad)
	}
	leak := buildSummary(systemStatus{}, botProcess{Running: true, RSS: 100 << 20, RSSTrendPerHour: 20 << 20, RSSTrendFit: 0.9},
		watchdogStatus{}, nil, diskForecast{}, nil)
	if leak.Level != "warn" || !strings.Contains(leak.Items[0].Text, "утечка") {
		t.Fatalf("leak: %+v", leak)
	}
}

func TestNetworkProberReportsLatencyAndFailures(t *testing.T) {
	prober := networkProber{dial: func(ctx context.Context, address string) error {
		if strings.HasPrefix(address, "api.") {
			return errors.New("refused")
		}
		if strings.HasPrefix(address, "91.") {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	started := time.Now()
	prober.probe(context.Background())
	if time.Since(started) > 6*time.Second {
		t.Fatal("probes must run in parallel")
	}
	results := prober.snapshot()
	if len(results) != len(telegramTargets) {
		t.Fatalf("results: %d", len(results))
	}
	for _, result := range results {
		switch {
		case strings.HasPrefix(result.Address, "api."):
			if result.OK || !strings.Contains(result.Error, "отклонено") {
				t.Fatalf("refused: %+v", result)
			}
		case strings.HasPrefix(result.Address, "91."):
			if result.OK || !strings.Contains(result.Error, "нет ответа") {
				t.Fatalf("timeout: %+v", result)
			}
		default:
			if !result.OK {
				t.Fatalf("ok: %+v", result)
			}
		}
	}
	if prober.best() < 0 {
		t.Fatal("best latency")
	}
}

func TestScanDiskUsageSortsAndAddsPanelData(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(path string, size int) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(root, "venv", "lib", "big.so"), 5000)
	mustWrite(filepath.Join(root, "heroku.log"), 1200)
	mustWrite(filepath.Join(root, "small.txt"), 10)
	panel := t.TempDir()
	mustWrite(filepath.Join(panel, "host-history.jsonl"), 300)
	usage := scanDiskUsage(root, map[string]string{"Данные панели": panel, "Внутри": filepath.Join(root, "venv")})
	if usage.Partial || usage.Total != 6210 || len(usage.Entries) != 4 {
		t.Fatalf("usage: %+v", usage)
	}
	if usage.Entries[0].Name != "venv" || !usage.Entries[0].Dir || usage.Entries[0].Bytes != 5000 {
		t.Fatalf("order: %+v", usage.Entries)
	}
	found := false
	for _, entry := range usage.Entries {
		found = found || (entry.Name == "Данные панели" && entry.Bytes == 300)
		if entry.Name == "Внутри" {
			t.Fatal("paths inside the bot directory must not be counted twice")
		}
	}
	if !found {
		t.Fatalf("panel data missing: %+v", usage.Entries)
	}
}

func TestSystemDetailsHidesProcessesFromViewers(t *testing.T) {
	s := newTestServer(t)
	s.latestLive = liveSample{processes: []procSample{{PID: 1, Name: "secret-service", CPUPercent: 5}}}
	s.diskScanner.result = diskUsage{ScannedAt: time.Now()}
	for _, role := range []string{"viewer", "operator"} {
		token, _, err := s.auth.createInviteWithRole(time.Hour, role)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.auth.register(token, role+"-user", "long-password-123"); err != nil {
			t.Fatal(err)
		}
		session, _, err := s.sessions.create(role + "-user")
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/system/details", nil)
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		response := httptest.NewRecorder()
		s.routes().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", role, response.Code, response.Body.String())
		}
		var body systemDetailsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		visible := len(body.Processes) == 1 && !body.ProcessesHidden
		if visible != (role == "operator") {
			t.Fatalf("%s sees processes=%v hidden=%v", role, len(body.Processes), body.ProcessesHidden)
		}
	}
}
