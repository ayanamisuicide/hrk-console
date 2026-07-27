// Package termwin открывает новое окно терминала для режима "два окна":
// родная консоль бота должна идти отдельным окном, а не забирать вьюер лога.
package termwin

import (
	"os/exec"
)

// start запускает окно и снимает с себя обязанность его хоронить: консоль
// живёт часами, а незавершённый Wait оставлял бы процесс эмулятора зомби на
// всё это время.
func start(cmd *exec.Cmd) bool {
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}

// Open запускает cmd (уже полная команда с аргументами) в новом окне
// терминала — что найдётся первым из ghostty/kitty/alacritty. Возвращает
// false, если ни один эмулятор не найден.
func Open(title, bg string, cmd []string) bool {
	if _, err := exec.LookPath("ghostty"); err == nil {
		args := append([]string{
			"--title=" + title,
			"--background=" + bg, "--foreground=#c9d1d9",
			"--font-size=11", "--window-padding-x=10", "--window-padding-y=10",
			"--window-width=104", "--window-height=34",
			"-e",
		}, cmd...)
		return start(exec.Command("ghostty", args...))
	}
	if _, err := exec.LookPath("kitty"); err == nil {
		args := append([]string{
			"--title", title,
			"-o", "background=" + bg, "-o", "foreground=#c9d1d9",
			"-o", "font_size=11", "-o", "window_padding_width=10",
			"-o", "remember_window_size=no",
			"-o", "initial_window_width=104c", "-o", "initial_window_height=34c",
		}, cmd...)
		return start(exec.Command("kitty", args...))
	}
	if _, err := exec.LookPath("alacritty"); err == nil {
		args := append([]string{"--title", title, "-e"}, cmd...)
		return start(exec.Command("alacritty", args...))
	}
	return false
}
