//go:build !linux

package main

import "os/exec"

func configureCommandProcess(_ *exec.Cmd) {}
func killCommandProcessGroup(_ *exec.Cmd) {}
