// Команда hkc — консоль Heroku-юзербота: меню запуска и живой просмотр
// логов. Переписано на Go + Bubble Tea/Lip Gloss с прежней bash-версии;
// вся построчная арифметика курсора (tput csr/cup, ручная пересборка
// экрана при ресайзе) заменена настоящим TUI-фреймворком — прокрутка,
// перенос строк и обработка ресайза теперь не самодельные.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"heroku-console/internal/botproc"
	"heroku-console/internal/logfeed"
	"heroku-console/internal/termwin"
	"heroku-console/internal/tui"
)

// version подставляется линкером из Makefile (`-X main.version=...`),
// значение по умолчанию остаётся у сборок через голый `go build`.
var version = "dev"

const usage = `hkc — консоль Heroku-юзербота.

  hkc              проверки, затем меню выбора режима
  hkc 1|2|3|4      сразу нужный режим, без меню
  hkc console      родная консоль бота в текущем терминале

  --no-check       пропустить проверки окружения
  --version        показать версию
  --help           эта справка

Переменные окружения: HEROKU_DIR — каталог бота, LOG_FILE — файл лога.`

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func dropFlag(args []string, flag string) []string {
	out := args[:0:0]
	for _, a := range args {
		if a != flag {
			out = append(out, a)
		}
	}
	return out
}

func herokuDir() string {
	if d := os.Getenv("HEROKU_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Heroku")
}

func main() {
	args := os.Args[1:]

	// Раньше всего прочего: и версия, и справка должны отвечать даже
	// там, где нет терминала — из скрипта, по ssh, в пакетном сборщике.
	// До этой проверки любой аргумент проваливался в TUI и упирался в
	// «could not open a new TTY».
	if hasFlag(args, "--version") || hasFlag(args, "-v") {
		fmt.Println("hkc", version)
		return
	}
	if hasFlag(args, "--help") || hasFlag(args, "-h") {
		fmt.Println(usage)
		return
	}

	bot := botproc.New(herokuDir())

	if len(args) == 1 && args[0] == "console" {
		os.Exit(bot.RunForeground())
	}

	// Проверки идут до всего остального: они отвечают на "почему не
	// работает" раньше, чем пользователь упрётся в пустой экран лога.
	// Пропускаются флагом --no-check, чтобы не мешать, когда консоль
	// дёргают из скрипта.
	if !hasFlag(args, "--no-check") {
		pf := tui.NewPreflight(bot.HerokuDir, bot.LogFile)
		p := tea.NewProgram(pf, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка проверок:", err)
			os.Exit(1)
		}
		// Ctrl+C и q на экране проверок означают "выйти", а не "пропустить
		// проверки": раньше программа шла дальше в меню, и прервать её на
		// этом экране было нельзя вообще.
		if pf.Aborted {
			return
		}
	}
	args = dropFlag(args, "--no-check")

	choice := 0
	if len(args) == 1 {
		switch args[0] {
		case "1", "2", "3", "4":
			choice = int(args[0][0] - '0')
		}
	}

	if choice == 0 {
		m := tui.NewMenu(bot)
		p := tea.NewProgram(m, tea.WithAltScreen())
		res, err := p.Run()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка меню:", err)
			os.Exit(1)
		}
		choice = res.(*tui.Menu).Choice
		if choice == 0 {
			return // вышли по q
		}
	}

	switch choice {
	case 1:
		modeAttach(bot)
	case 2:
		modeRestart(bot)
	case 3:
		modeTwoWindows(bot)
	case 4:
		modeStop(bot)
	}
}

func pressAnyKey() {
	fmt.Print("\nНажмите любую клавишу для выхода...")
	var b [1]byte
	os.Stdin.Read(b[:])
}

// ensureRunning поднимает бота, если не запущен, и возвращает строку файла,
// с которой должен начаться живой просмотр (чтобы не показывать всю
// историю заново).
func ensureRunning(bot *botproc.Manager) (logFrom int, ok bool) {
	logFrom = logfeed.LineCount(bot.LogFile) + 1
	fmt.Println("Запускаю Heroku…")
	res := bot.Start()

	if res.AlreadyStarting {
		fmt.Println("запуск уже идёт в другом окне — жду")
		for i := 0; i < 40; i++ {
			if botproc.Alive() {
				fmt.Println("pid", botproc.PID())
				return logFrom, true
			}
			time.Sleep(250 * time.Millisecond)
		}
		fmt.Println("не дождался — попробуйте ещё раз")
		return 0, false
	}
	if res.Err != nil || res.PID == 0 {
		fmt.Println("не удалось запустить:", res.Err)
		return 0, false
	}

	time.Sleep(2 * time.Second)
	if botproc.Alive() {
		fmt.Println("pid", res.PID)
		return logFrom, true
	}
	fmt.Println("процесс не поднялся")
	return 0, false
}

func runViewer(bot *botproc.Manager, opts tui.ViewerOpts) {
	opts.Bot = bot
	v := tui.NewViewer(opts)
	p := tea.NewProgram(v, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка вьюера:", err)
		os.Exit(1)
	}
}

func modeAttach(bot *botproc.Manager) {
	if botproc.Alive() {
		runViewer(bot, tui.ViewerOpts{History: 40, ShowDebug: false})
		return
	}
	logFrom, ok := ensureRunning(bot)
	if !ok {
		pressAnyKey()
		os.Exit(1)
	}
	time.Sleep(400 * time.Millisecond) // дать прочитать итог запуска
	runViewer(bot, tui.ViewerOpts{From: logFrom, SkipBoot: true})
}

func modeRestart(bot *botproc.Manager) {
	if botproc.Alive() {
		fmt.Println("Останавливаю старый процесс…")
		bot.Stop()
	}
	logFrom, ok := ensureRunning(bot)
	if !ok {
		pressAnyKey()
		os.Exit(1)
	}
	time.Sleep(400 * time.Millisecond)
	runViewer(bot, tui.ViewerOpts{From: logFrom, SkipBoot: true})
}

func modeStop(bot *botproc.Manager) {
	if botproc.Alive() {
		fmt.Println("Останавливаю…")
		switch bot.Stop() {
		case 0:
			fmt.Println("остановлен")
		case 2:
			fmt.Println("принудительно остановлен")
		}
	} else {
		fmt.Println("не запущен")
	}
	pressAnyKey()
}

func modeTwoWindows(bot *botproc.Manager) {
	if botproc.Alive() {
		fmt.Println("Останавливаю старый процесс…")
		bot.Stop()
	}
	logFrom := logfeed.LineCount(bot.LogFile) + 1

	self, err := os.Executable()
	if err != nil {
		self = "hkc"
	}
	if !termwin.Open("Heroku · Консоль", "#12161f", []string{self, "console"}) {
		fmt.Println("терминал не найден — запускаю здесь")
		os.Exit(bot.RunForeground())
	}

	for i := 0; i < 60; i++ {
		if botproc.Alive() {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	runViewer(bot, tui.ViewerOpts{From: logFrom, SkipBoot: true})
}
