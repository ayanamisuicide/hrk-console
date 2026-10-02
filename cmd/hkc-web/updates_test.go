package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpdateEndpointsRequireAdministrator(t *testing.T) {
	s := newTestServer(t)
	for _, handler := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			s.updateStatus(w, httptest.NewRequest("GET", "/api/admin/updates", nil))
		},
		func(w *httptest.ResponseRecorder) {
			s.installUpdate(w, httptest.NewRequest("POST", "/api/admin/updates/install", nil))
		},
	} {
		w := httptest.NewRecorder()
		handler(w)
		if w.Code != 401 {
			t.Fatalf("unauthorized status: %d", w.Code)
		}
	}
}

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
