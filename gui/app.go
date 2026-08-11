package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"heroku-console/botproc"
	"heroku-console/logfeed"
	"heroku-console/preflight"
	"heroku-console/remotebot"
	"heroku-console/selfupdate"
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

func NewApp() *App {
	dir := os.Getenv("HEROKU_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "Heroku")
	}
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

// startRemote поднимает бота на другой машине по SSH вместо локального
// botproc — переставляет точки подмены (pid/aliveAt/start/stop/uptime/
// botVersion) на remotebot.Client, дальше statusLoop и вотчдог работают
// не зная разницы. Осечка при подключении (неверный хост, ключ не
// подходит) оставляет точки подмены как есть — они по-прежнему смотрят на
// локальный a.bot, что на машине без /proc и без каталога бота просто
// молча ничего не находит (PID()==0, Version()==""), а не падает: то же
// самое "тихая осечка не должна ронять окно", что и у остальных
// best-effort частей GUI. Notice в логе — единственный способ узнать
// почему.
func (a *App) startRemote() {
	r := a.ui.Remote
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		a.emit("notice", "не удалось определить каталог настроек: "+err.Error())
		a.setRemoteState("offline", err.Error())
		a.failRemotePreflight(err)
		return
	}
	c, err := remotebot.Dial(remotebot.Config{
		Host:           r.Host,
		User:           r.User,
		KeyPath:        r.KeyPath,
		HerokuDir:      r.Dir,
		KnownHostsPath: filepath.Join(cfgDir, "hkc", "known_hosts"),
	})
	if err != nil {
		a.emit("notice", "не удалось подключиться к "+r.Host+": "+err.Error())
		a.setRemoteState("offline", err.Error())
		a.failRemotePreflight(err)
		return
	}
	a.remote = c
	a.setRemoteState("online", "SSH · "+r.Dir)

	// Сорвавшаяся SSH-команда — это «не смог спросить», а не «бота нет».
	// Разница видна невооружённым глазом: на локальной машине /proc не
	// подводит, а через сеть ответ теряется регулярно, и без этого различия
	// pid в окне мигал 1234 → 0 → 1234 на каждой заминке, а вотчдог
	// принимался «спасать» живого бота. Держим последний достоверный ответ
	// remoteStaleWindow — дальше уже честнее признать, что мы не знаем.
	//
	// Заодно это и единственный регулярный пульс соединения: опрос pid идёт
	// каждую секунду, отдельно пинговать хост было бы тем же запросом ещё
	// раз.
	online := func(pid int) int {
		a.setRemoteState("online", "SSH · "+r.Dir)
		a.rememberPID(pid)
		return pid
	}
	offline := func(err error) int {
		a.setRemoteState("offline", err.Error())
		return a.stalePID()
	}
	a.pid = func() int {
		pid, err := c.PID()
		if err != nil {
			return offline(err)
		}
		return online(pid)
	}
	a.aliveAt = func(pid int) bool {
		pids, err := c.PIDs()
		if err != nil {
			return offline(err) == pid
		}
		for _, p := range pids {
			if p == pid {
				online(pid)
				return true
			}
		}
		a.setRemoteState("online", "SSH · "+r.Dir)
		return false
	}
	a.start = func() startResult {
		r := c.Start()
		return startResult{PID: r.PID, AlreadyStarting: r.AlreadyStarting, Err: r.Err}
	}
	a.stop = c.Stop
	a.uptime = func(pid int) string {
		s, err := c.Uptime(pid)
		if err != nil {
			return "—"
		}
		return s
	}
	a.botVersion = func() string {
		v, _ := c.Version()
		return v
	}

	go a.runChecklist(c.Preflight())
	a.loadRemoteHistory()
	go a.followRemoteLog()
}

