// Package setup поднимает окружение бота с нуля перед первым запуском:
// клонирует репозиторий, ставит python3, venv и зависимости бота, а также
// эмулятор терминала для режима «два окна» — всё, чего preflight обычно
// просто сообщает об отсутствии. Каждый шаг сперва проверяет, не сделано ли
// уже, и молча выходит, если да, — повторные запуски ничего не переустанавливают.
//
// Работает до входа в Bubble Tea, обычным stdout/stdin: git clone и sudo
// могут попросить пароль или показать прогресс, а внутри alt-screen TUI
// этого показать нельзя.
package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// repoURL — откуда клонировать бота, если каталога ещё нет.
const repoURL = "https://github.com/coddrago/Heroku"

// terminalPkg — какой эмулятор ставить, если для режима «два окна» не
// нашлось ни одного из ghostty/kitty/alacritty. kitty выбран как самый
// предсказуемо доступный пакет в apt-репозиториях Ubuntu/Mint из этой тройки.
const terminalPkg = "kitty"

// EnsureAll поднимает всё окружение бота по порядку: без каталога нет смысла
// ставить venv, без python3 venv не соберётся. Ошибки на одном шаге не
// прерывают остальные — что не получилось, увидит и объяснит preflight,
// который идёт следом.
func EnsureAll(herokuDir string) {
	ensureHerokuDir(herokuDir)
	ensurePython3()
	ensureVenv(herokuDir)
	ensureTerminalEmulator()
}

func ensureHerokuDir(dir string) {
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return
	}
	fmt.Println("• каталог бота не найден — клонирую", repoURL)
	run("git", "clone", repoURL, dir)
}

func ensurePython3() {
	if _, err := exec.LookPath("python3"); err == nil {
		return
	}
	fmt.Println("• python3 не найден — устанавливаю")
	aptInstall("python3", "python3-venv", "python3-pip")
}

func ensureVenv(dir string) {
	activate := filepath.Join(dir, "venv", "bin", "activate")
	if _, err := os.Stat(activate); err == nil {
		return
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return // каталога бота нет — ставить venv некуда, ensureHerokuDir выше уже сказал, в чём дело
	}
	fmt.Println("• venv не найден — создаю")
	if !run("python3", "-m", "venv", filepath.Join(dir, "venv")) {
		return
	}
	req := filepath.Join(dir, "requirements.txt")
	if _, err := os.Stat(req); err != nil {
		return
	}
	fmt.Println("• устанавливаю зависимости бота (requirements.txt)")
	run(filepath.Join(dir, "venv", "bin", "pip"), "install", "-r", req)
}

// ensureTerminalEmulator ставит эмулятор для режима «два окна» заранее, а не
// в момент выбора режима: пользователь просил, чтобы всё нужное было готово
// сразу при запуске, а не всплывало на середине работы с меню.
func ensureTerminalEmulator() {
	for _, name := range []string{"ghostty", "kitty", "alacritty"} {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	fmt.Println("• эмулятор терминала для режима «два окна» не найден — устанавливаю", terminalPkg)
	aptInstall(terminalPkg)
}

// aptInstall ставит пакеты через apt-get -y, без вопросов. На системе не с
// apt (не Ubuntu/Mint-семейство) просто просит поставить руками — угадывать
// пакетный менеджер здесь не нужно, консоль пишется под Mint.
func aptInstall(pkgs ...string) {
	if _, err := exec.LookPath("apt-get"); err != nil {
		fmt.Println("  apt-get недоступен — установите вручную:", strings.Join(pkgs, " "))
		return
	}
	run("sudo", append([]string{"apt-get", "install", "-y"}, pkgs...)...)
}

// run выполняет команду, унаследовав stdio текущего терминала — sudo должен
// иметь возможность спросить пароль, git clone показать прогресс.
func run(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("  ошибка:", err)
		return false
	}
	return true
}
