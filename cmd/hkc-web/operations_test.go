package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func adminRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("Authorization", "Bearer admin-secret")
	return request
}

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

func TestDiagnosticBundleRedactsSecrets(t *testing.T) {
	s := newTestServer(t)
	s.audit = newAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err := os.MkdirAll(s.bot.HerokuDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "very-secret-value"
	config := `{"api_hash":"` + secret + `","api_id":123}`
	if err := os.WriteFile(s.configPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logLine := "bot_token=" + secret + " hkc.identifier.another-secret"
	if err := os.WriteFile(s.bot.LogFile, []byte(logLine), 0o600); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	s.diagnosticBundle(response, adminRequest(http.MethodGet, "/api/admin/diagnostic-bundle", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("bundle: %d %s", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	all := bytes.Buffer{}
	names := make(map[string]bool)
	for _, file := range archive.File {
		names[file.Name] = true
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(&all, reader)
		_ = reader.Close()
	}
	for _, name := range []string{"version.json", "status.json", "system.json", "config-summary.json", "audit.json", "bot.log"} {
		if !names[name] {
			t.Errorf("bundle is missing %s", name)
		}
	}
	if strings.Contains(all.String(), secret) || strings.Contains(all.String(), "another-secret") {
		t.Fatalf("bundle leaked a secret: %s", all.String())
	}
	if !strings.Contains(all.String(), "[REDACTED]") {
		t.Fatal("bundle did not contain a redaction marker")
	}
}

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