// failRemotePreflight отмечает все проверки как заведомо неудавшиеся —
// подключиться не вышло даже для того, чтобы их выполнить (плохие
// настройки, сеть). Экран проверок не должен виснуть с крутящимися
// индикаторами до бесконечности только потому, что нет самого способа их
// прогнать: пользователь должен увидеть ровно ту же причину, что уже ушла
// в notice, прямо там, куда и так смотрит.
func (a *App) failRemotePreflight(err error) {
	checks := (&remotebot.Client{}).Preflight()
	for i, c := range checks {
		a.emit("preflight-check", PreflightEvent{
			Index: i, Total: len(checks), Name: c.Name,
			Status: "failed", Detail: "нет подключения: " + err.Error(),
		})
	}
}

// loadRemoteHistory — то же, что loadHistory делает локально, только
// историю приносит одна SSH-команда (remotebot.Client.TailLines), а не
// чтение файла с диска.
func (a *App) loadRemoteHistory() {
	lines, err := a.remote.TailLines(ringCapacity)
	if err != nil {
		a.emit("notice", "не удалось прочитать лог на удалённой машине: "+err.Error())
		return
	}
	a.mu.Lock()
	a.ring = append(a.ring[:0], lines...)
	a.rebuildLocked()
	a.mu.Unlock()
}

