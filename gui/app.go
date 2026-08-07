package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"heroku-console/botproc"
	"heroku-console/logfeed"
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

// App — состояние бэкенда. Один экземпляр на окно, живёт всё время работы
// приложения. mu защищает ring/parser/visible/ui/filter — всё, что читает
// и живой хвост лога (feedLine), и обработчики настроек с фронтенда
// (SetFilter и подобные), поэтому оба пути идут только через него.
// watchMu — отдельный, более узкий замок вокруг вотчдога: он не должен
// ждать пересборки лога, чтобы не задерживать перезапуск бота.
type App struct {
	ctx context.Context
	bot *botproc.Manager

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

	// Точки подмены. Всё, чем бэкенд трогает внешний мир, — поиск процесса
	// в /proc, запуск и остановка бота, отправка события в окно — проходит
	// через эти поля. Без них логику вотчдога (кто, когда и на каком
	// основании решает перезапускать) нельзя проверить, не подняв
	// настоящего бота в настоящем окне Wails; ровно поэтому ложное
	// срабатывание из 1.6.1 и доехало до релиза.
	pid     func() int
	aliveAt func(pid int) bool
	start   func() botproc.StartResult
	stop    func() int
	emit    func(event string, data ...interface{})
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
	a.start = a.bot.Start
	a.stop = a.bot.Stop
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
	a.loadHistory()
	go a.followLog()
	go a.statusLoop()
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
		uptime = botproc.Uptime(pid)
	}
	streamIssue := ""
	if a.follower != nil {
		if ok, reason, since := a.follower.Alive(); !ok {
			streamIssue = reason + " · " + humanSince(time.Since(since))
		}
	}
	a.watchMu.Lock()
	restarting := a.restarting
	a.watchMu.Unlock()
	return Status{
		Alive: pid != 0, PID: pid, Uptime: uptime,
		BotVersion: a.bot.Version(), HkcVersion: version,
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
	switch a.stop() {
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
