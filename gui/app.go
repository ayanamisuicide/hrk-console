package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"heroku-console/botproc"
	"heroku-console/logfeed"
	"heroku-console/remotebot"
	"heroku-console/state"
)

// version подставляется линкером из build/config или остаётся "dev" для
// go run/wails dev — тот же трюк, что и у hkc.
var version = "dev"

// ringCapacity — сколько сырых строк лога держим для пересборки при смене
// фильтра/порога/DEBUG. То же число, что и в TUI-вьюере: история должна
// оставаться доступной поиску, а не только тому, что уже нарисовано.
const ringCapacity = 5000

// watchdogCooldown — не чаще какого интервала вотчдог пробует поднять бота
// заново, чтобы падение сразу после старта не превращалось в цикл рестартов.
const watchdogCooldown = 5 * time.Second

// Status — то, что видно в шапке и панели: живость бота, версии, вотчдог.
//
// StreamIssue и StreamIssueFor разделены, а не склеены в одну строку:
// причина и «как давно» — разные по важности вещи, и рисуются они по-разному.
//
// RemoteState — состояние SSH-подключения, когда бот на другой машине:
// "" (локальный бот), "connecting", "online", "offline". Живость самого
// бота (Alive) к нему отношения не имеет: подключение может работать
// прекрасно, а бот на той стороне быть не запущен.
type Status struct {
	Alive           bool   `json:"alive"`
	PID             int    `json:"pid"`
	Uptime          string `json:"uptime"`
	BotVersion      string `json:"botVersion"`
	HkcVersion      string `json:"hkcVersion"`
	Watchdog        bool   `json:"watchdog"`
	Restarting      bool   `json:"restarting"`
	StreamIssue     string `json:"streamIssue"`
	StreamIssueFor  string `json:"streamIssueFor"`
	RemoteState     string `json:"remoteState"`
	RemoteStateNote string `json:"remoteStateNote"`
}

// Rec — запись лога в виде, пригодном для JSON: то же самое, что
// logfeed.Record, но без непубличного fullModule (фильтрация по нему уже
// сделана на бэкенде, фронтенду он не нужен).
type Rec struct {
	Date   string   `json:"date"`
	Time   string   `json:"time"`
	Level  int      `json:"level"`
	Module string   `json:"module"`
	Lines  []string `json:"lines"`
	Hard   []bool   `json:"hard"`
	Warn   bool     `json:"warn"`
	Err    bool     `json:"err"`
	Count  int      `json:"count"`
}

// TailEvent — событие для одной новой строки живого лога. Replace=true
// значит "эта запись — повтор предыдущей, обнови последнюю показанную
// строку счётчиком", а не "допиши новую" — то же правило схлопывания,
// что и в TUI (logfeed.SameEntry), просто на стороне бэкенда.
type TailEvent struct {
	Replace bool `json:"replace"`
	Rec     Rec  `json:"rec"`
}

// Bootstrap — всё, что нужно фронтенду для первого рендера: статус,
// сохранённые настройки экрана и уже отфильтрованная история.
//
// Platform — runtime.GOOS. Блок «Подключение» существует ради Windows, где
// бота локально нет и быть не может; на машине, где бот и так стоит рядом,
// он только занимает место в панели, и фронтенд прячет его — но знать, где
// он запущен, может только бэкенд.
type Bootstrap struct {
	Status   Status      `json:"status"`
	UIState  state.State `json:"uiState"`
	Records  []Rec       `json:"records"`
	Platform string      `json:"platform"`
}

type ActionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// UpdateInfo — "вышла версия новее той, что запущена".
type UpdateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// UpdateCheckResult — ответ на CheckForUpdate: не только "нашлась новее
// версия", но и "проверили — уже последняя", и "проверить не вышло". OK
// значит "запрос отработал", а не "версия новая" — Available разводит эти
// два вопроса, иначе фронтенду было бы не отличить "сети нет" от "и так
// последняя".
type UpdateCheckResult struct {
	OK        bool   `json:"ok"`
	Available bool   `json:"available"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Message   string `json:"message"`
	// Channel — с каким каналом сверялись: фронтенд показывает бейдж и
	// переключатель канала одним и тем же ответом, не гадая, к какому из
	// двух опросов он относится, если оба случились почти одновременно.
	Channel string `json:"channel"`
}

// App — состояние бэкенда. Один экземпляр на окно, живёт всё время работы
// приложения. mu защищает ring/parser/visible/ui/filter — всё, что читает
// и живой хвост лога (feedLine), и обработчики настроек с фронтенда
// (SetFilter и подобные), поэтому оба пути идут только через него.
// watchMu — отдельный, более узкий замок вокруг вотчдога: он не должен
// ждать пересборки лога, чтобы не задерживать перезапуск бота.
//
// Методы App разложены по файлам одного пакета main по назначению, а не
// все в этом файле: remote.go (SSH-режим), preflight.go (проверки
// окружения), update.go (самообновление), logstatus.go (лог/статус/
// вотчдог), actions.go (команды с фронтенда). Здесь — только сам тип,
// жизненный цикл (startup/shutdown) и то, что нужно им всем.
type App struct {
	ctx context.Context
	bot *botproc.Manager

	// remote — не nil, когда бот управляется по SSH на другой машине (см.
	// пакет remotebot и state.Remote), а не через локальный a.bot. follower
	// и remoteFollower взаимоисключающи по той же причине: ровно один из
	// двух источников лога активен за раз, второй остаётся nil.
	remote         *remotebot.Client
	remoteFollower *remotebot.FollowHandle

	// remoteState/remoteStateNote — что показать в блоке «Подключение»
	// (см. Status.RemoteState). Под a.mu, как и остальное, что читает
	// statusLocked.
	remoteState     string
	remoteStateNote string

	mu      sync.Mutex
	ring    []string
	parser  *logfeed.Parser
	visible []*logfeed.Record
	ui      state.State
	filter  string // не persisted — как и в TUI, поиск не переживает перезапуск

	follower *logfeed.Follower

	watchMu            sync.Mutex
	restarting         bool
	lastRestartAttempt time.Time
	// everAlive — тик хоть раз застал бота живым. Вотчдог обязан лечить
	// только падение уже бежавшего бота: без этого флага он на первом же
	// тике после открытия окна видит "не жив" (бот просто ещё не запущен)
	// и радостно рапортует "не отвечает — перезапускаю" тому, что никогда
	// не запускалось.
	everAlive bool

	// lastPID — pid бота с прошлого тика, под watchMu. tickPID подтверждает
	// его дешёвой проверкой (AliveAt) вместо полного обхода /proc на каждой
	// секунде — см. tickPID.
	lastPID int

	tray         *Tray
	trayMu       sync.Mutex
	windowHidden bool // под trayMu — переключается только из колбэка трея

	// remotePID/remotePIDAt — последний pid, который удалённая машина
	// действительно назвала, и когда. Нужны только в удалённом режиме, см.
	// stalePID.
	remotePID   int
	remotePIDAt time.Time

	// Точки подмены. Всё, чем бэкенд трогает внешний мир, — поиск процесса,
	// запуск и остановка бота, его версия и аптайм, отправка события в
	// окно — проходит через эти поля. Изначально (см. NewApp) смотрят на
	// локальный botproc; startRemote переставляет их на remotebot.Client,
	// если в настройках указан удалённый хост — дальше statusLoop, вотчдог
	// и StartBot/StopBot/RestartBot работают одинаково в обоих случаях, не
	// зная, локальный бот или нет. Без этой подмены логику вотчдога (кто,
	// когда и на каком основании решает перезапускать) нельзя было бы
	// проверить, не подняв настоящего бота в настоящем окне Wails; ровно
	// поэтому ложное срабатывание из 1.6.1 и доехало до релиза.
	pid        func() int
	aliveAt    func(pid int) bool
	start      func() startResult
	stop       func() (int, error)
	uptime     func(pid int) string
	botVersion func() string
	emit       func(event string, data ...interface{})
}

// startResult — тот же контракт, что у botproc.StartResult и
// remotebot.StartResult, но свой: App не должен знать, из какого из двух
// пакетов он на самом деле пришёл.
type startResult struct {
	PID             int
	AlreadyStarting bool
	Err             error
}

// herokuDir — тот же HEROKU_DIR/~/Heroku, что и у hkc (cmd/hkc/main.go).
// Отдельная функция, а не только инлайн в NewApp: runSetupOnly (main.go)
// должен резолвить тот же каталог, не поднимая App целиком.
func herokuDir() string {
	if dir := os.Getenv("HEROKU_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Heroku")
}

func NewApp() *App {
	dir := herokuDir()
	a := &App{bot: botproc.New(dir)}
	a.pid = botproc.PID
	a.aliveAt = botproc.AliveAt
	a.start = func() startResult {
		r := a.bot.Start()
		return startResult{PID: r.PID, AlreadyStarting: r.AlreadyStarting, Err: r.Err}
	}
	a.stop = func() (int, error) { return a.bot.Stop(), nil }
	a.uptime = botproc.Uptime
	a.botVersion = a.bot.Version
	// ctx читается в момент вызова, а не сейчас: на этапе NewApp окна ещё
	// нет, его подставит startup.
	a.emit = func(event string, data ...interface{}) {
		wailsrt.EventsEmit(a.ctx, event, data...)
	}
	return a
}

// alive — единственный источник правды о живости бота для разовых ручных
// действий (RestartBot и подобные), где нет смысла кэшировать pid между
// вызовами — в отличие от statusLoop, тут нет тика раз в секунду. Сам тик
// использует tickPID, дешевле.
func (a *App) alive() bool { return a.pid() != 0 }

// tickPID — pid бота для очередного тика statusLoop. Пока бот жив на том
// же pid, что и на прошлом тике, подтверждает это одним чтением
// /proc/<pid>/cmdline (aliveAt) вместо полного обхода /proc (a.pid) —
// раньше он делался дважды за тик (statusLocked и maybeWatchdogRestart
// спрашивали независимо), теперь не делается вовсе, пока бот стабильно
// жив. Полный обход остаётся источником истины при первом тике и сразу
// после падения или перезапуска бота, когда lastPID не подтверждается.
func (a *App) tickPID() int {
	a.watchMu.Lock()
	last := a.lastPID
	a.watchMu.Unlock()

	if last != 0 && a.aliveAt(last) {
		return last
	}

	pid := a.pid()
	a.watchMu.Lock()
	a.lastPID = pid
	a.watchMu.Unlock()
	return pid
}

// remoteStaleWindow — сколько последний достоверный pid считается ещё
// пригодным, когда удалённая машина перестала отвечать. Секунды заминки
// пережить надо, минуты — уже нет: иначе окно бесконечно показывало бы
// «live» давно оборванного соединения.
const remoteStaleWindow = 15 * time.Second

func (a *App) rememberPID(pid int) {
	a.watchMu.Lock()
	a.remotePID, a.remotePIDAt = pid, time.Now()
	a.watchMu.Unlock()
}

func (a *App) stalePID() int {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	if a.remotePID == 0 || time.Since(a.remotePIDAt) > remoteStaleWindow {
		return 0
	}
	return a.remotePID
}

func (a *App) setRemoteState(st, note string) {
	a.mu.Lock()
	a.remoteState, a.remoteStateNote = st, note
	a.mu.Unlock()
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.ui = state.Load()
	a.parser = logfeed.NewParser()

	if a.healPendingUpdate() {
		return
	}

	if a.ui.Remote.Host != "" {
		a.setRemoteState("connecting", "подключаюсь…")
		go a.startRemote()
	} else {
		a.loadHistory()
		go a.followLog()
		go a.runPreflight()
	}
	go a.statusLoop()
	// Отдельной проверки обновлений при старте здесь больше нет: CheckChannels
	// с фронтенда (экран обновлений после проверок окружения) и так узнаёт
	// оба канала, и её ответа хватает, чтобы выставить бейдж в шапке. Два
	// запроса ради одного и того же ответа выбирали неаутентифицированный
	// лимит GitHub (60 в час на IP) за пару десятков перезапусков окна, после
	// чего API отвечал 403 на всё подряд — включая само обновление.

	a.tray = &Tray{}
	go a.tray.Start(a.toggleWindow, func() { wailsrt.Quit(a.ctx) })
}

// toggleWindow — единственное действие иконки в трее (клик или пункт меню
// "Показать / скрыть"): свернуть окно, если оно видно, и наоборот. Вызывается
// из горутины D-Bus-обработчика (см. tray.go), поэтому состояние — под
// отдельным trayMu, а не watchMu/mu, которые про совсем другое.
func (a *App) toggleWindow() {
	a.trayMu.Lock()
	hidden := a.windowHidden
	a.windowHidden = !hidden
	a.trayMu.Unlock()
	if hidden {
		wailsrt.WindowShow(a.ctx)
	} else {
		wailsrt.WindowHide(a.ctx)
	}
}

// shutdown закрывает соединение трея с D-Bus и соединение с удалённым ботом,
// если они поднимались. Отдельный хук (options.App.OnShutdown), а не просто
// оставить процессу самому прибраться при выходе: незакрытые соединения — не
// критично (ОС закроет сокеты вместе с процессом), но явный Close честнее,
// чем полагаться на это.
func (a *App) shutdown(ctx context.Context) {
	if a.tray != nil {
		a.tray.Close()
	}
	if a.remote != nil {
		a.remote.Close()
	}
}

// Bootstrap — методы этого файла, вызываемые с фронтенда напрямую; команды
// StartBot/StopBot/SetFilter и подобные — в actions.go.
func (a *App) Bootstrap() Bootstrap {
	pid := a.tickPID()
	a.mu.Lock()
	defer a.mu.Unlock()
	return Bootstrap{
		Status: a.statusLocked(pid), UIState: a.ui,
		Records: toRecs(a.visible), Platform: runtime.GOOS,
	}
}