// followRemoteLog — удалённый аналог followLog: тот же feedLine на каждую
// новую строку, разница только в источнике (remotebot.Client.Follow вместо
// logfeed.Follow).
func (a *App) followRemoteLog() {
	h, err := a.remote.Follow()
	if err != nil {
		a.emit("notice", "не удалось начать слежение за логом: "+err.Error())
		return
	}
	a.remoteFollower = h
	for raw := range h.Lines {
		a.feedLine(raw)
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

// runPreflight гоняет те же проверки окружения, что и TUI (preflight.All),
// последовательно, не параллельно — прогон должен читаться как цепочка
// шагов, а не список, обновившийся целиком разом, тот же контракт, что и в
// internal/tui/preflight.go. Шлёт результат каждой отдельным событием, а не
// одним ответом в конце: "модульные тесты" внутри могут идти секундами, и
// окно не должно висеть немым всё это время.
//
// Идёт в фоне параллельно с loadHistory/followLog/statusLoop, а не до них —
// сами проверки лишь показывают состояние окружения, они не blocking-условие
// для того, чтобы начать читать уже существующий heroku.log.
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

func (a *App) runPreflight() {
	a.runChecklist(preflight.All(a.bot.HerokuDir, a.bot.LogFile))
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
		checks = preflight.All(a.bot.HerokuDir, a.bot.LogFile)
	}
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return names
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

// guiAssetPrefix/guiAssetSuffix — имя архива с GUI-бинарником в релизе, как
// его кладёт release.yml: hrk-console-gui-$TAG-linux-amd64.tar.gz на Linux,
// hrk-console-gui-$TAG-windows-amd64.tar.gz на Windows (два разных job'а в
// release.yml, одна и та же схема имени). hkc в том же релизе носит своё
// имя (hkc-*) — selfupdate.Apply различает их по этим prefix/suffix, иначе
// скачал бы не то.
const guiAssetPrefix = "hrk-console-gui-"

func guiAssetSuffix() string {
	if runtime.GOOS == "windows" {
		return "-windows-amd64.tar.gz"
	}
	return "-linux-amd64.tar.gz"
}

// старте окна идёт через CheckChannels (экран обновлений), который и так
// узнаёт оба канала. Логика запроса и сравнения версий общая с TUI на
// стабильном канале (selfupdate.Check); канал "dev" — только у GUI.
func (a *App) CheckForUpdate() UpdateCheckResult {
	a.mu.Lock()
	channel := a.ui.UpdateChannel
	a.mu.Unlock()
	r := selfupdate.CheckChannel(channel, version)
	return UpdateCheckResult{
		OK: r.OK, Available: r.Available,
		Current: r.Current, Latest: r.Latest,
		URL: r.URL, Message: r.Message, Channel: channel,
	}
}

// SetUpdateChannel переключает канал самообновления ("" — стабильный,
// "dev" — сборки с ветки dev) и сразу возвращает свежую проверку по новому
// каналу — отдельным запросом с фронтенда после переключения было бы то же
// самое действие, просто в два шага вместо одного.
func (a *App) SetUpdateChannel(channel string) UpdateCheckResult {
	a.mu.Lock()
	a.ui.UpdateChannel = channel
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
	return a.CheckForUpdate()
}

// ChannelsResult — состояние обоих каналов разом, для экрана обновлений
// после проверок окружения. Не «что новее»: между каналами это неразрешимо
// (в dev-теге хеш коммита, у хеша нет порядка), поэтому наружу идут факты —
// тег и когда он опубликован, — а решение остаётся за человеком.
type ChannelsResult struct {
	Current string                  `json:"current"`
	Channel string                  `json:"channel"` // на каком канале сидим сейчас
	Stable  selfupdate.ChannelState `json:"stable"`
	Dev     selfupdate.ChannelState `json:"dev"`
	// Offer — есть ли вообще что предлагать. Экран показывается только
	// тогда: гонять его вхолостую после каждой проверки окружения ради
	// «всё актуально» значило бы добавить лишний шаг к каждому запуску.
	Offer bool `json:"offer"`
}

// CheckChannels опрашивает stable и dev разом — их два независимых запроса
// идут параллельно внутри selfupdate.CheckBoth. Осечка одного канала не
// прячет второй: у каждого свой ok/message, и экран покажет то, что удалось
// узнать, вместо пустоты.
func (a *App) CheckChannels() ChannelsResult {
	a.mu.Lock()
	channel := a.ui.UpdateChannel
	a.mu.Unlock()

	stable, dev := selfupdate.CheckBoth(version)
	res := ChannelsResult{Current: version, Channel: channel, Stable: stable, Dev: dev}
	// Предлагать есть что, если хоть один канал ответил тегом, который не
	// равен запущенной сборке. Именно равенство, а не «новее»: сравнить
	// dev-сборку со стабильным релизом по номеру нельзя, а вот «это не то,
	// что запущено» — утверждение, верное всегда.
	res.Offer = (stable.OK && !stable.IsCurrent) || (dev.OK && !dev.IsCurrent)
	return res
}

// ApplyUpdateFrom — то же, что ApplyUpdate, но из явно названного канала, и
// с отчётом о ходе: каждый шаг selfupdate уезжает фронтенду событием
// 'update-step', по которому рисуется окно обновления. Смена канала здесь
// же и сохраняется — уехав на dev через этот экран, пользователь остаётся
// на dev и при следующих проверках, иначе окно обновилось бы до сборки, о
// которой само потом «забыло» бы.
func (a *App) ApplyUpdateFrom(channel string) ActionResult {
	a.mu.Lock()
	if a.ui.UpdateChannel != channel {
		a.ui.UpdateChannel = channel
		ui := a.ui
		a.mu.Unlock()
		state.Save(ui)
	} else {
		a.mu.Unlock()
	}

	applied, err := selfupdate.ApplyChannelProgress(channel, guiAssetPrefix, guiAssetSuffix(),
		func(p selfupdate.Progress) { a.emit("update-step", p) })
	if err != nil {
		// Отдельным событием, а не только возвратом: окно обновления живёт
		// на потоке шагов, и оборвись он молча — оно осталось бы навсегда
		// с крутящимся шагом, который уже никогда не закроется.
		a.emit("update-failed", err.Error())
		return ActionResult{OK: false, Message: err.Error()}
	}
	return ActionResult{OK: true, Message: applied}
}

// ApplyUpdate скачивает GUI-бинарник из последнего релиза выбранного канала
// и подменяет им себя на диске (selfupdate.ApplyChannel — общая логика с
// TUI на стабильном канале, dev — только здесь). Не вызывается сам по себе
// в фоне — только по явному клику из фронтенда (см. update-status): подмена
// собственного исполняемого файла необратима без переустановки, и
// молчаливый автозапуск для такого не подходит, в отличие от простой
// проверки обновлений. Перезапуск (RestartApp) — отдельный шаг.
func (a *App) ApplyUpdate() ActionResult {
	a.mu.Lock()
	channel := a.ui.UpdateChannel
	a.mu.Unlock()
	return a.ApplyUpdateFrom(channel)
}

// healPendingUpdate подхватывает обновление, которое ApplyUpdate когда-то
// скачал (см. RestartApp), но подмена так и не завершилась, — например,
// предыдущий явный клик «перезапустить» запустил своп-скрипт, а тот не
// уложился в отведённые попытки (антивирус подержал свежескачанный exe
// дольше обычного). Без этой проверки «.new» так и лежал бы рядом с
// прежним exe навсегда, а пользователю приходилось бы вручную стирать
// старый файл и переименовывать новый. Тот же своп-скрипт просто
// запускается заново при каждом обычном старте окна, без единого клика —
// startup() зовёт это самым первым делом, до всего остального: если
// обновление ждёт применения, окну всё равно предстоит тут же закрыться.
func (a *App) healPendingUpdate() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	staged := selfupdate.PendingUpdate(exe)
	if staged == "" {
		return false
	}
	if err := restartWithSwap(exe, staged); err != nil {
		return false
	}
	a.quitSoon()
	return true
}

// quitSoon — небольшая задержка перед Quit, чтобы фронтенд успел получить
// ответ на вызвавший её запрос и показать что-то осмысленное, прежде чем
// окно исчезнет.
func (a *App) quitSoon() {
	go func() {
		time.Sleep(300 * time.Millisecond)
		wailsrt.Quit(a.ctx)
	}()
}

// RestartApp поднимает новый процесс того же бинарника (уже подменённого
// ApplyUpdate) и завершает текущий. Отдельный явный шаг, а не часть
// ApplyUpdate, — обновление на диске и разрыв текущей сессии окна должны
// быть двумя разными решениями пользователя, не одним неожиданным.
func (a *App) RestartApp() ActionResult {
	exe, err := os.Executable()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}

	// На Windows ApplyUpdate не смог подменить себя сразу (файл заблокирован,
	// пока этот процесс из него выполняется) и оставил новую версию рядом —
	// restartWithSwap ждёт, пока этот процесс действительно завершится,
	// прежде чем подменять файл. На Linux staged всегда пусто: там подмена
	// уже произошла в момент ApplyUpdate, и RestartApp — просто обычный
	// перезапуск.
	if staged := selfupdate.PendingUpdate(exe); staged != "" {
		if err := restartWithSwap(exe, staged); err != nil {
			return ActionResult{OK: false, Message: err.Error()}
		}
	} else {
		cmd := exec.Command(exe)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return ActionResult{OK: false, Message: err.Error()}
		}
	}

	a.quitSoon()
	return ActionResult{OK: true}
}

