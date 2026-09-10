//go:build linux

package botproc

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Без venv запускать нечего: bash ушёл бы в `source venv/bin/activate` и
// умер бы молча в .startup.log, а консоль отрапортовала бы об успехе.
func TestStartRequiresVenv(t *testing.T) {
	requireNoBot(t)

	m := New(t.TempDir())
	res := m.Start()
	if res.Err == nil {
		t.Fatalf("без venv ожидалась ошибка, got %+v", res)
	}
	if !strings.Contains(res.Err.Error(), "venv") {
		t.Errorf("ошибка не объясняет причину: %v", res.Err)
	}
	if res.PID != 0 {
		t.Errorf("процесс не должен был запуститься, got pid %d", res.PID)
	}
}

// Лок держит другое окно — второй старт обязан отступить, а не поднять
// второго бота рядом.
func TestStartYieldsToHeldLock(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	lock, err := os.OpenFile(m.LockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("не удалось занять лок: %v", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	res := m.Start()
	if !res.AlreadyStarting {
		t.Errorf("при занятом локе ожидалось AlreadyStarting, got %+v", res)
	}
}

func TestStartFailsOnUnwritableLockPath(t *testing.T) {
	// Каталога нет — лок-файл создать негде.
	m := New(filepath.Join(t.TempDir(), "нет-такого"))
	if res := m.Start(); res.Err == nil {
		t.Errorf("ожидалась ошибка создания лока, got %+v", res)
	}
}
