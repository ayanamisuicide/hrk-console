package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
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
	for _, path := range []string{"/", "/motion.js", "/app.js", "/modules/journal.js", "/css/base.css", "/admin/", "/admin/modules/auth.js", "/css/admin.css", "/theme.js", "/api/version"} {
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

// Список маршрутов берётся из routes.go, а не пишется вручную: новый маршрут без защиты
// не пройдёт тест, даже если про него забыли здесь.
var routeDeclaration = regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) (/[^"]*)"`)

// Открытые без входа маршруты и то, чем они должны отвечать без учётных данных.
var openRoutes = map[string][]int{
	"GET /api/version":        {http.StatusOK},
	"POST /api/auth/login":    {http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests},
	"POST /api/auth/register": {http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests},
	"GET /api/public/status":  {http.StatusNotFound}, // публичная страница выключена по умолчанию
	"POST /api/tg/session":    {http.StatusBadRequest, http.StatusUnauthorized, http.StatusServiceUnavailable},
}

func TestEveryRouteRejectsAnonymousRequests(t *testing.T) {
	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := routeDeclaration.FindAllStringSubmatch(string(source), -1)
	if len(declared) < 60 {
		t.Fatalf("в routes.go найдено только %d маршрутов: разбор сломался", len(declared))
	}
	handler := newTestServer(t).routes()
	placeholder := regexp.MustCompile(`\{[^}]+\}`)
	for _, route := range declared {
		method, pattern := route[1], route[2]
		key := method + " " + pattern
		path := placeholder.ReplaceAllString(pattern, "x")
		for _, credentials := range []string{"", "Bearer wrong-token", "Bearer "} {
			request := httptest.NewRequest(method, path, strings.NewReader("{}"))
			if credentials != "" {
				request.Header.Set("Authorization", credentials)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if allowed, open := openRoutes[key]; open {
				if !slices.Contains(allowed, response.Code) {
					t.Errorf("%s (%q): код %d, допустимо %v", key, credentials, response.Code, allowed)
				}
				continue
			}
			// Маршруты мини-приложения без настроенного бота отвечают 503: данных за ними нет.
			miniApp503 := strings.HasPrefix(pattern, "/api/tg/") && response.Code == http.StatusServiceUnavailable
			if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden && !miniApp503 {
				t.Errorf("%s (%q): без входа получили %d, ждали 401/403", key, credentials, response.Code)
			}
		}
	}
}
