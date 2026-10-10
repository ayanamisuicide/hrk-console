//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// configureCommandProcess создаёт отдельную группу процесса для завершения команды вместе с её потомками.
func configureCommandProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killCommandProcessGroup завершает всю группу команды при отмене или таймауте.
func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