// loadHistory поднимает хвост уже написанного лога при старте окна — без
// этого приложение открывалось бы на пустом экране, пока не придёт первая
// новая строка.
func (a *App) loadHistory() {
	lines := logfeed.TailLines(a.bot.LogFile, ringCapacity)
	a.mu.Lock()
	a.ring = append(a.ring[:0], lines...)
	a.rebuildLocked()
	a.mu.Unlock()
}

// rebuildLocked пересобирает a.visible с нуля из a.ring под текущие
// filter/showDebug/minLevel — нужна и при первой загрузке, и при любой
// смене этих настроек с фронтенда. Новый парсер продолжает жить как
// a.parser: дальше живой хвост (feedLine) кормит именно его.
// Вызывающий обязан держать a.mu.
func (a *App) rebuildLocked() {
	p := logfeed.NewParser()
	var out []*logfeed.Record
	for _, raw := range a.ring {
		rec, complete := p.Feed(raw)
		if !complete {
			continue
		}
		if !logfeed.Visible(rec, a.ui.ShowDebug, a.filter, logfeed.Level(a.ui.MinLevel)) {
			continue
		}
		if n := len(out); n > 0 && logfeed.SameEntry(out[n-1], rec) {
			out[n-1].Count = maxInt(out[n-1].Count, 1) + 1
			out[n-1].Time = rec.Time
			continue
		}
		out = append(out, rec)
	}
	a.visible = out
	a.parser = p
}

