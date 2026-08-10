//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// restartWithSwap — на не-Windows selfupdate.Apply уже подменяет себя
// атомарно в момент скачивания (os.Rename поверх открытого работающего
// файла там безопасен), так что RestartApp никогда не находит здесь
// настоящего ожидающего обновления — реализация существует только чтобы
// код был тотальным на всех платформах, а не потому что эта ветка реально
// вызывается.
func restartWithSwap(exe, staged string) error {
	if err := os.Rename(staged, exe); err != nil {
		return err
	}
	return exec.Command(exe).Start()
}
