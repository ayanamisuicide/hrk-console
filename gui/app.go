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
type Status struct {
	Alive       bool   `json:"alive"`
	PID         int    `json:"pid"`
	Uptime      string `json:"uptime"`
	BotVersion  string `json:"botVersion"`
	HkcVersion  string `json:"hkcVersion"`
	Watchdog    bool   `json:"watchdog"`
	Restarting  bool   `json:"restarting"`
	StreamIssue string `json:"streamIssue"`
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
type Bootstrap struct {
	Status  Status      `json:"status"`
	UIState state.State `json:"uiState"`
	Records []Rec       `json:"records"`
}

type ActionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// UpdateInfo — событие "вышла версия новее той, что запущена", шлётся
// фронтенду не чаще раза за окно (см. checkUpdateOnce).
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

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.ui = state.Load()
	a.parser = logfeed.NewParser()

	if a.ui.Remote.Host != "" {
		go a.startRemote()
	} else {
		a.loadHistory()
		go a.followLog()
		go a.runPreflight()
	}
	go a.statusLoop()
	go a.checkUpdateOnce()
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
		return
	}
	a.remote = c

	a.pid = func() int {
		pid, _ := c.PID()
		return pid
	}
	a.aliveAt = func(pid int) bool {
		pids, err := c.PIDs()
		if err != nil {
			return false
		}
		for _, p := range pids {
			if p == pid {
				return true
			}
		}
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

	a.loadRemoteHistory()
	go a.followRemoteLog()
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

// shutdown закрывает соединение с удалённым ботом, если оно было открыто.
// Отдельный хук (options.App.OnShutdown), а не просто оставить процессу
// самому прибраться при выходе: незакрытое соединение — не критично (ОС
// закроет сокет вместе с процессом), но явный Close честнее, чем
// полагаться на это.
func (a *App) shutdown(ctx context.Context) {
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
func (a *App) runPreflight() {
	checks := preflight.All(a.bot.HerokuDir, a.bot.LogFile)
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

// PreflightChecks — только имена проверок, без запуска. Фронтенду нужно
// нарисовать полный список строк ДО того, как первая проверка успеет
// отработать (события 'preflight-check' начинают идти сразу при старте
// окна) — иначе экран открывался бы пустым и достраивался бы по одной
// строке снизу, а не показывал сразу, сколько шагов всего и какие.
//
// В удалённом режиме пустой список — проверки (venv, python3, ffmpeg и
// так далее) про локальную файловую систему, а бот живёт на другой
// машине; пустой список фронтенд уже умеет молча закрывать экран проверок
// (см. renderPreflightChecks в main.js), поэтому дублировать эти проверки
// по SSH не понадобилось.
func (a *App) PreflightChecks() []string {
	// a.ui.Remote, не a.remote: a.ui выставляется синхронно в startup() до
	// первой же горутины, а a.remote появляется только после успешного SSH-
	// подключения (startRemote — фоновая горутина). Фронтенд же зовёт этот
	// метод сразу при загрузке страницы — гонка с a.remote раньше иногда
	// успевала застать его ещё nil и по ошибке отдать локальный список,
	// который потом никогда не резолвился (runPreflight() в удалённом
	// режиме не запускается вовсе), и экран проверок зависал навсегда.
	if a.ui.Remote.Host != "" {
		return nil
	}
	checks := preflight.All(a.bot.HerokuDir, a.bot.LogFile)
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

// checkUpdateOnce — разовая проверка последнего релиза на GitHub при
// старте окна. Шлёт фронтенду результат через CheckForUpdate, но только на
// успешный ответ (галочка "актуально" или бейдж новой версии) — на осечке
// (нет сети, GitHub не ответил, версия сборки не чистый тег) молчит: само
// уведомление не настолько важно, чтобы шуметь при каждом старте окна из-за
// временной недоступности сети. Ручная проверка (CheckForUpdate по клику)
// осечку уже показывает — там пользователь явно спросил и ждёт ответа.
func (a *App) checkUpdateOnce() {
	res := a.CheckForUpdate()
	if !res.OK {
		return
	}
	a.emit("update-status", res)
}

// CheckForUpdate — вызывается и из checkUpdateOnce при старте окна, и по
// клику на бейдж в шапке ("менюшка обновлений" — сам бейдж). Логика запроса
// и сравнения версий общая с TUI, см. selfupdate.Check.
func (a *App) CheckForUpdate() UpdateCheckResult {
	r := selfupdate.Check(version)
	return UpdateCheckResult{
		OK: r.OK, Available: r.Available,
		Current: r.Current, Latest: r.Latest,
		URL: r.URL, Message: r.Message,
	}
}

// ApplyUpdate скачивает GUI-бинарник из последнего релиза и подменяет им
// себя на диске (selfupdate.Apply — общая логика с TUI). Не вызывается сам
// по себе в фоне — только по явному клику из фронтенда (см. update-status):
// подмена собственного исполняемого файла необратима без переустановки, и
// молчаливый автозапуск для такого не подходит, в отличие от простой
// проверки в checkUpdateOnce. Перезапуск (RestartApp) — отдельный шаг.
func (a *App) ApplyUpdate() ActionResult {
	applied, err := selfupdate.Apply(guiAssetPrefix, guiAssetSuffix())
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	return ActionResult{OK: true, Message: applied}
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

	// Небольшая задержка — чтобы фронтенд успел получить этот ответ и
	// показать что-то осмысленное, прежде чем окно исчезнет.
	go func() {
		time.Sleep(300 * time.Millisecond)
		wailsrt.Quit(a.ctx)
	}()
	return ActionResult{OK: true}
}

// notifyDesktop — best-effort desktop-уведомление через notify-send (часть
// libnotify-bin, обычно уже стоит на Linux-рабочих столах): чтобы падение
// бота было видно, даже когда окно GUI свёрнуто или не в фокусе. Нет
// бинарника или недоступны DISPLAY/DBUS — тихо ничего не делает, само
// уведомление не настолько важно, чтобы падать или шуметь в лог.
func notifyDesktop(title, body string) {
	_ = exec.Command("notify-send", "-a", "Heroku", title, body).Run()
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
	streamIssue := ""
	switch {
	case a.follower != nil:
		if ok, reason, since := a.follower.Alive(); !ok {
			streamIssue = reason + " · " + humanSince(time.Since(since))
		}
	case a.remoteFollower != nil:
		if ok, reason, since := a.remoteFollower.Alive(); !ok {
			streamIssue = reason + " · " + humanSince(time.Since(since))
		}
	}
	a.watchMu.Lock()
	restarting := a.restarting
	a.watchMu.Unlock()
	return Status{
		Alive: pid != 0, PID: pid, Uptime: uptime,
		BotVersion: a.botVersion(), HkcVersion: version,
		Watchdog: a.ui.Watchdog, Restarting: restarting,
		StreamIssue: streamIssue,
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
	return Bootstrap{Status: a.statusLocked(pid), UIState: a.ui, Records: toRecs(a.visible)}
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

// SetShowSidebar — клавиша s. Лога не касается, пересобирать нечего, но
// сохранить надо: ShowSidebar лежит в общем с TUI state.json, и без этого
// два интерфейса расходились бы в настройке, которая у них одна на двоих.
func (a *App) SetShowSidebar(on bool) {
	a.mu.Lock()
	a.ui.ShowSidebar = on
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
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

// ExportLog сохраняет содержимое в файл через нативный диалог "сохранить
// как". Сам текст формирует фронтенд — он уже знает, как рендерит
// видимые строки (тот же порядок, что на экране, включая фильтр), задача
// бэкенда — только диалог выбора пути и запись на диск, недоступные из
// песочницы webview напрямую.
func (a *App) ExportLog(content string) ActionResult {
	path, err := wailsrt.SaveFileDialog(a.ctx, wailsrt.SaveDialogOptions{
		Title:           "Экспорт лога",
		DefaultFilename: "heroku-log-" + time.Now().Format("2006-01-02-150405") + ".txt",
	})
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	if path == "" {
		return ActionResult{OK: true, Message: "отменено"}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	return ActionResult{OK: true, Message: path}
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
