//go:build !unix

package tui

import (
	"os"
	"os/exec"
)

// RestartSelf — запасной вариант для систем без syscall.Exec (практически
// только Windows). Сам hkc собирается и выпускается лишь под Linux (см.
// release.yml), так что до этой ветки доходит разве что `go build ./...` на
// машине разработчика; она существует, чтобы пакет продолжал собираться
// везде, а не ради работающего там перезапуска.
//
// Здесь неизбежен прежний приём — породить новый процесс и завершить
// текущий, — со всеми его недостатками для терминала (см. подробности в
// restart_unix.go). Тот же приём (без недостатков — GUI не держит
// терминал) использует gui/app.go:App.RestartApp в своей ветке без
// staged-подмены; правка одного места, вероятно, касается и другого.
func RestartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil // недостижимо, но компилятору нужен возврат
}
