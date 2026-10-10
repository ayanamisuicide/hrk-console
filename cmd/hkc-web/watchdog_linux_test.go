//go:build linux

package main

import (
	"heroku-console/botproc"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Проверяем SIGKILL и запуск нового PID на изолированном Python-процессе,
// который игнорирует SIGTERM. Настоящая установка Heroku не участвует.
func TestForceRecoveryKillsUnresponsiveProcess(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	venv := filepath.Join(dir, ".venv")
	if output, err := exec.Command(python, "-m", "venv", "--without-pip", venv).CombinedOutput(); err != nil {
		t.Skipf("venv unavailable: %v %s", err, output)
	}
	packageDir := filepath.Join(dir, "heroku")
	if err := os.Mkdir(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	source := "import os,signal,time\nif os.getenv('HKC_TEST_IGNORE_TERM'): signal.signal(signal.SIGTERM, signal.SIG_IGN)\nopen('.test-ready','w').write(str(os.getpid()))\nwhile True: time.sleep(1)\n"
	if err := os.WriteFile(filepath.Join(packageDir, "__main__.py"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(venv, "bin", "python3"), "-m", "heroku")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HKC_TEST_IGNORE_TERM=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, ".test-ready"))
		if string(data) == strconv.Itoa(cmd.Process.Pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stub did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := newTestServer(t)
	s.bot = botproc.New(dir)
	t.Cleanup(func() { s.bot.Stop() })
	result := s.forceBotRecovery()
	if !result.OK || result.PID == cmd.Process.Pid || !s.bot.AliveAt(result.PID) {
		t.Fatalf("recovery: %+v", result)
	}
	if s.bot.AliveAt(cmd.Process.Pid) {
		t.Fatal("old unresponsive process survived recovery")
	}
}