// followLog следит за живым хвостом heroku.log и кормит им a.parser по
// одной строке — в отличие от rebuildLocked, тут не полная пересборка, а
// инкрементальное схлопывание относительно уже показанного хвоста.
func (a *App) followLog() {
	from := logfeed.LineCount(a.bot.LogFile) + 1
	f, err := logfeed.Follow(a.bot.LogFile, from)
	if err != nil {
		return
	}
	a.follower = f
	for raw := range f.Lines {
		a.feedLine(raw)
	}
}

func (a *App) feedLine(raw string) {
	a.mu.Lock()
	a.ring = append(a.ring, raw)
	if len(a.ring) > ringCapacity+500 {
		a.ring = a.ring[len(a.ring)-ringCapacity:]
	}
	rec, complete := a.parser.Feed(raw)
	if !complete {
		a.mu.Unlock()
		return
	}
	var evt *TailEvent
	if logfeed.Visible(rec, a.ui.ShowDebug, a.filter, logfeed.Level(a.ui.MinLevel)) {
		if n := len(a.visible); n > 0 && logfeed.SameEntry(a.visible[n-1], rec) {
			last := a.visible[n-1]
			last.Count = maxInt(last.Count, 1) + 1
			last.Time = rec.Time
			evt = &TailEvent{Replace: true, Rec: toRec(last)}
		} else {
			a.visible = append(a.visible, rec)
			// Тот же предел, что у кольцевого буфера. Без него visible рос
			// без границы всю жизнь окна: обрезала его только пересборка при
			// смене фильтра и кнопка очистки, то есть у окна, которое просто
			// оставили открытым, — никогда. Показать больше, чем помнит ring,
			// всё равно нельзя: пересборка возьмёт историю именно оттуда.
			if len(a.visible) > ringCapacity+500 {
				a.visible = a.visible[len(a.visible)-ringCapacity:]
			}
			evt = &TailEvent{Replace: false, Rec: toRec(rec)}
		}
	}
	a.mu.Unlock()
	if evt != nil {
		a.emit("log-tail", evt)
	}
}

func (a *App) statusLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		// Один pid на весь тик: раньше statusLocked и maybeWatchdogRestart
		// спрашивали его независимо, то есть дважды сканировали /proc в
		// одну и ту же секунду ради одного и того же ответа.
		pid := a.tickPID()
		a.emitStatus(pid)
		a.maybeWatchdogRestart(pid)
	}
}

func (a *App) emitStatus(pid int) {
	a.mu.Lock()
	st := a.statusLocked(pid)
	a.mu.Unlock()
	a.emit("status", st)
}

// statusLocked собирает Status из текущего состояния. Вызывающий обязан
// держать a.mu (кроме watchMu — это отдельный замок, дедлока не будет).
func (a *App) statusLocked(pid int) Status {
	uptime := "—"
	if pid != 0 {
		uptime = a.uptime(pid)
	}
	streamIssue, streamIssueFor := "", ""
	switch {
	case a.follower != nil:
		if ok, reason, since := a.follower.Alive(); !ok {
			streamIssue, streamIssueFor = reason, humanSince(time.Since(since))
		}
	case a.remoteFollower != nil:
		if ok, reason, since := a.remoteFollower.Alive(); !ok {
			streamIssue, streamIssueFor = reason, humanSince(time.Since(since))
		}
	}
	a.watchMu.Lock()
	restarting := a.restarting
	a.watchMu.Unlock()
	return Status{
		Alive: pid != 0, PID: pid, Uptime: uptime,
		BotVersion: a.botVersion(), HkcVersion: version,
		Watchdog: a.ui.Watchdog, Restarting: restarting,
		StreamIssue: streamIssue, StreamIssueFor: streamIssueFor,
		RemoteState: a.remoteState, RemoteStateNote: a.remoteStateNote,
	}
}

