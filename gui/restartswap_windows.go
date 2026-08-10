//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// restartWithSwap — Windows не даёт подменить (даже переименовать поверх)
// exe, который прямо сейчас выполняется как этот процесс: файл образа
// заблокирован загрузчиком, пока процесс жив. Поэтому подмена идёт отдельным
// detached-процессом, который сначала дожидается, пока этот процесс точно
// завершится (иначе move всё ещё упрётся в блокировку), затем подменяет
// файл и запускает уже новую версию. Секунда паузы — тот же порядок, что и у
// похожих self-update реализаций: обычный процесс успевает выйти по Quit
// заметно быстрее.
func restartWithSwap(exe, staged string) error {
	script := fmt.Sprintf(`timeout /T 1 /NOBREAK >nul & move /Y "%s" "%s" & start "" "%s"`, staged, exe, exe)
	cmd := exec.Command("cmd", "/C", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
