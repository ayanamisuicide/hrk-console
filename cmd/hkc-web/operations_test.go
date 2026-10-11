package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// adminRequest готовит запрос с тестовым административным токеном.
func adminRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("Authorization", "Bearer admin-secret")
	return request
}

// TestConfigValidationHidesValues проверяет предварительную валидацию и отсутствие секретных значений
// в ответе.
func TestConfigValidationHidesValues(t *testing.T) {
	s := newTestServer(t)
	if err := os.MkdirAll(s.bot.HerokuDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(s.configPath(), []byte(`{"api_id":123,"app_name":"before"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	s.validateConfig(response, adminRequest(http.MethodPost, "/api/admin/config/validate", strings.NewReader(`{"api_hash":"`+secret+`","app_name":"after"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"key":"api_hash","change":"added"`) || !strings.Contains(response.Body.String(), `"key":"app_name","change":"changed"`) {
		t.Fatalf("validate: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("validation response leaked the submitted value")
	}
}

// TestSecurityOverviewRequiresAdmin проверяет защиту сводки безопасности и её данные.
func TestSecurityOverviewRequiresAdmin(t *testing.T) {
	s := newTestServer(t)
	unauthorized := httptest.NewRecorder()
	s.securityOverview(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/security", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("without admin token: %d", unauthorized.Code)
	}

	response := httptest.NewRecorder()
	s.securityOverview(response, adminRequest(http.MethodGet, "/api/admin/security", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"rateLimiter"`) {
		t.Fatalf("overview: %d %s", response.Code, response.Body.String())
	}
}