// maybeWatchdogRestart — тот же контракт, что у watchdog в TUI-вьюере:
// включается вручную, поднимает бота заново только если он упал сам, не
// чаще watchdogCooldown. pid — тот же, что уже получил statusLoop за этот
// тик (tickPID), отдельно не спрашивается.
func (a *App) maybeWatchdogRestart(pid int) {
	a.mu.Lock()
	on := a.ui.Watchdog
	a.mu.Unlock()

	alive := pid != 0
	a.watchMu.Lock()
	if alive {
		a.everAlive = true
	}
	seen := a.everAlive
	a.watchMu.Unlock()

	if !on || alive || !seen {
		return
	}

	a.watchMu.Lock()
	if a.restarting || time.Since(a.lastRestartAttempt) < watchdogCooldown {
		a.watchMu.Unlock()
		return
	}
	a.restarting = true
	a.lastRestartAttempt = time.Now()
	a.watchMu.Unlock()

	a.emit("notice", "watchdog: бот не отвечает — перезапускаю")
	notifyDesktop("Heroku", "Бот не отвечает — перезапускаю")
	go func() {
		a.start()
		a.watchMu.Lock()
		a.restarting = false
		a.watchMu.Unlock()
	}()
}

// ─── методы, вызываемые с фронтенда ────────────────────────────────────────

func (a *App) Bootstrap() Bootstrap {
	pid := a.tickPID()
	a.mu.Lock()
	defer a.mu.Unlock()
	return Bootstrap{
		Status: a.statusLocked(pid), UIState: a.ui,
		Records: toRecs(a.visible), Platform: runtime.GOOS,
	}
}

// ConnectRemote сохраняет настройки подключения к боту на другой машине и
// перезапускает приложение (тем же RestartApp, что и после самообновления).
// Переключение между локальным ботом и удалённым на лету — по сути другая
// программа (другой источник /proc, другой файл лога, другое соединение) —
// пересобирать половину App вместо честного перезапуска было бы источником
// трудноуловимых гонок между старыми и новыми горутинами.
func (a *App) ConnectRemote(cfg state.Remote) ActionResult {
	a.mu.Lock()
	a.ui.Remote = cfg
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
	return a.RestartApp()
}

// DisconnectRemote возвращает к локальному боту тем же перезапуском.
func (a *App) DisconnectRemote() ActionResult {
	a.mu.Lock()
	a.ui.Remote = state.Remote{}
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
	return a.RestartApp()
}

// TestRemoteConnection — «проверить соединение» до того, как сохранить
// настройки и перезапустить окно: тот же remotebot.Dial, что и настоящее
// подключение (включая TOFU-проверку ключа хоста), но соединение сразу
// закрывается и ничего не сохраняется. Без этого метода единственный
// способ узнать, что хост/логин/ключ введены неверно, — сохранить,
// перезапустить окно и увидеть notice постфактум.
func (a *App) TestRemoteConnection(cfg state.Remote) ActionResult {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	c, err := remotebot.Dial(remotebot.Config{
		Host:           cfg.Host,
		User:           cfg.User,
		KeyPath:        cfg.KeyPath,
		HerokuDir:      cfg.Dir,
		KnownHostsPath: filepath.Join(cfgDir, "hkc", "known_hosts"),
	})
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	defer c.Close()

	pid, err := c.PID()
	if err != nil {
		return ActionResult{OK: false, Message: "подключился, но не смог прочитать /proc: " + err.Error()}
	}
	if pid == 0 {
		return ActionResult{OK: true, Message: "подключение работает, но бот сейчас не запущен"}
	}
	return ActionResult{OK: true, Message: fmt.Sprintf("подключение работает, бот жив (pid %d)", pid)}
}

