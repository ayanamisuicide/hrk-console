package botproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallModulesBridgeScopedToVenv(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	site := filepath.Join(venv, "lib", "python3.12", "site-packages")
	for _, path := range []string{filepath.Join(venv, "bin"), site} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(venv, "bin", "python3"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(dir).InstallModulesBridge(); err != nil {
		t.Fatal(err)
	}
	pth, err := os.ReadFile(filepath.Join(site, "hkc_modules_bridge.pth"))
	if err != nil || !strings.Contains(string(pth), "hkc_modules_bridge.install(") {
		t.Fatalf("pth: %s, %v", pth, err)
	}
	if data, err := os.ReadFile(filepath.Join(site, "hkc_modules_bridge.py")); err != nil || len(data) == 0 {
		t.Fatalf("bridge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "heroku")); !os.IsNotExist(err) {
		t.Fatal("installer must not change Heroku sources")
	}
}
