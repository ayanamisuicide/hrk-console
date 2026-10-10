package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestUpdateProgressReturnsRecordedSteps проверяет выдачу сохранённых этапов установки.
func TestUpdateProgressReturnsRecordedSteps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HKC_UPDATE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"phase":"downloading","progress":48,"events":[{"message":"SHA-256 подтверждена"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/admin/updates/progress", nil)
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.updateProgress(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("SHA-256 подтверждена")) {
		t.Fatalf("progress: %d %s", w.Code, w.Body.String())
	}
}

// TestUpdateEndpointsRequireAdministrator проверяет административную защиту маршрутов обновления.
func TestUpdateEndpointsRequireAdministrator(t *testing.T) {
	s := newTestServer(t)
	for _, handler := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			s.updateStatus(w, httptest.NewRequest("GET", "/api/admin/updates", nil))
		},
		func(w *httptest.ResponseRecorder) {
			s.installUpdate(w, httptest.NewRequest("POST", "/api/admin/updates/install", nil))
		},
		func(w *httptest.ResponseRecorder) {
			s.updateProgress(w, httptest.NewRequest("GET", "/api/admin/updates/progress", nil))
		},
	} {
		w := httptest.NewRecorder()
		handler(w)
		if w.Code != 401 {
			t.Fatalf("unauthorized status: %d", w.Code)
		}
	}
}

// TestInstallUpdateDisabledByDefault проверяет запрет установки без явного включения.
func TestInstallUpdateDisabledByDefault(t *testing.T) {
	t.Setenv("HKC_UPDATE_ENABLED", "")
	s := newTestServer(t)
	r := httptest.NewRequest("POST", "/api/admin/updates/install", nil)
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.installUpdate(w, r)
	if w.Code != 409 {
		t.Fatalf("got %d", w.Code)
	}
}

