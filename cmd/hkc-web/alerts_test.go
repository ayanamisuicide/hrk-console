package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWatchdogSuspendsAfterCrashLoop проверяет, что короткие промежутки работы между падениями
// не сбрасывают серию, а лимит останавливает перезапуски до явного возобновления.
func TestWatchdogSuspendsAfterCrashLoop(t *testing.T) {
	settings := watchdogSettings{Enabled: true, TimeoutSeconds: 30, MaxAttempts: 2}
	now := time.Now()
	var monitor watchdogMonitor
	for attempt := 1; attempt <= 2; attempt++ {
		now = now.Add(31 * time.Second)
		monitor.tick(now, settings, 0, false, "stopped")
		now = now.Add(31 * time.Second)
		if !monitor.tick(now, settings, 0, false, "stopped") {
			t.Fatalf("attempt %d must restart", attempt)
		}
		monitor.complete(now, actionResponse{OK: true, Message: "started"})
		// Бот отвечает минуту и снова падает: этого мало для стабильной работы.
		now = now.Add(time.Minute)
		monitor.tick(now, settings, 100+attempt, true, "")
	}
	now = now.Add(time.Second)
	if monitor.tick(now, settings, 0, false, "stopped") || monitor.snapshot().State != "suspended" {
		t.Fatalf("crash loop must suspend: %+v", monitor.snapshot())
	}
	if monitor.tick(now.Add(time.Hour), settings, 0, false, "stopped") {
		t.Fatal("suspended watchdog restarted bot")
	}
	monitor.resume()
	if monitor.tick(now.Add(time.Hour+time.Second), settings, 0, false, "stopped") {
		t.Fatal("resume must give a full timeout")
	}
	if !monitor.tick(now.Add(time.Hour+32*time.Second), settings, 0, false, "stopped") {
		t.Fatal("resumed watchdog must restart after timeout")
	}
}

func TestWatchdogStableHealthResetsStreak(t *testing.T) {
	settings := watchdogSettings{Enabled: true, TimeoutSeconds: 30, MaxAttempts: 1}
	now := time.Now()
	var monitor watchdogMonitor
	monitor.tick(now, settings, 0, false, "stopped")
	monitor.tick(now.Add(31*time.Second), settings, 0, false, "stopped")
	monitor.complete(now.Add(31*time.Second), actionResponse{OK: true})
	monitor.tick(now.Add(time.Minute), settings, 7, true, "")
	monitor.tick(now.Add(time.Minute+watchdogStableAfter), settings, 7, true, "")
	if got := monitor.snapshot().Streak; got != 0 {
		t.Fatalf("stable health must reset streak, got %d", got)
	}
	monitor.tick(now.Add(time.Hour), settings, 0, false, "stopped")
	if monitor.snapshot().State == "suspended" {
		t.Fatal("reset streak still suspended")
	}
}

