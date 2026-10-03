package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHostHistoryPersistsAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-history.json")
	history := newHostHistoryStore(path)
	now := time.Now()
	if err := history.add(hostPoint{At: now.Add(-2 * time.Hour), CPU: 5, PID: 10}); err != nil {
		t.Fatal(err)
	}
	if err := history.add(hostPoint{At: now, CPU: 30, PID: 11}); err != nil {
		t.Fatal(err)
	}
	reopened := newHostHistoryStore(path)
	if points := reopened.since(now.Add(-time.Hour)); len(points) != 1 || points[0].PID != 11 {
		t.Fatalf("unexpected points: %+v", points)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("history permissions: %v %v", info, err)
	}
}

func TestDetectIncidentsGroupsErrorsAndWarningBursts(t *testing.T) {
	lines := []string{
		"2026-10-03 10:00:00 [INFO] Core: starting",
		"2026-10-03 10:00:01 [ERROR] Core: first failure",
		"2026-10-03 10:00:02 [ERROR] Core: second failure",
		"2026-10-03 10:10:01 [WARNING] Net: retry 1",
		"2026-10-03 10:10:02 [WARNING] Net: retry 2",
		"2026-10-03 10:10:03 [WARNING] Net: retry 3",
	}
	got := detectIncidents(lines)
	if len(got) != 2 || got[0].Module != "Net" || got[0].Count != 3 || got[1].Count != 2 || got[1].Context == "" {
		t.Fatalf("incidents: %+v", got)
	}
}

func TestTerminalRequiresExplicitConfirmation(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/terminal", strings.NewReader(`{"command":"pwd"}`))
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.adminTerminal(w, r)
	if w.Code != http.StatusPreconditionRequired && w.Code != http.StatusNotImplemented {
		t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
	}
}