// TestSourceInspectionFindsUncommittedAndUntrackedChanges проверяет обнаружение изменённых и новых файлов в
// копии Git.
func TestSourceInspectionFindsUncommittedAndUntrackedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
	clean := inspectSource(context.Background(), dir)
	if clean.Error != "" || clean.Dirty || clean.Branch != "main" || len(clean.Commit) != 40 {
		t.Fatalf("clean: %+v", clean)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !inspectSource(context.Background(), dir).Dirty {
		t.Fatal("untracked file was ignored")
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !inspectSource(context.Background(), dir).Dirty {
		t.Fatal("modified file was ignored")
	}
}

// TestStableReleaseTagValidation проверяет допустимый формат стабильного тега релиза.
func TestStableReleaseTagValidation(t *testing.T) {
	for _, tag := range []string{"v2.2.0-rc1", "../main", "v2.2.0/other", "main", "v2.2"} {
		if releaseTag.MatchString(tag) {
			t.Fatalf("accepted %q", tag)
		}
	}
	if !releaseTag.MatchString("v2.2.0") {
		t.Fatal("stable tag rejected")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{{"v2.10.0", "v2.9.9", 1}, {"v2.5.1", "v2.5.1", 0}, {"v2.4.0", "v2.5.0", -1}, {"dev", "v0.0.1", -1}, {"v1.0.0", "garbage", 1}}
	for _, item := range cases {
		if got := compareVersions(item.a, item.b); got != item.want {
			t.Fatalf("%s vs %s: %d", item.a, item.b, got)
		}
	}
}

// TestParseChangelogReadsProjectFile разбирает настоящий CHANGELOG.md проекта.
func TestParseChangelogReadsProjectFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	releases := parseChangelog(string(data), 5)
	if len(releases) != 5 {
		t.Fatalf("releases: %d", len(releases))
	}
	for _, release := range releases {
		if !releaseTag.MatchString(release.Version) || release.Date == "" || len(release.Sections) == 0 || len(release.Sections[0].Items) == 0 {
			t.Fatalf("release: %+v", release)
		}
		for _, section := range release.Sections {
			for _, item := range section.Items {
				if strings.Contains(item, "](") {
					t.Fatalf("link markup left: %q", item)
				}
			}
		}
	}
	notes := parseChangelog("## [3.0.0](x) — 2026-11-01\n\n### Добавлено\n\n- Первый [пункт](https://e)\n  продолжение\n- Второй\n\n## 2.0.0\n### Изменено\n- старое\n", 10)
	if len(notes) != 2 || notes[0].Sections[0].Items[0] != "Первый пункт продолжение" || notes[1].Version != "v2.0.0" {
		t.Fatalf("notes: %+v", notes)
	}
}

func updateTestServer(t *testing.T) *server {
	t.Helper()
	s := newTestServer(t)
	s.dataDir = t.TempDir()
	s.updates = &updateChecker{}
	s.updates.remote = remoteVersion{Version: "v9.0.0", Commit: strings.Repeat("a", 40), CheckedAt: time.Now()}
	state := t.TempDir()
	t.Setenv("HKC_UPDATE_DIR", state)
	t.Setenv("HKC_SOURCE_DIR", "")
	t.Setenv("HKC_LOCAL_SOURCE_DIR", "")
	return s
}

func TestUpdateOverviewExplainsBlockers(t *testing.T) {
	s := updateTestServer(t)
	t.Setenv("HKC_UPDATE_ENABLED", "")
	overview := s.buildUpdateOverview(context.Background())
	if !overview.UpdateAvailable || len(overview.Blockers) == 0 || overview.Blockers[0].Fix == "" {
		t.Fatalf("overview: %+v", overview)
	}
	r := httptest.NewRequest("POST", "/api/admin/updates/install", nil)
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	s.installUpdate(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "установщик") {
		t.Fatalf("blocked install: %d %s", w.Code, w.Body.String())
	}
}

func TestInstallAndRollbackWriteRequestForService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("установка из панели работает только в Linux")
	}
	s := updateTestServer(t)
	t.Setenv("HKC_UPDATE_ENABLED", "1")
	started := 0
	previous := startUpdateService
	startUpdateService = func(context.Context) error { started++; return nil }
	defer func() { startUpdateService = previous }()
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer admin-secret")
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w
	}
	if w := request("/api/admin/updates/install", `{"version":"v2.4.0"}`); w.Code != 202 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(s.dataDir, "update-request.json"))
	if err != nil || !strings.Contains(string(data), `"version":"v2.4.0"`) || started != 1 {
		t.Fatalf("request: %s %v started=%d", data, err, started)
	}
	if w := request("/api/admin/updates/install", `{"version":"main"}`); w.Code != 400 {
		t.Fatalf("bad version accepted: %d", w.Code)
	}
	if w := request("/api/admin/updates/rollback", `{"backup":"20261010T000000-v2.4.0"}`); w.Code != 404 {
		t.Fatalf("unknown backup: %d", w.Code)
	}
	index := `{"backups":[{"name":"20261010T000000-v2.4.0","version":"v2.4.0"},{"name":"../evil","version":"x"}]}`
	if err := os.WriteFile(filepath.Join(os.Getenv("HKC_UPDATE_DIR"), "backups.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readBackups(); len(got) != 1 {
		t.Fatalf("unsafe backup names must be dropped: %+v", got)
	}
	if w := request("/api/admin/updates/rollback", `{"backup":"20261010T000000-v2.4.0"}`); w.Code != 202 {
		t.Fatalf("rollback: %d %s", w.Code, w.Body.String())
	}
	status := `{"phase":"downloading","updatedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(os.Getenv("HKC_UPDATE_DIR"), "status.json"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/admin/updates/install", `{}`); w.Code != 409 {
		t.Fatalf("parallel install: %d", w.Code)
	}
}

func TestJobRunningIgnoresStaleJobs(t *testing.T) {
	if !jobRunning(map[string]any{"phase": "restarting", "updatedAt": time.Now().UTC().Format(time.RFC3339Nano)}) {
		t.Fatal("fresh job must run")
	}
	if jobRunning(map[string]any{"phase": "restarting", "updatedAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}) {
		t.Fatal("stale job must not block")
	}
	if jobRunning(map[string]any{"phase": "complete"}) {
		t.Fatal("finished job")
	}
}

func TestCheckerAnnouncesNewVersionOnce(t *testing.T) {
	announced := make(chan string, 4)
	checker := &updateChecker{
		fetch:        func(context.Context) (remoteVersion, error) { return remoteVersion{Version: "v99.0.0"}, nil },
		onNewVersion: func(version string, _ []releaseNotes) { announced <- version },
	}
	for attempt := 0; attempt < 2; attempt++ {
		checker.check()
		deadline := time.Now().Add(2 * time.Second)
		for checker.snapshot().Checking || checker.snapshot().CheckedAt.IsZero() {
			if time.Now().After(deadline) {
				t.Fatal("check did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
		checker.mu.Lock()
		checker.remote.CheckedAt = time.Time{}
		checker.mu.Unlock()
	}
	if len(announced) != 1 || <-announced != "v99.0.0" {
		t.Fatal("new version must be announced exactly once")
	}
}
