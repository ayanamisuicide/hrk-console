//go:build linux

package botproc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Stop останавливает бота: сперва SIGTERM и до 5 секунд ожидания, затем
// SIGKILL, если не помогло. Возврат: 0 — остановлен штатно, 2 — пришлось
// убивать, 1 — не был запущен.
func (m *Manager) Stop() int {
	pids := m.PIDs()
	if len(pids) == 0 {
		return 1
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for i := 0; i < 50; i++ {
		if !m.Alive() {
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range m.PIDs() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	time.Sleep(500 * time.Millisecond)
	return 2
}

// Start поднимает бота в своей сессии (аналог setsid), отвязанным от
// текущего терминала: если бот получит .restart из Telegram и сделает
// killpg по своей группе процессов, это не заденет консоль. flock на
// LockFile не даёт двум параллельным стартам поднять два процесса разом.
func (m *Manager) Start() StartResult {
	lock, err := os.OpenFile(m.LockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return StartResult{Err: err}
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return StartResult{AlreadyStarting: true}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	// Проверка живости именно здесь, под локом: вызывающий проверял её до
	// захвата, и два окна, стартовавшие одновременно, успевали поднять двух
	// ботов — лок лишь выстраивал их в очередь, а не отменял второй запуск.
	if pid := m.PID(); pid != 0 {
		return StartResult{PID: pid}
	}

	venv := m.VirtualEnv()
	if venv == "" {
		return StartResult{Err: fmt.Errorf("виртуальное окружение .venv или venv не найдено")}
	}
	out, err := os.Create(m.StartupLog)
	if err != nil {
		return StartResult{Err: err}
	}
	defer out.Close()
	if err := m.InstallModulesBridge(); err != nil {
		fmt.Fprintf(out, "Мониторинг модулей не подключён: %v\n", err)
	}

	cmd := exec.Command(filepath.Join(venv, "bin", "python3"), "-m", "heroku", "--root")
	cmd.Dir = m.HerokuDir
	cmd.Env = append(os.Environ(), "VIRTUAL_ENV="+venv, "PATH="+filepath.Join(venv, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return StartResult{Err: err}
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release() // не ждём завершения — процесс живёт своей жизнью

	time.Sleep(300 * time.Millisecond) // дать процессу зацепиться за свою группу
	if !m.AliveAt(pid) {
		return StartResult{Err: fmt.Errorf("процесс завершился сразу после запуска; проверьте %s", m.StartupLog)}
	}
	return StartResult{PID: pid}
}
