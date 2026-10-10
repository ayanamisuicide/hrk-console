//go:build !linux

package main

import "os/exec"

// configureCommandProcess оставляет команду без Linux-настроек группы; терминал на этой платформе
// недоступен.
func configureCommandProcess(_ *exec.Cmd) {}

// killCommandProcessGroup не применяет Linux-сигналы на неподдерживаемой платформе.
func killCommandProcessGroup(_ *exec.Cmd) {}
