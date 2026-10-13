//go:build linux

package botproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHeroku готовит установку, которую Start примет за настоящую: .venv/bin/python3 ведёт на
// системный Python, а пакет heroku в каталоге бота выполняет переданный код. Менеджер ищет
// процессы только в своём каталоге, поэтому работающий рядом настоящий бот тестам не мешает.
func fakeHeroku(t *testing.T, main string) *Manager {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	dir := t.TempDir()
	for _, path := range []string{filepath.Join(dir, ".venv", "bin"), filepath.Join(dir, "heroku")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(python, filepath.Join(dir, ".venv", "bin", "python3")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "heroku", "__main__.py"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	t.Cleanup(func() {
		if m.Alive() {
			m.Stop()
		}
	})
	return m
}

const sleepingBot = "import sys, time\nprint('бот запущен', sys.argv[1:], flush=True)\ntime.sleep(60)\n"

// TestStartAndStopBot проходит обычный путь: запуск, повторный запуск без второго процесса,
// аптайм, вывод в .startup.log с копией прошлого запуска и штатная остановка.
func TestStartAndStopBot(t *testing.T) {
	m := fakeHeroku(t, sleepingBot)
	if err := os.WriteFile(m.StartupLog, []byte("прошлый запуск\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := m.Start()
	if res.Err != nil || res.PID == 0 {
		t.Fatalf("start: %+v", res)
	}
	if got := m.PID(); got != res.PID {
		t.Fatalf("PID() = %d, запущен %d", got, res.PID)
	}
	if !m.Alive() || !m.AliveAt(res.PID) {
		t.Fatal("запущенный бот не виден менеджеру")
	}
	if again := m.Start(); again.Err != nil || again.PID != res.PID {
		t.Fatalf("повторный запуск должен вернуть тот же процесс: %+v", again)
	}
	if pids := m.PIDs(); len(pids) != 1 {
		t.Fatalf("ожидался один процесс, got %v", pids)
	}
	if got := Uptime(res.PID); got == "—" {
		t.Fatal("аптайм запущенного бота не посчитан")
	}

	previous, err := os.ReadFile(m.StartupLog + ".1")
	if err != nil || string(previous) != "прошлый запуск\n" {
		t.Fatalf("вывод прошлого запуска не сохранён: %q, %v", previous, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var output []byte
	for time.Now().Before(deadline) {
		output, _ = os.ReadFile(m.StartupLog)
		if strings.Contains(string(output), "бот запущен") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(output), "бот запущен") || !strings.Contains(string(output), "--root") {
		t.Fatalf("вывод бота не попал в .startup.log: %q", output)
	}
	// Без site-packages мост не ставится, но запуск это не останавливает, а причина видна в выводе.
	if !strings.Contains(string(output), "Мониторинг модулей не подключён") {
		t.Fatalf("причина без моста не записана: %q", output)
	}

	if code := m.Stop(); code != 0 {
		t.Fatalf("штатная остановка вернула %d", code)
	}
	if m.Alive() {
		t.Fatal("бот жив после остановки")
	}
	if code := m.Stop(); code != 1 {
		t.Fatalf("остановка незапущенного бота вернула %d, want 1", code)
	}
}

// TestStopKillsBotIgnoringSIGTERM проверяет, что бот, не реагирующий на SIGTERM, добивается SIGKILL.
func TestStopKillsBotIgnoringSIGTERM(t *testing.T) {
	m := fakeHeroku(t, "import signal, time\nsignal.signal(signal.SIGTERM, signal.SIG_IGN)\ntime.sleep(60)\n")
	if res := m.Start(); res.Err != nil {
		t.Fatalf("start: %+v", res)
	}
	if code := m.Stop(); code != 2 {
		t.Fatalf("Stop вернул %d, want 2 (SIGKILL)", code)
	}
	if m.Alive() {
		t.Fatal("бот жив после SIGKILL")
	}
}

// TestStartReportsImmediateExit: бот, упавший сразу после запуска, — ошибка со ссылкой на вывод, а не успех.
func TestStartReportsImmediateExit(t *testing.T) {
	m := fakeHeroku(t, "import sys\nprint('ImportError: no module', file=sys.stderr)\nsys.exit(3)\n")
	res := m.Start()
	if res.Err == nil || !strings.Contains(res.Err.Error(), m.StartupLog) {
		t.Fatalf("ожидалась ошибка с путём к выводу, got %+v", res)
	}
	if res.PID != 0 {
		t.Fatalf("упавший процесс не должен считаться запущенным: %+v", res)
	}
	if output, _ := os.ReadFile(m.StartupLog); !strings.Contains(string(output), "ImportError") {
		t.Fatalf("причина падения не попала в .startup.log: %q", output)
	}
}

// TestManagerFollowsSymlinkedDirectory: каталог бота, заданный через символическую ссылку, совпадает
// с рабочим каталогом процесса, который ядро показывает уже без ссылки.
func TestManagerFollowsSymlinkedDirectory(t *testing.T) {
	m := fakeHeroku(t, sleepingBot)
	res := m.Start()
	if res.Err != nil {
		t.Fatalf("start: %+v", res)
	}
	link := filepath.Join(t.TempDir(), "heroku-link")
	if err := os.Symlink(m.HerokuDir, link); err != nil {
		t.Fatal(err)
	}
	if got := New(link).PID(); got != res.PID {
		t.Fatalf("менеджер по ссылке нашёл %d, want %d", got, res.PID)
	}
}
