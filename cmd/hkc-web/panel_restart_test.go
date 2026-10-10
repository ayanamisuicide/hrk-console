package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubPanelExit подменяет выход процесса и признак systemd на время теста.
func stubPanelExit(t *testing.T, systemd bool) chan int {
	t.Helper()
	exited := make(chan int, 1)
	previousExit, previousSystemd := exitProcess, underSystemd
	exitProcess = func(code int) { exited <- code }
	underSystemd = func() bool { return systemd }
	t.Cleanup(func() { exitProcess, underSystemd = previousExit, previousSystemd })
	return exited
}

func restartRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/admin/panel/restart", nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

// TestRestartPanelRequiresAdmin не даёт перезапустить панель без административного токена.
func TestRestartPanelRequiresAdmin(t *testing.T) {
	exited := stubPanelExit(t, true)
	s := newTestServer(t)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, restartRequest("wrong"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want unauthorized", response.Code)
	}
	select {
	case <-exited:
		t.Fatal("panel exited without authorization")
	case <-time.After(700 * time.Millisecond):
	}
}

// TestRestartPanelOutsideSystemd отказывает, если панель некому поднять после выхода.
func TestRestartPanelOutsideSystemd(t *testing.T) {
	stubPanelExit(t, false)
	s := newTestServer(t)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, restartRequest("admin-secret"))
	if response.Code != http.StatusConflict {
		t.Fatalf("got %d, want conflict", response.Code)
	}
}

// TestRestartPanelExitsAfterResponse отвечает клиенту и затем завершает процесс для systemd.
func TestRestartPanelExitsAfterResponse(t *testing.T) {
	exited := stubPanelExit(t, true)
	t.Setenv("HKC_UPDATE_DIR", "")
	s := newTestServer(t)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, restartRequest("admin-secret"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("got %d: %s", response.Code, response.Body.String())
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("panel did not exit")
	}
}

// TestRestartPanelWaitsForUpdate не перебивает идущее обновление.
func TestRestartPanelWaitsForUpdate(t *testing.T) {
	stubPanelExit(t, true)
	dir := t.TempDir()
	t.Setenv("HKC_UPDATE_DIR", dir)
	status := `{"phase":"downloading","updatedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, restartRequest("admin-secret"))
	if response.Code != http.StatusConflict {
		t.Fatalf("got %d, want conflict", response.Code)
	}
}
