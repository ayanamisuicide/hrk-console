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
	"time"

	"heroku-console/internal/theme"
)

// repoURL — откуда клонировать бота, если каталога ещё нет.
const repoURL = "https://github.com/coddrago/Heroku"

// terminalPkg — какой эмулятор ставить, если для режима «два окна» вообще
// ничего не нашлось. kitty выбран как самый предсказуемо доступный пакет в
// apt-репозиториях Ubuntu/Mint среди эмуляторов с гибкой раскраской.
const terminalPkg = "kitty"

// knownTerminals — то же самое, что умеет открывать internal/termwin: три
// эмулятора с гибкой раскраской и штатные терминалы окружений Mint
// (gnome-terminal у Cinnamon, mate-terminal у MATE, xfce4-terminal у Xfce),
// плюс общий alternatives-symlink. Если хоть один уже стоит — ставить нечего,
// termwin сам найдёт и воспользуется тем, что есть.
var knownTerminals = []string{
	"ghostty", "kitty", "alacritty",
	"xfce4-terminal", "mate-terminal", "gnome-terminal", "x-terminal-emulator",
}

// EnsureAll поднимает всё окружение бота по порядку: без каталога нет смысла
// ставить venv, без python3 venv не соберётся. Ошибки на одном шаге не
// прерывают остальные — что не получилось, увидит и объяснит preflight,
// который идёт следом.
//
// Заставка появляется, только если реально есть что делать: молчаливый
// повторный запуск не должен ничем моргать на экране.
func EnsureAll(herokuDir string) {
	pending := needHerokuDir(herokuDir) || needPython3() || needVenvModule() ||
		needVenv(herokuDir) || needTerminal() || needFFmpeg() ||
		len(MissingModuleDeps(herokuDir)) > 0
	if !pending {
		return
	}
	banner()
	ensureHerokuDir(herokuDir)
	ensurePython3()
	ensureVenvModule()
	ensureVenv(herokuDir)
	ensureTerminalEmulator()
	ensureFFmpeg()
	// Зависимости модулей идут последними: venv к этому моменту уже создан,
	// а сам разбор импортов запускается его питоном. Кэш разбора сбрасываем —
	// его могли посчитать до создания venv, когда проверять было нечем.
	invalidateDepCache()
	ensureModuleDeps(herokuDir)
	fmt.Println()
}

