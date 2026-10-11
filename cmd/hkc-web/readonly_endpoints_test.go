package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoopbackAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true, "[::1]:8080": true, "localhost:80": true,
		"0.0.0.0:8080": false, "192.168.1.5:80": false, ":8080": false, "no-port": false, "example.com:443": false,
	} {
		if got := loopbackAddress(addr); got != want {
			t.Errorf("loopbackAddress(%q) = %v, ждали %v", addr, got, want)
		}
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("HKC_TEST_ENV_OR", "  значение  ")
	if got := envOr("HKC_TEST_ENV_OR", "запас"); got != "значение" {
		t.Fatalf("задано: %q", got)
	}
	t.Setenv("HKC_TEST_ENV_OR", "   ")
	if got := envOr("HKC_TEST_ENV_OR", "запас"); got != "запас" {
		t.Fatalf("пусто: %q", got)
	}
}

// Страницы только для чтения отвечают вошедшему пользователю и закрыты для остальных.
func TestReadOnlyEndpointsRequireSessionAndAnswer(t *testing.T) {
	f := newFlowServer(t)
	f.hostHistory = nil
	cookie := f.login(t, "viewer")
	for _, path := range []string{"/api/incidents", "/api/modules", "/api/system", "/api/system/details", "/api/insights", "/api/metrics"} {
		if anonymous := f.do(t, flowRequest{method: "GET", path: path}); anonymous.Code != http.StatusUnauthorized {
			t.Errorf("%s без входа: %d", path, anonymous.Code)
		}
		response := f.do(t, flowRequest{method: "GET", path: path, cookie: cookie})
		if response.Code != http.StatusOK {
			t.Errorf("%s: код %d: %s", path, response.Code, response.Body.String())
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(response.Body.String()), "{") {
			t.Errorf("%s: ответ не объект JSON: %q", path, response.Body.String())
		}
	}
}

// Диспетчер: событие доходит до приёмника, подавленный настройками тип — нет, а лента получает всё.
func TestAlertDispatcherDeliversFiltersAndRecords(t *testing.T) {
	sink := &recordingSink{}
	settings := defaultAlertSettings()
	settings.BotState = false
	var recorded []alertEvent
	dispatcher := &alertDispatcher{
		sinks:    []alertSink{sink},
		settings: func() alertSettings { return settings },
		record:   func(e alertEvent) { recorded = append(recorded, e) },
		host:     "panel-1",
	}
	dispatcher.notify(alertEvent{Kind: "bot.stopped", Severity: "critical", Title: "x"})
	dispatcher.notify(alertEvent{Kind: "watchdog.failed", Severity: "warning", Title: "y"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		count := len(sink.events)
		sink.mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("доставлено %d событий, ждали 1", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	sink.mu.Lock()
	got := sink.events[0]
	sink.mu.Unlock()
	if got.Kind != "watchdog.failed" || got.Host != "panel-1" || got.Time.IsZero() {
		t.Fatalf("доставленное событие: %+v", got)
	}
	if len(recorded) != 2 {
		t.Fatalf("в ленту должны попасть оба события: %+v", recorded)
	}
	var none *alertDispatcher
	none.notify(alertEvent{Kind: "x"}) // nil-диспетчер не падает
}

// Сборщик истории передаёт замеры хоста в тревоги; без поддержки платформы ничего не делает.
func TestObserveHostAlertsSkipsUnsupported(t *testing.T) {
	f := newFlowServer(t)
	var recorded []alertEvent
	f.alerts = &alertDispatcher{record: func(e alertEvent) { recorded = append(recorded, e) }}
	var err error
	f.operations, err = openOperationStore(f.dataDir + "/operations.json")
	if err != nil {
		t.Fatal(err)
	}
	f.observeHostAlerts(time.Now(), systemStatus{Supported: false, CPUPercent: 100})
	if len(recorded) != 0 {
		t.Fatalf("тревога при неподдерживаемой платформе: %+v", recorded)
	}
	f.observeHostAlerts(time.Now(), systemStatus{Supported: true, CPUPercent: 1, MemoryTotal: 100, MemoryUsed: 1, DiskTotal: 100, DiskUsed: 1})
	if len(recorded) != 0 {
		t.Fatalf("тревога при спокойном хосте: %+v", recorded)
	}
}

func TestTailTextJoinsLastLines(t *testing.T) {
	path := t.TempDir() + "/startup.log"
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tailText(path, 2); got != "c\nd" {
		t.Fatalf("tailText: %q", got)
	}
	if got := tailText(path+".missing", 2); got != "" {
		t.Fatalf("нет файла: %q", got)
	}
}
