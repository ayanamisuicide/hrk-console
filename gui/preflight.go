package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"heroku-console/preflight"
	"heroku-console/remotebot"
	"heroku-console/termwin"
)

// preflightCheckDwell — минимум, который проверка держится на экране, тот
// же, что у TUI (internal/tui/preflight.go): проверки окружения отрабатывают
// за доли миллисекунды, и без выдержки список сменился бы внутри одного
// кадра фронтенда — смотреть было бы не на что.
const preflightCheckDwell = 260 * time.Millisecond

// PreflightEvent — одна проверка окружения, отданная фронтенду по мере
// прогона. Та же информация, что в preflight.Check, но в JSON-пригодном виде
// и с индексом — фронтенд не пересчитывает порядок сам, просто раскладывает
// события по местам.
type PreflightEvent struct {
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Name   string `json:"name"`
	Status string `json:"status"` // passed | failed | skipped
	Detail string `json:"detail"`
	TookMs int64  `json:"tookMs"`
}

// runChecklist гоняет уже готовый список проверок последовательно, не
// параллельно — прогон должен читаться как цепочка шагов, а не список,
// обновившийся целиком разом, тот же контракт, что и в
// internal/tui/preflight.go. Шлёт результат каждой отдельным событием, а не
// одним ответом в конце: и "модульные тесты" локально, и SSH-команды
// удалённо могут идти секундами, окно не должно молчать всё это время.
// Общая для локального (preflight.All) и удалённого (remotebot.Client.
// Preflight) списков — сам прогон и отправка событий не знают разницы
// между ними.
func (a *App) runChecklist(checks []preflight.Check) {
	for i, c := range checks {
		start := time.Now()
		detail, status := c.Run()
		if rest := preflightCheckDwell - time.Since(start); rest > 0 {
			time.Sleep(rest)
		}
		a.emit("preflight-check", PreflightEvent{
			Index: i, Total: len(checks), Name: c.Name,
			Status: preflightStatusString(status), Detail: detail,
			TookMs: time.Since(start).Milliseconds(),
		})
	}
}

// runPreflight — та же runChecklist, но идёт в фоне параллельно с
// loadHistory/followLog/statusLoop, а не до них: сами проверки лишь
// показывают состояние окружения, они не blocking-условие для того, чтобы
// начать читать уже существующий heroku.log.
func (a *App) runPreflight() {
	a.runChecklist(a.localChecks())
}

// localChecks — проверки локального окружения, либо честная заглушка
// "локальный режим не поддерживается" на ОС, где botproc умеет только
// притворяться (macOS, Windows — см. botproc/manage_other.go): гонять там
// venv/ffmpeg/права-на-запись под несуществующий Linux-каталог бота бессмысленно,
// единственный рабочий путь — SSH-подключение (remotebot).
func (a *App) localChecks() []preflight.Check {
	if runtime.GOOS != "linux" {
		return preflight.Unsupported()
	}
	return preflight.All(a.bot.HerokuDir, a.bot.LogFile)
}

// PreflightChecks — только имена проверок, без запуска. Фронтенду нужно
// нарисовать полный список строк ДО того, как первая проверка успеет
// отработать (события 'preflight-check' начинают идти сразу при старте
// окна) — иначе экран открывался бы пустым и достраивался бы по одной
// строке снизу, а не показывал сразу, сколько шагов всего и какие.
//
// В удалённом режиме список строится через (&remotebot.Client{}).Preflight()
// — пустым, ещё не подключённым клиентом: сами имена проверок не зависят
// от того, есть ли уже живое SSH-соединение, а вот дождаться его здесь
// было бы неправильно — a.ui.Remote выставляется синхронно в начале
// startup(), а a.remote только после успешного подключения (startRemote —
// фоновая горутина), и ожидание одного из другого раньше иногда вешало
// экран проверок навсегда, если фронтенд успевал спросить раньше.
func (a *App) PreflightChecks() []string {
	var checks []preflight.Check
	if a.ui.Remote.Host != "" {
		checks = (&remotebot.Client{}).Preflight()
	} else {
		checks = a.localChecks()
	}
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return names
}

// FixEnvironment открывает настоящий терминал и гоняет в нём то же самое
// setup.EnsureAll, что уже год чинит окружение hkc (python3, venv, ffmpeg,
// зависимости модулей) — просто раньше это умел только TUI. У GUI нет
// собственного терминала, а часть шагов может попросить пароль sudo, и у
// молчаливого вызова из webview этот пароль было бы решительно негде
// набрать — окно просто зависло бы навсегда на невидимом запросе. Вместо
// этого: тот же бинарник GUI, запущенный ещё раз со скрытым флагом
// setupOnlyFlag (main.go), в окне termwin.Open — том же механизме, что уже
// открывает родную консоль бота в режиме "два окна" у TUI. bash -c с read в
// конце держит окно открытым после того, как настройка закончится: иначе
// многие эмуляторы закрывают окно тем же кадром, что и exit самого процесса,
// и результат никто не успел бы прочитать.
//
// Не заменяет пункт «продолжить всё равно» на экране проверок — это
// отдельное действие, которое пользователь запускает сам, когда видит, что
// что-то не в порядке, а не автоматический шаг при каждом провале проверки.
func (a *App) FixEnvironment() ActionResult {
	if runtime.GOOS != "linux" {
		return ActionResult{OK: false, Message: "бот работает только на Linux — подключитесь к удалённой машине по SSH вместо локальной настройки"}
	}
	exe, err := os.Executable()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	cmd := []string{"bash", "-c",
		fmt.Sprintf("'%s' %s; echo; read -p 'нажмите Enter, чтобы закрыть…' _", exe, setupOnlyFlag)}
	if !termwin.Open("Heroku · Настройка окружения", "#171129", cmd) {
		return ActionResult{OK: false, Message: "эмулятор терминала не найден"}
	}
	return ActionResult{OK: true, Message: "окно настройки открыто"}
}

func preflightStatusString(s preflight.Status) string {
	switch s {
	case preflight.Passed:
		return "passed"
	case preflight.Failed:
		return "failed"
	case preflight.Skipped:
		return "skipped"
	default:
		return "passed"
	}
}
