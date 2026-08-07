package botproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUptimeFormat(t *testing.T) {
	if got := Uptime(0); got != "—" {
		t.Errorf("незапущенный процесс: got %q, want —", got)
	}
	// Собственный процесс жив заведомо, и время должно разбираться без
	// ошибок: формат /proc/<pid>/stat — единственное, на чём это стоит.
	got := Uptime(os.Getpid())
	if got == "—" {
		t.Fatal("для живого процесса аптайм должен считаться")
	}
	if !strings.Contains(got, "м") {
		t.Errorf("формат аптайма: got %q", got)
	}
}

// Процесс мог умереть между чтениями, и /proc отдаст обрезанный файл. Раньше
// срез уходил за границу и ронял весь интерфейс паникой — а вызывается это
// раз в секунду.
func TestStartTimeDoesNotPanicOnGarbage(t *testing.T) {
	if _, err := startTime(-1); err == nil {
		t.Error("для несуществующего процесса ожидалась ошибка")
	}
	if _, err := startTime(1 << 30); err == nil {
		t.Error("для заведомо отсутствующего pid ожидалась ошибка")
	}
}

func TestVersionFromLog(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	// Лога ещё нет — версии тоже, но падать не на чем.
	if got := m.Version(); got != "" {
		t.Errorf("без файла лога: got %q, want пусто", got)
	}

	log := filepath.Join(dir, "heroku.log")
	os.WriteFile(log, []byte("шум\n🪐 Heroku 2.2.1 #abc started\nещё шум\n🪐 Heroku 2.2.2 #def started\n"), 0o644)
	// Берётся последний баннер: в файле накапливаются все сессии, а нужна
	// та версия, с которой бот работает сейчас.
	if got := m.Version(); got != "2.2.2" {
		t.Errorf("версия: got %q, want 2.2.2", got)
	}

	os.WriteFile(log, []byte(""), 0o644)
	if got := m.Version(); got != "" {
		t.Errorf("пустой лог: got %q, want пусто", got)
	}
}

// AliveAt читает один pid вместо полного обхода /proc — используется вместо
// PIDs() там, где pid уже известен с прошлого тика (gui/app.go, tickPID).
func TestAliveAt(t *testing.T) {
	// Свой процесс жив, но это тест, а не бот — needle не совпадёт.
	if AliveAt(os.Getpid()) {
		t.Error("AliveAt(себя) — не бот, ожидался false")
	}
	if AliveAt(-1) {
		t.Error("AliveAt(-1) — несуществующий pid, ожидался false")
	}
	if AliveAt(1 << 30) {
		t.Error("AliveAt(заведомо отсутствующий pid) — ожидался false")
	}
}

func TestNewPaths(t *testing.T) {
	m := New("/tmp/бот")
	if m.LogFile != "/tmp/бот/heroku.log" {
		t.Errorf("путь к логу: got %q", m.LogFile)
	}
	if m.LockFile != "/tmp/бот/.launch.lock" {
		t.Errorf("путь к локу: got %q", m.LockFile)
	}
}