// banner — короткая заставка перед автонастройкой: печатается посимвольно,
// чтобы момент, когда программа явно что-то ставит в систему, был заметен, а
// не проскакивал безликой строкой лога среди вывода apt/git/pip.
func banner() {
	fmt.Println()
	fmt.Println(theme.Gradient("─", 42, false))
	title := "  АВТОНАСТРОЙКА ОКРУЖЕНИЯ"
	for _, r := range title {
		fmt.Print(theme.Title.Render(string(r)))
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Println()
	fmt.Println(theme.Faint.Render("  ставлю то, чего не хватает — дальше пойдёт само"))
	fmt.Println(theme.Gradient("─", 42, false))
	fmt.Println()
}

// step печатает цветной заголовок шага перед его выполнением — акцентный
// цвет темы, а не безликая точка, чтобы момент, где программа лезет в
// систему (apt-get, git clone), не терялся в потоке вывода этих же команд.
func step(msg string) {
	fmt.Println(theme.Title.Render("›") + " " + msg)
}

// result печатает исход шага тем же языком, что и весь остальной интерфейс:
// зелёная заливка на успех, красная на провал — те же стили, что у "● live"
// и "○ не запущен" в шапке вьюера.
func result(ok bool, okMsg, failMsg string) {
	if ok {
		fmt.Println(theme.StatusLive.Render("  ✓ " + okMsg))
	} else {
		fmt.Println(theme.StatusDown.Render("  ✗ " + failMsg))
	}
}

func needHerokuDir(dir string) bool {
	info, err := os.Stat(dir)
	return err != nil || !info.IsDir()
}

func ensureHerokuDir(dir string) {
	if !needHerokuDir(dir) {
		return
	}
	step("каталог бота не найден — клонирую " + repoURL)
	ok := run("git", "clone", repoURL, dir)
	result(ok, "готово: "+dir, "клонировать не удалось — см. вывод выше")
}

func needPython3() bool {
	_, err := exec.LookPath("python3")
	return err != nil
}

func ensurePython3() {
	if !needPython3() {
		return
	}
	step("python3 не найден — устанавливаю")
	ok := aptInstall("python3", "python3-venv", "python3-pip")
	result(ok, "python3 установлен", "установка не удалась — см. вывод выше")
}

// needVenvModule отвечает, может ли python3 вообще создать venv. На
// Debian/Ubuntu-семействе модуль venv (через ensurepip) ставится отдельным
// пакетом python3-venv, который не тянется вместе с самим python3 —
// системы часто приходят с python3, но без него. needPython3() эту нехватку
// не ловит: бинарник на месте, а ensureVenv молча падает на пустом месте.
func needVenvModule() bool {
	if needPython3() {
		return false // python3 не найден — про venv-модуль пока рано
	}
	return exec.Command("python3", "-c", "import ensurepip").Run() != nil
}

func ensureVenvModule() {
	if !needVenvModule() {
		return
	}
	step("модуль venv (ensurepip) не найден — устанавливаю python3-venv")
	ok := aptInstall("python3-venv")
	result(ok, "python3-venv установлен", "установка не удалась — см. вывод выше")
}

func needVenv(dir string) bool {
	if needHerokuDir(dir) {
		return false // каталога бота нет — ставить venv пока некуда
	}
	_, err := os.Stat(filepath.Join(dir, "venv", "bin", "activate"))
	return err != nil
}

func ensureVenv(dir string) {
	if !needVenv(dir) {
		return
	}
	step("venv не найден — создаю")
	if !run("python3", "-m", "venv", filepath.Join(dir, "venv")) {
		result(false, "", "не удалось создать venv — см. вывод выше")
		return
	}
	result(true, "venv создан", "")

	req := filepath.Join(dir, "requirements.txt")
	if _, err := os.Stat(req); err != nil {
		return
	}
	step("устанавливаю зависимости бота (requirements.txt)")
	ok := run(filepath.Join(dir, "venv", "bin", "pip"), "install", "-r", req)
	result(ok, "зависимости установлены", "pip install не удался — см. вывод выше")
}

// needTerminal отвечает, нужен ли вообще эмулятор терминала для режима «два
// окна» — заранее, тем же списком, что и internal/termwin умеет открывать.
func needTerminal() bool {
	for _, name := range knownTerminals {
		if _, err := exec.LookPath(name); err == nil {
			return false
		}
	}
	return true
}

// ensureTerminalEmulator ставит эмулятор для режима «два окна» заранее, а не
// в момент выбора режима: пользователь просил, чтобы всё нужное было готово
// сразу при запуске, а не всплывало на середине работы с меню.
func ensureTerminalEmulator() {
	if !needTerminal() {
		return
	}
	step("эмулятор терминала для режима «два окна» не найден — устанавливаю " + terminalPkg)
	ok := aptInstall(terminalPkg)
	result(ok, terminalPkg+" установлен", "установка не удалась — см. вывод выше")
}

// aptInstall ставит пакеты через apt-get -y, без вопросов. На системе не с
// apt (не Ubuntu/Mint-семейство) просто просит поставить руками — угадывать
// пакетный менеджер здесь не нужно, консоль пишется под Mint.
func aptInstall(pkgs ...string) bool {
	if _, err := exec.LookPath("apt-get"); err != nil {
		fmt.Println(theme.Faint.Render("  apt-get недоступен — установите вручную: " + strings.Join(pkgs, " ")))
		return false
	}
	return run("sudo", append([]string{"apt-get", "install", "-y"}, pkgs...)...)
}

// run выполняет команду, унаследовав stdio текущего терминала — sudo должен
// иметь возможность спросить пароль, git clone показать прогресс.
func run(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println(theme.StatusDown.Render("  ошибка: " + err.Error()))
		return false
	}
	return true
}
