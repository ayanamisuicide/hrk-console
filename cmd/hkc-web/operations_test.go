package main

import (
	"bytes"
	"encoding/json"
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

// TestConfigHistorySnapshotAndRestore проверяет сохранение предыдущей конфигурации и восстановление версии.
func TestConfigHistorySnapshotAndRestore(t *testing.T) {
	s := newTestServer(t)
	if err := os.MkdirAll(s.bot.HerokuDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"api_id":123,"app_name":"before"}`)
	if err := os.WriteFile(s.configPath(), original, 0o600); err != nil {
		t.Fatal(err)
	}

	s.configMu.Lock()
	if err := s.snapshotConfigLocked(); err != nil {
		s.configMu.Unlock()
		t.Fatal(err)
	}
	history, err := s.configHistoryLocked()
	s.configMu.Unlock()
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %v, %+v", err, history)
	}
	if err := os.WriteFile(s.configPath(), []byte(`{"api_id":456}`), 0o600); err != nil {
		t.Fatal(err)
	}

	request := adminRequest(http.MethodPost, "/api/admin/config/history/restore", nil)
	request.SetPathValue("name", history[0].Name)
	response := httptest.NewRecorder()
	s.restoreConfigHistory(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", response.Code, response.Body.String())
	}
	restored, err := os.ReadFile(s.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]json.RawMessage
	_ = json.Unmarshal(restored, &got)
	_ = json.Unmarshal(original, &want)
	if !bytes.Equal(got["app_name"], want["app_name"]) || !bytes.Equal(got["api_id"], want["api_id"]) {
		t.Fatalf("restored config = %s", restored)
	}
}

// TestConfigValidationAndHistoryDiffHideValues проверяет предварительную валидацию и отсутствие секретных
// значений в сравнении.
func TestConfigValidationAndHistoryDiffHideValues(t *testing.T) {
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

	s.configMu.Lock()
	if err := s.snapshotConfigLocked(); err != nil {
		s.configMu.Unlock()
		t.Fatal(err)
	}
	history, _ := s.configHistoryLocked()
	s.configMu.Unlock()
	if err := os.WriteFile(s.configPath(), []byte(`{"api_id":456,"api_hash":"`+secret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := adminRequest(http.MethodGet, "/api/admin/config/history/diff", nil)
	request.SetPathValue("name", history[0].Name)
	response = httptest.NewRecorder()
	s.diffConfigHistory(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"change":"removed"`) {
		t.Fatalf("diff: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("history diff leaked a configuration value")
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
