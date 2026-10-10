package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRoutesKeepProtectedEndpoints проверяет защиту маршрутов и доступность встроенных зависимостей
// интерфейса.
func TestRoutesKeepProtectedEndpoints(t *testing.T) {
	s := newTestServer(t)
	handler := s.routes()
	for _, path := range []string{"/api/admin/watchdog", "/api/modules", "/api/status", "/api/metrics", "/api/insights", "/api/v1/status", "/api/admin/config"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want unauthorized", response.Code)
			}
		})
	}
	for _, path := range []string{"/", "/motion.js", "/app.js", "/modules/journal.js", "/styles/foundation.css", "/admin/", "/admin/modules/auth.js", "/admin/styles/layout.css", "/api/version"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: got %d", path, response.Code)
		}
	}
}

// Удалённые функции не возвращаются даже при старом включающем флаге и правильном токене.
func TestRemovedDiagnosticAndTerminalRoutes(t *testing.T) {
	t.Setenv("HKC_TERMINAL_ENABLED", "1")
	handler := newTestServer(t).routes()
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/terminal"},
		{http.MethodPost, "/api/admin/diagnostics/process"},
		{http.MethodGet, "/api/admin/diagnostic-bundle"},
		{http.MethodGet, "/api/diagnostics"},
	} {
		request := httptest.NewRequest(endpoint.method, endpoint.path, nil)
		request.Header.Set("Authorization", "Bearer admin-secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: removed route returned %d", endpoint.path, response.Code)
		}
	}
}
