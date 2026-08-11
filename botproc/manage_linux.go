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
	pids := PIDs()
	if len(pids) == 0 {
		return 1
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for i := 0; i < 50; i++ {
		if !Alive() {
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range PIDs() {
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
	if pid := PID(); pid != 0 {
		return StartResult{PID: pid}
	}

	if _, err := os.Stat(filepath.Join(m.HerokuDir, "venv", "bin", "activate")); err != nil {
		return StartResult{Err: fmt.Errorf("venv не найден: %w", err)}
	}

	out, err := os.Create(m.StartupLog)
	if err != nil {
		return StartResult{Err: err}
	}
	defer out.Close()

	cmd := exec.Command("bash", "-c", "source venv/bin/activate && exec python3 -m heroku")
	cmd.Dir = m.HerokuDir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return StartResult{Err: err}
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release() // не ждём завершения — процесс живёт своей жизнью

	time.Sleep(300 * time.Millisecond) // дать процессу зацепиться за свою группу
	return StartResult{PID: pid}
}
