// Package termwin открывает новое окно терминала для режима "два окна":
// родная консоль бота должна идти отдельным окном, а не забирать вьюер лога.
package termwin

import (
	"os/exec"
	"runtime"
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
// терминала — что найдётся первым. Сначала три эмулятора с гибкой раскраской
// (ghostty/kitty/alacritty), затем штатные терминалы окружений Linux Mint —
// gnome-terminal (Cinnamon), mate-terminal (MATE), xfce4-terminal (Xfce), —
// у которых почти всегда есть готовый терминал без доустановки чего-либо, а
// цвет фона умеет менять только xfce4-terminal из этой тройки. Последним —
// x-terminal-emulator: alternatives-symlink на дефолтный терминал системы,
// который есть почти на любом Debian/Ubuntu-производном дистрибутиве.
// Возвращает false, если вообще ничего не нашлось.
func Open(title, bg string, cmd []string) bool {
	// bg игнорируется на Windows — ни wt.exe, ни cmd.exe не берут цвет фона
	// из командной строки так же просто, как их Linux-аналоги, а этот путь
	// и так уже best-effort (см. openWindows).
	if runtime.GOOS == "windows" {
		return openWindows(title, cmd)
	}
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
	if _, err := exec.LookPath("xfce4-terminal"); err == nil {
		// -x, а не -e: принимает cmd отдельными argv-элементами, без
		// разбора строки шеллом — так же, как ghostty/alacritty выше.
		args := append([]string{
			"--title=" + title,
			"--color-bg=" + bg, "--color-text=#c9d1d9",
			"--geometry=104x34",
			"-x",
		}, cmd...)
		return start(exec.Command("xfce4-terminal", args...))
	}
	if _, err := exec.LookPath("mate-terminal"); err == nil {
		args := append([]string{"--title", title, "-x"}, cmd...)
		return start(exec.Command("mate-terminal", args...))
	}
	if _, err := exec.LookPath("gnome-terminal"); err == nil {
		// "--" — рекомендованный современным gnome-terminal способ передать
		// команду argv-элементами: то же самое, что "-x" у старых VTE-форков.
		args := append([]string{"--title", title, "--"}, cmd...)
		return start(exec.Command("gnome-terminal", args...))
	}
	if _, err := exec.LookPath("x-terminal-emulator"); err == nil {
		args := append([]string{"-e"}, cmd...)
		return start(exec.Command("x-terminal-emulator", args...))
	}
	return false
}

// openWindows — тот же выбор "что найдётся первым", что и Open на Linux, но
// под здешние эмуляторы: Windows Terminal (wt.exe), если стоит, иначе
// штатный cmd.exe, который есть на любой Windows машине.
func openWindows(title string, cmd []string) bool {
	if _, err := exec.LookPath("wt.exe"); err == nil {
		args := append([]string{"new-tab", "--title", title, "--"}, cmd...)
		return start(exec.Command("wt.exe", args...))
	}
	if _, err := exec.LookPath("cmd.exe"); err == nil {
		args := append([]string{"/C", "start", title}, cmd...)
		return start(exec.Command("cmd.exe", args...))
	}
	return false
}
