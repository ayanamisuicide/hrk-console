package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

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
