package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHerokuConfigMasksSecretsAndPreservesOtherKeys(t *testing.T) {
	s := newTestServer(t)
	if err := os.MkdirAll(s.bot.HerokuDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.configPath(), []byte(`{"custom_setting":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/config", bytes.NewBufferString(`{"api_id":"12345","api_hash":"0123456789abcdef0123456789abcdef"}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	response := httptest.NewRecorder()
	s.updateConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	if values["api_id"] != float64(12345) || values["custom_setting"] != "keep" {
		t.Fatalf("config changed unexpectedly: %v", values)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	get.Header.Set("Authorization", "Bearer admin-secret")
	masked := httptest.NewRecorder()
	s.adminConfig(masked, get)
	if masked.Code != http.StatusOK || bytes.Contains(masked.Body.Bytes(), []byte("0123456789abcdef")) || !bytes.Contains(masked.Body.Bytes(), []byte(`"api_hash":true`)) {
		t.Fatalf("config response leaked data: %s", masked.Body.String())
	}
}

func TestDiagnosticCommandRejectsUnknownAction(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/diagnostics/arbitrary", nil)
	r.SetPathValue("command", "arbitrary")
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.adminDiagnosticCommand(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected code: %d", w.Code)
	}
}

func TestAdminTerminalRequiresToken(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/terminal", strings.NewReader(`{"command":"pwd"}`))
	w := httptest.NewRecorder()
	s.adminTerminal(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected code: %d", w.Code)
	}
}

func TestAdminTerminalRunsInsideHerokuDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("terminal is available only on Linux")
	}
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/terminal", strings.NewReader(`{"command":"pwd; printf terminal-ok","confirmed":true}`))
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.adminTerminal(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), s.bot.HerokuDir) || !strings.Contains(w.Body.String(), "terminal-ok") {
		t.Fatalf("terminal failed: %d %s", w.Code, w.Body.String())
	}
}

func TestSystemHealthReturnsPlatform(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.systemHealth(w, httptest.NewRequest(http.MethodGet, "/api/system", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), runtime.GOOS) {
		t.Fatalf("system health failed: %d %s", w.Code, w.Body.String())
	}
}

func TestAPITokenScopeAndRevocation(t *testing.T) {
	s := newTestServer(t)
	view, raw, err := s.auth.createAPIToken("monitor", "read")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "hkc.") {
		t.Fatal("invalid token format")
	}
	if label, ok := s.auth.useAPIToken(raw, "read"); !ok || label != "monitor" {
		t.Fatal("read token rejected")
	}
	if _, ok := s.auth.useAPIToken(raw, "control"); ok {
		t.Fatal("read token gained control")
	}
	list := s.auth.listAPITokens()
	encoded, _ := json.Marshal(list)
	if len(list) != 1 || bytes.Contains(encoded, []byte(raw)) {
		t.Fatal("token list leaked secret")
	}
	if ok, err := s.auth.revokeAPIToken(view.ID); !ok || err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok := s.auth.useAPIToken(raw, "read"); ok {
		t.Fatal("revoked token still works")
	}
}

func TestViewerCannotControlBot(t *testing.T) {
	s := newTestServer(t)
	invite, _, err := s.auth.createInviteWithRole(time.Hour, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.auth.register(invite, "viewer", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.sessions.create("viewer")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := s.authorizeControl(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodPost, "/api/bot/start", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler(response, request)
	if called || response.Code != http.StatusForbidden {
		t.Fatalf("viewer control: called=%v status=%d", called, response.Code)
	}
	if ok, err := s.auth.setRole("viewer", "operator"); !ok || err != nil {
		t.Fatalf("change role: %v", err)
	}
	response = httptest.NewRecorder()
	handler(response, request)
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("operator control: called=%v status=%d", called, response.Code)
	}
}

func TestAuditPersistsAndInsightsUseLog(t *testing.T) {
	s := newTestServer(t)
	s.audit = newAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"))
	s.metrics = newMetricStore()
	s.record(httptest.NewRequest(http.MethodGet, "/", nil), "alice", "test", "Событие")
	events, err := s.audit.recent(10)
	if err != nil || len(events) != 1 || events[0].Actor != "alice" {
		t.Fatalf("audit: %+v %v", events, err)
	}
	response := httptest.NewRecorder()
	s.insights(response, httptest.NewRequest(http.MethodGet, "/api/insights", nil))
	var body struct {
		LogCounts    map[string]int `json:"logCounts"`
		SampledLines int            `json:"sampledLines"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.LogCounts["error"] != 0 {
		t.Fatalf("insights: %d %s", response.Code, response.Body.String())
	}
}

func TestAuditRetentionKeepsNewestEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	audit := newAuditStore(path)
	for i := 0; i < auditRetention+3; i++ {
		if err := audit.add(auditEvent{Actor: fmt.Sprintf("actor-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := audit.recent(auditRetention + 10)
	if err != nil || len(events) != auditRetention {
		t.Fatalf("retained %d events: %v", len(events), err)
	}
	if events[0].Actor != fmt.Sprintf("actor-%d", auditRetention+2) || events[len(events)-1].Actor != "actor-3" {
		t.Fatalf("unexpected retained range: first=%s last=%s", events[0].Actor, events[len(events)-1].Actor)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(data, []byte("\n")); lines != auditRetention {
		t.Fatalf("audit file has %d lines, want %d", lines, auditRetention)
	}
}

func TestMetricHistorySamplesEverySecondAndKeepsTwoMinutes(t *testing.T) {
	store := newMetricStore()
	start := time.Now().UTC()
	store.append(metricPoint{Time: start, RSS: 1})
	if points := store.append(metricPoint{Time: start.Add(500 * time.Millisecond), RSS: 2}); len(points) != 1 {
		t.Fatalf("sampled too early: %d points", len(points))
	}
	for i := 1; i <= 125; i++ {
		store.append(metricPoint{Time: start.Add(time.Duration(i) * time.Second), RSS: uint64(i + 1)})
	}
	points := store.append(metricPoint{Time: start.Add(125*time.Second + 500*time.Millisecond), RSS: 999})
	if len(points) != 120 || points[0].RSS != 7 || points[len(points)-1].RSS != 126 {
		t.Fatalf("unexpected metric window: %d points, first=%d last=%d", len(points), points[0].RSS, points[len(points)-1].RSS)
	}
	points = store.append(metricPoint{Time: start.Add(126 * time.Second), RSS: 40, PID: 123})
	if len(points) != 1 || points[0].RSS != 40 {
		t.Fatal("metric history was not reset after process change")
	}
}

func TestBackupRestorePreservesCurrentState(t *testing.T) {
	s := newTestServer(t)
	invite, _, err := s.auth.createInvite(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.auth.register(invite, "alice", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	backup, err := s.auth.backup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.deleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.auth.restore(backup.Name); err != nil {
		t.Fatal(err)
	}
	if !s.auth.authenticate("alice", "long-enough-password") {
		t.Fatal("user was not restored")
	}
	backups, err := s.auth.backups()
	if err != nil || len(backups) != 2 {
		t.Fatalf("pre-restore backup missing: %d %v", len(backups), err)
	}
	if err := s.auth.restore("../other.json"); err == nil {
		t.Fatal("path traversal accepted")
	}
}
