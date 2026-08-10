//go:build linux

package botproc

import (
	"os"
	"os/exec"
	"syscall"
)

// RunForeground запускает бота прямо в текущем терминале, унаследовав его
// stdio — аналог bot-console.sh. Бот получает Setpgid (свою группу
// процессов), а не Setsid: если он получит .restart из Telegram и сделает
// killpg по своей группе (heroku/_internal.py: die()), это не заденет наш
// процесс — мы в другой группе, хотя и делим терминал.
//
// Возвращает код выхода процесса бота (или -1, если не удалось запустить).
func (m *Manager) RunForeground() int {
	cmd := exec.Command("bash", "-c", "source venv/bin/activate && exec python3 -m heroku")
	cmd.Dir = m.HerokuDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return -1
	}
	return 0
}
