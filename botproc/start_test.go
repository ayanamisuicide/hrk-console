package botproc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// requireNoBot защищает от тестов, которые на машине с работающим ботом
// проверяли бы совсем не то, что заявляют, — а то и трогали бы чужой процесс.
func requireNoBot(t *testing.T) {
	t.Helper()
	if PID() != 0 {
		t.Skip("на машине уже запущен бот — проверка недостоверна")
	}
}

func TestPIDAgreesWithPIDs(t *testing.T) {
	pids := PIDs()
	pid := PID()
	switch {
	case len(pids) == 0 && pid != 0:
		t.Errorf("PID вернул %d, хотя PIDs пуст", pid)
	case len(pids) > 0 && pid != pids[0]:
		t.Errorf("PID вернул %d, первый из PIDs — %d", pid, pids[0])
	}
	if Alive() != (pid != 0) {
		t.Errorf("Alive=%v при pid=%d", Alive(), pid)
	}
}

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

// heroku.log растёт до 10 МБ, а нужна версия текущей сессии — читается
// только хвост. Баннер, оставшийся далеко в начале файла, находиться не
// должен: он от давно завершённого запуска.
func TestVersionReadsOnlyTail(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	var buf bytes.Buffer
	buf.WriteString("🪐 Heroku 1.0.0 #old started\n")
	buf.Write(bytes.Repeat([]byte("шум шум шум\n"), 40000)) // сильно больше хвоста в 256 КБ
	if err := os.WriteFile(m.LogFile, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.Version(); got != "" {
		t.Errorf("баннер за пределами хвоста не должен находиться, got %q", got)
	}

	buf.WriteString("🪐 Heroku 2.3.0 #new started\n")
	if err := os.WriteFile(m.LogFile, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.Version(); got != "2.3.0" {
		t.Errorf("версия из хвоста: got %q, want 2.3.0", got)
	}
}

func TestUptimeUnknownPID(t *testing.T) {
	if got := Uptime(1 << 30); got != "—" {
		t.Errorf("для отсутствующего процесса: got %q, want —", got)
	}
}