// ClearLog сбрасывает буфер и показанную историю — старые накопленные
// ошибки перестают маячить в панели и счётчиках. Сам файл heroku.log не
// трогается: живой хвост (followLog) продолжает читать его с той же
// позиции, что и раньше, просто дальше пишет уже в пустой буфер.
func (a *App) ClearLog() {
	a.mu.Lock()
	a.ring = a.ring[:0]
	a.visible = nil
	a.parser = logfeed.NewParser()
	a.mu.Unlock()
}

func (a *App) SetFilter(text string) []Rec {
	a.mu.Lock()
	a.filter = text
	a.rebuildLocked()
	recs := toRecs(a.visible)
	a.mu.Unlock()
	return recs
}

func (a *App) SetShowDebug(on bool) []Rec {
	a.mu.Lock()
	a.ui.ShowDebug = on
	a.rebuildLocked()
	recs, ui := toRecs(a.visible), a.ui
	a.mu.Unlock()
	state.Save(ui)
	return recs
}

// CycleMinLevel — тот же цикл, что клавиша w в TUI: всё → warning+ → error+.
func (a *App) CycleMinLevel() []Rec {
	a.mu.Lock()
	switch logfeed.Level(a.ui.MinLevel) {
	case logfeed.LevelWarning:
		a.ui.MinLevel = int(logfeed.LevelError)
	case logfeed.LevelError:
		a.ui.MinLevel = int(logfeed.LevelDebug)
	default:
		a.ui.MinLevel = int(logfeed.LevelWarning)
	}
	a.rebuildLocked()
	recs, ui := toRecs(a.visible), a.ui
	a.mu.Unlock()
	state.Save(ui)
	return recs
}

func (a *App) SetWatchdog(on bool) {
	a.mu.Lock()
	a.ui.Watchdog = on
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
}

func (a *App) StartBot() ActionResult {
	// Отмечаем попытку до самого запуска: exec ещё не заменил cmdline bash на
	// "python3 -m heroku" в первые мгновения, PID() короткое время видит 0, и
	// без этой отметки вотчдог на ближайшем тике принимает только что
	// стартовавший процесс за упавший и бьёт тревогу поверх ручного запуска.
	a.watchMu.Lock()
	a.lastRestartAttempt = time.Now()
	a.watchMu.Unlock()

	res := a.start()
	switch {
	case res.Err != nil:
		return ActionResult{OK: false, Message: res.Err.Error()}
	case res.AlreadyStarting:
		return ActionResult{OK: true, Message: "запуск уже идёт в другом окне"}
	default:
		return ActionResult{OK: true, Message: fmt.Sprintf("pid %d", res.PID)}
	}
}

func (a *App) StopBot() ActionResult {
	code, err := a.stop()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	switch code {
	case 0:
		return ActionResult{OK: true, Message: "остановлен"}
	case 2:
		return ActionResult{OK: true, Message: "принудительно остановлен"}
	default:
		return ActionResult{OK: true, Message: "не был запущен"}
	}
}

func (a *App) RestartBot() ActionResult {
	if a.alive() {
		a.stop()
	}
	return a.StartBot()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func toRec(r *logfeed.Record) Rec {
	return Rec{
		Time: r.Time, Level: int(r.Level), Module: r.Module,
		Lines: r.Lines, Hard: r.Hard, Warn: r.Warn, Err: r.Err, Count: r.Count,
	}
}

func toRecs(rs []*logfeed.Record) []Rec {
	out := make([]Rec, len(rs))
	for i, r := range rs {
		out[i] = toRec(r)
	}
	return out
}

func humanSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%dс", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dм", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dч %dм", int(d.Hours()), int(d.Minutes())%60)
	}
}