func TestWatchdogHTTPPartialUpdateKeepsLimitAndResumes(t *testing.T) {
	s := newTestServer(t)
	var err error
	s.operations, err = openOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.setWatchdog(response, adminRequest(http.MethodPut, "/api/admin/watchdog", strings.NewReader(`{"enabled":true,"timeoutSeconds":120,"maxAttempts":3}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	s.setWatchdog(response, adminRequest(http.MethodPut, "/api/admin/watchdog", strings.NewReader(`{"enabled":true,"timeoutSeconds":200}`)))
	if got := s.operations.watchdogSettings(); response.Code != http.StatusOK || got.MaxAttempts != 3 || got.TimeoutSeconds != 200 {
		t.Fatalf("partial update: %d %+v", response.Code, got)
	}
	for _, body := range []string{`{"maxAttempts":0}`, `{"maxAttempts":21}`} {
		response = httptest.NewRecorder()
		s.setWatchdog(response, adminRequest(http.MethodPut, "/api/admin/watchdog", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s", body)
		}
	}
	s.watchdog.status.Streak = 3
	response = httptest.NewRecorder()
	s.resumeWatchdog(response, adminRequest(http.MethodPost, "/api/admin/watchdog/resume", nil))
	if response.Code != http.StatusOK || s.watchdog.snapshot().Streak != 0 {
		t.Fatalf("resume: %d streak=%d", response.Code, s.watchdog.snapshot().Streak)
	}
}

func TestAlertSettingsPersistWithWatchdog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.alertSettings() != defaultAlertSettings() {
		t.Fatalf("defaults: %+v", store.alertSettings())
	}
	alerts := alertSettings{BotState: false, Watchdog: true, Modules: false, CPUPercent: 0, MemoryPercent: 80, DiskPercent: 95, SustainSeconds: 60}
	if err := store.setAlerts(alerts); err != nil {
		t.Fatal(err)
	}
	watchdog := watchdogSettings{Enabled: true, TimeoutSeconds: 90, MaxAttempts: 4}
	if err := store.setWatchdog(watchdog); err != nil {
		t.Fatal(err)
	}
	reopened, err := openOperationStore(path)
	if err != nil || reopened.alertSettings() != alerts || reopened.watchdogSettings() != watchdog {
		t.Fatalf("reopen: %v %+v %+v", err, reopened.alertSettings(), reopened.watchdogSettings())
	}
	for _, bad := range []alertSettings{{CPUPercent: 10, SustainSeconds: 60}, {CPUPercent: 90, SustainSeconds: 5}, {DiskPercent: 101, SustainSeconds: 60}} {
		if store.setAlerts(bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

// recordingSink запоминает доставленные события для проверки диспетчера.
type recordingSink struct {
	mu     sync.Mutex
	events []alertEvent
}

func (sink *recordingSink) name() string { return "telegram" }

func (sink *recordingSink) send(_ context.Context, event alertEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, event)
	return nil
}

func TestTelegramSinkSendsEscapedHTMLAndHidesToken(t *testing.T) {
	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123"
	var got map[string]any
	var path string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["text"] == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(got["text"].(string), "fail") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Forbidden: bot was blocked by the user"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer api.Close()
	env := map[string]string{"HKC_TELEGRAM_BOT_TOKEN": token, "HKC_TELEGRAM_CHAT_ID": "-1001234", "HKC_TELEGRAM_API_URL": api.URL}
	dispatcher := newAlertDispatcher(func(key string) string { return env[key] }, nil)
	if !dispatcher.channels()["telegram"] || dispatcher.channels()["webhook"] {
		t.Fatalf("channels: %+v", dispatcher.channels())
	}
	event := dispatcher.prepare(alertEvent{Kind: "test", Severity: "critical", Title: "<b>x</b>", Message: "a & b"})
	if err := dispatcher.deliver(t.Context(), event)["telegram"]; err != nil {
		t.Fatal(err)
	}
	if path != "/bot"+token+"/sendMessage" || got["chat_id"] != "-1001234" || got["parse_mode"] != "HTML" {
		t.Fatalf("request: %s %+v", path, got)
	}
	text := got["text"].(string)
	if !strings.HasPrefix(text, "🔴 <b>&lt;b&gt;x&lt;/b&gt;</b>") || !strings.Contains(text, "a &amp; b") {
		t.Fatalf("text: %q", text)
	}
	err := dispatcher.deliver(t.Context(), dispatcher.prepare(alertEvent{Title: "fail"}))["telegram"]
	if err == nil || !strings.Contains(err.Error(), "blocked") || strings.Contains(err.Error(), token) {
		t.Fatalf("error: %v", err)
	}
	api.Close()
	err = dispatcher.deliver(t.Context(), event)["telegram"]
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("network error leaks token: %v", err)
	}
}

func TestAlertDispatcherRejectsInvalidChannels(t *testing.T) {
	for _, env := range []map[string]string{
		{"HKC_TELEGRAM_BOT_TOKEN": "nope", "HKC_TELEGRAM_CHAT_ID": "1"},
		{"HKC_TELEGRAM_BOT_TOKEN": "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123", "HKC_TELEGRAM_CHAT_ID": "chat"},
		{"HKC_WEBHOOK_URL": "http://example.com/hook"},
		{"HKC_WEBHOOK_URL": "https://user:pass@example.com/hook"},
	} {
		if newAlertDispatcher(func(key string) string { return env[key] }, nil).active() {
			t.Fatalf("accepted %+v", env)
		}
	}
}

func TestWebhookKeepsLegacyStatusField(t *testing.T) {
	payloads := make(chan map[string]any, 2)
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		payloads <- payload
	}))
	defer hook.Close()
	sink := &webhookSink{endpoint: hook.URL, client: hook.Client()}
	if err := sink.send(t.Context(), alertEvent{Kind: "bot.stopped", Title: "Бот остановлен"}); err != nil {
		t.Fatal(err)
	}
	if got := <-payloads; got["service"] != "Heroku bot" || got["status"] != "stopped" || got["event"] != "bot.stopped" {
		t.Fatalf("payload: %+v", got)
	}
	if err := sink.send(t.Context(), alertEvent{Kind: "host.disk"}); err != nil {
		t.Fatal(err)
	}
	if got := <-payloads; got["status"] != nil {
		t.Fatalf("non-state event must not carry status: %+v", got)
	}
}

func TestAlertDispatcherFiltersAndRateLimits(t *testing.T) {
	sink := &recordingSink{}
	settings := defaultAlertSettings()
	settings.BotState = false
	dispatcher := &alertDispatcher{sinks: []alertSink{sink}, settings: func() alertSettings { return settings }}
	if dispatcher.settings().allows("bot.stopped") || !dispatcher.settings().allows("watchdog.failed") || !dispatcher.settings().allows("test") {
		t.Fatal("filter")
	}
	now := time.Now()
	for index := 0; index < alertRateLimit; index++ {
		if !dispatcher.allowRate(now) {
			t.Fatalf("limit reached early at %d", index)
		}
	}
	if dispatcher.allowRate(now) {
		t.Fatal("rate limit not applied")
	}
	if !dispatcher.allowRate(now.Add(alertRateWindow + time.Second)) {
		t.Fatal("window must slide")
	}
}

func TestHostAlertMonitorSustainAndHysteresis(t *testing.T) {
	settings := alertSettings{CPUPercent: 90, MemoryPercent: 0, DiskPercent: 90, SustainSeconds: 60}
	var monitor hostAlertMonitor
	now := time.Now()
	observe := func(offset time.Duration, cpu float64) []alertEvent {
		return monitor.observe(now.Add(offset), settings, map[string]float64{"cpu": cpu, "memory": 99, "disk": 10})
	}
	if events := observe(0, 95); len(events) != 0 {
		t.Fatalf("fired immediately: %+v", events)
	}
	if events := observe(59*time.Second, 92); len(events) != 0 {
		t.Fatal("fired before sustain")
	}
	events := observe(60*time.Second, 91)
	if len(events) != 1 || events[0].Kind != "host.cpu" || events[0].Severity != "critical" {
		t.Fatalf("expected cpu alert: %+v", events)
	}
	if events := observe(61*time.Second, 99); len(events) != 0 {
		t.Fatal("repeated alert")
	}
	if events := observe(62*time.Second, 87); len(events) != 0 {
		t.Fatal("hysteresis ignored")
	}
	events = observe(63*time.Second, 80)
	if len(events) != 1 || events[0].Severity != "ok" {
		t.Fatalf("expected recovery: %+v", events)
	}
}

func TestModuleAlertMonitorBaselineAndNewErrors(t *testing.T) {
	var monitor moduleAlertMonitor
	snapshot := modulesSnapshot{Status: "live", PID: 10, Session: "a", Modules: []moduleEntry{
		{ID: "old", Name: "Old", State: "error", Error: "boom"},
		{ID: "ok", Name: "Fine", State: "ready"},
	}}
	if events := monitor.observe(snapshot); len(events) != 0 {
		t.Fatal("baseline must be silent")
	}
	snapshot.Modules[1] = moduleEntry{ID: "ok", Name: "Fine", State: "error", Error: "ValueError: bad\ntraceback"}
	events := monitor.observe(snapshot)
	if len(events) != 1 || events[0].Title != "Ошибка модуля Fine" || !strings.Contains(events[0].Message, "ValueError: bad") || strings.Contains(events[0].Message, "traceback") {
		t.Fatalf("events: %+v", events)
	}
	if events := monitor.observe(snapshot); len(events) != 0 {
		t.Fatal("duplicate alert")
	}
	snapshot.Session = "b"
	if events := monitor.observe(snapshot); len(events) != 1 || !strings.Contains(events[0].Title, "2") {
		t.Fatalf("new session must report again: %+v", events)
	}
	snapshot.Status = "stale"
	if events := monitor.observe(snapshot); events != nil {
		t.Fatal("stale snapshot used")
	}
}

func TestAlertsHTTP(t *testing.T) {
	s := newTestServer(t)
	var err error
	s.operations, err = openOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.alerts = &alertDispatcher{settings: s.operations.alertSettings}
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/alerts", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", response.Code)
	}
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, adminRequest(http.MethodPost, "/api/admin/alerts/test", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("test without channels: %d", response.Code)
	}
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, adminRequest(http.MethodPut, "/api/admin/alerts", strings.NewReader(`{"diskPercent":85,"botState":false}`)))
	if got := s.operations.alertSettings(); response.Code != http.StatusOK || got.DiskPercent != 85 || got.BotState || got.CPUPercent != 90 {
		t.Fatalf("update: %d %+v", response.Code, got)
	}
	for _, body := range []string{`{"diskPercent":20}`, `{"token":"x"}`} {
		response = httptest.NewRecorder()
		s.routes().ServeHTTP(response, adminRequest(http.MethodPut, "/api/admin/alerts", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d", body, response.Code)
		}
	}
	sink := &recordingSink{}
	s.alerts.sinks = []alertSink{sink}
	response = httptest.NewRecorder()
	s.routes().ServeHTTP(response, adminRequest(http.MethodPost, "/api/admin/alerts/test", nil))
	if response.Code != http.StatusOK || len(sink.events) != 1 || sink.events[0].Kind != "test" {
		t.Fatalf("test alert: %d %s", response.Code, response.Body.String())
	}
}
