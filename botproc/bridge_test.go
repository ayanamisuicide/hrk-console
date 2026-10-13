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
	// Повторная установка при следующем запуске заменяет файлы и не оставляет временных копий.
	if err := New(dir).InstallModulesBridge(); err != nil {
		t.Fatal(err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(site, ".hkc-bridge-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

// TestInstallModulesBridgeRefusesUnknownLayout: без окружения или с несколькими site-packages
// мост не ставится наугад, а сообщает причину.
func TestInstallModulesBridgeRefusesUnknownLayout(t *testing.T) {
	dir := t.TempDir()
	if err := New(dir).InstallModulesBridge(); err == nil || !strings.Contains(err.Error(), "окружение") {
		t.Fatalf("without venv: %v", err)
	}
	venv := filepath.Join(dir, "venv")
	for _, path := range []string{filepath.Join(venv, "bin"), filepath.Join(venv, "lib", "python3.11", "site-packages"),
		filepath.Join(venv, "lib", "python3.12", "site-packages")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(venv, "bin", "python3"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(dir).InstallModulesBridge(); err == nil || !strings.Contains(err.Error(), "site-packages") {
		t.Fatalf("with two site-packages: %v", err)
	}
	if err := writeBridgeFile(filepath.Join(dir, "missing", "bridge.py"), []byte("x")); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
}
