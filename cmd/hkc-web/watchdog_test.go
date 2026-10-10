package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchdogTimeoutRecoveryAndCooldown(t *testing.T) {
	settings := watchdogSettings{Enabled: true, TimeoutSeconds: 180}
	now := time.Now()
	var monitor watchdogMonitor
	if monitor.tick(now, settings, 42, true, "") {
		t.Fatal("healthy bot must not restart")
	}
	if monitor.tick(now.Add(time.Second), settings, 42, false, "hung") {
		t.Fatal("must wait")
	}
	if monitor.tick(now.Add(180*time.Second), settings, 42, false, "hung") {
		t.Fatal("must wait full 180 seconds")
	}
	if !monitor.tick(now.Add(181*time.Second), settings, 42, false, "hung") {
		t.Fatal("hung bot must recover")
	}
	monitor.complete(now.Add(187*time.Second), actionResponse{Message: "failed"})
	if monitor.tick(now.Add(366*time.Second), settings, 0, false, "stopped") {
		t.Fatal("retry too soon")
	}
	if !monitor.tick(now.Add(367*time.Second), settings, 0, false, "stopped") {
		t.Fatal("failed recovery must retry")
	}
	if got := monitor.snapshot(); got.Attempts != 1 || got.LastResult != "failed" {
		t.Fatalf("status: %+v", got)
	}
}

func TestWatchdogNewProcessDisableAndHealthyReset(t *testing.T) {
	now := time.Now()
	settings := watchdogSettings{Enabled: true, TimeoutSeconds: 180}
	var monitor watchdogMonitor
	monitor.tick(now, settings, 0, false, "stopped")
	if monitor.tick(now.Add(179*time.Second), settings, 52, false, "starting") {
		t.Fatal("new pid needs startup grace")
	}
	if monitor.tick(now.Add(180*time.Second), settings, 52, false, "starting") {
		t.Fatal("new pid restarted too early")
	}
	monitor.tick(now.Add(200*time.Second), settings, 52, true, "")
	if monitor.tick(now.Add(350*time.Second), settings, 52, false, "hung again") {
		t.Fatal("health must reset outage")
	}
	settings.Enabled = false
	if monitor.tick(now.Add(time.Hour), settings, 0, false, "manual stop") {
		t.Fatal("disabled watchdog restarted bot")
	}
	if monitor.snapshot().State != "disabled" {
		t.Fatal("wrong disabled state")
	}
}

func TestWatchdogSettingsPersistAndLegacyFieldsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	if err := os.WriteFile(path, []byte(`{"maintenance":{"enabled":true},"schedules":[{"status":"pending","action":"stop"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.watchdogSettings(); !got.Enabled || got.TimeoutSeconds != 180 {
		t.Fatalf("defaults: %+v", got)
	}
	settings := watchdogSettings{Enabled: false, TimeoutSeconds: 240}
	if err := store.setWatchdog(settings); err != nil {
		t.Fatal(err)
	}
	reopened, err := openOperationStore(path)
	if err != nil || reopened.watchdogSettings() != settings {
		t.Fatalf("reopen: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "maintenance") || strings.Contains(string(data), "schedules") {
		t.Fatal("legacy features still saved")
	}
	if err := store.setWatchdog(watchdogSettings{TimeoutSeconds: 0}); err == nil {
		t.Fatal("invalid timeout accepted")
	}
	if store.watchdogSettings() != settings {
		t.Fatal("invalid change overwrote settings")
	}
}

func TestWatchdogHTTPProtectionAndValidation(t *testing.T) {
	s := newTestServer(t)
	var err error
	s.operations, err = openOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.getWatchdog(response, httptest.NewRequest(http.MethodGet, "/api/admin/watchdog", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", response.Code)
	}
	for _, body := range []string{`{"enabled":true,"timeoutSeconds":0}`, `{"enabled":true,"timeoutSeconds":180,"unknown":1}`, `{"enabled":true,"timeoutSeconds":180} {}`} {
		response = httptest.NewRecorder()
		s.setWatchdog(response, adminRequest(http.MethodPut, "/api/admin/watchdog", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d", body, response.Code)
		}
	}
	response = httptest.NewRecorder()
	s.setWatchdog(response, adminRequest(http.MethodPut, "/api/admin/watchdog", strings.NewReader(`{"enabled":false,"timeoutSeconds":180}`)))
	if response.Code != http.StatusOK || s.operations.watchdogSettings().Enabled {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/admin/maintenance", "/api/admin/schedules"} {
		response = httptest.NewRecorder()
		s.routes().ServeHTTP(response, adminRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("removed route %s still exists: %d", path, response.Code)
		}
	}
}
