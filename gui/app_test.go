package main

import (
	"sync"
	"testing"
	"time"

	"heroku-console/botproc"
	"heroku-console/logfeed"
	"heroku-console/state"
)

// harness — App с подменёнными точками выхода наружу плюс запись того, что
// он попытался сделать. Настоящий бэкенд ходит в /proc и в рантайм Wails;
// здесь вместо них — управляемый pid и журнал событий, так что "вотчдог
// решил перезапустить" проверяется без единого живого процесса.
type harness struct {
	app *App

	mu      sync.Mutex
	pid     int
	starts  int
	stops   int
	notices []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// state.Save пишет в os.UserConfigDir(); без подмены тесты затирали бы
	// живые настройки того, кто их запускает.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	h := &harness{}
	a := &App{bot: botproc.New(t.TempDir())}
	a.parser = logfeed.NewParser()
	a.ui = state.Default
	a.pid = func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.pid
	}
	a.start = func() botproc.StartResult {
		h.mu.Lock()
		h.starts++
		h.mu.Unlock()
		return botproc.StartResult{PID: 4242}
	}
	a.stop = func() int {
		h.mu.Lock()
		h.stops++
		h.mu.Unlock()
		return 0
	}
	a.emit = func(event string, data ...interface{}) {
		if event != "notice" || len(data) == 0 {
			return
		}
		s, _ := data[0].(string)
		h.mu.Lock()
		h.notices = append(h.notices, s)
		h.mu.Unlock()
	}
	h.app = a
	return h
}

func (h *harness) setPID(pid int) {
	h.mu.Lock()
	h.pid = pid
	h.mu.Unlock()
}

func (h *harness) counts() (starts, stops, notices int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts, h.stops, len(h.notices)
}

// tick — один оборот statusLoop. Перезапуск вотчдог делает в отдельной
// горутине, поэтому ждём, пока флаг restarting снимется, иначе следующий
// тик увидит недостоверное состояние.
func (h *harness) tick(t *testing.T) {
	t.Helper()
	h.app.maybeWatchdogRestart()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.app.watchMu.Lock()
		busy := h.app.restarting
		h.app.watchMu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("перезапуск вотчдога не завершился за 2с")
		}
		time.Sleep(time.Millisecond)
	}
}

// Регрессия 1.6.1: окно открыли при выключенном боте, вотчдог включён с
// прошлого раза. "Никогда не запускался" — не то же самое, что "упал", и
// поднимать тут нечего.
func TestWatchdogSilentWhenBotNeverRan(t *testing.T) {
	h := newHarness(t)
	h.app.ui.Watchdog = true
	h.setPID(0)

	for i := 0; i < 5; i++ {
		h.tick(t)
	}

	starts, _, notices := h.counts()
	if starts != 0 {
		t.Errorf("вотчдог запустил бота %d раз, хотя тот ни разу не работал", starts)
	}
	if notices != 0 {
		t.Errorf("вотчдог показал %d уведомлений на боте, который не падал", notices)
	}
}

func TestWatchdogRestartsAfterCrash(t *testing.T) {
	h := newHarness(t)
	h.app.ui.Watchdog = true

	h.setPID(1234) // бот работает — вотчдог это запоминает
	h.tick(t)
	if starts, _, _ := h.counts(); starts != 0 {
		t.Fatalf("вотчдог дёрнул запуск на живом боте (%d раз)", starts)
	}

	h.setPID(0) // упал
	h.tick(t)

	starts, _, notices := h.counts()
	if starts != 1 {
		t.Errorf("после падения ожидался 1 запуск, получено %d", starts)
	}
	if notices != 1 {
		t.Errorf("после падения ожидалось 1 уведомление, получено %d", notices)
	}
}

// Кулдаун: бот, который падает сразу после старта, не должен превращать
// вотчдог в цикл перезапусков раз в секунду.
func TestWatchdogRespectsCooldown(t *testing.T) {
	h := newHarness(t)
	h.app.ui.Watchdog = true

	h.setPID(1234)
	h.tick(t)
	h.setPID(0)
	h.tick(t)
	h.tick(t)
	h.tick(t)

	if starts, _, _ := h.counts(); starts != 1 {
		t.Errorf("в пределах кулдауна ожидался 1 запуск, получено %d", starts)
	}

	// Отматываем отметку за кулдаун — следующий тик обязан сработать снова.
	h.app.watchMu.Lock()
	h.app.lastRestartAttempt = time.Now().Add(-watchdogCooldown - time.Second)
	h.app.watchMu.Unlock()
	h.tick(t)

	if starts, _, _ := h.counts(); starts != 2 {
		t.Errorf("после кулдауна ожидалось 2 запуска, получено %d", starts)
	}
}

func TestWatchdogOffDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.app.ui.Watchdog = false

	h.setPID(1234)
	h.tick(t)
	h.setPID(0)
	h.tick(t)

	if starts, _, notices := h.counts(); starts != 0 || notices != 0 {
		t.Errorf("выключенный вотчдог сработал: запусков %d, уведомлений %d", starts, notices)
	}
}

// Ручной Start взводит кулдаун до самого запуска: exec ещё не заменил
// cmdline у bash, pid короткое время не находится, и без отметки вотчдог
// принял бы только что стартовавший процесс за упавший.
func TestStartBotArmsCooldownAgainstWatchdog(t *testing.T) {
	h := newHarness(t)
	h.app.ui.Watchdog = true

	h.setPID(1234)
	h.tick(t)
	h.setPID(0)

	if res := h.app.StartBot(); !res.OK {
		t.Fatalf("StartBot вернул ошибку: %s", res.Message)
	}
	h.tick(t) // pid ещё 0 — окно, в котором раньше срабатывала ложная тревога

	if _, _, notices := h.counts(); notices != 0 {
		t.Errorf("вотчдог поднял тревогу поверх ручного запуска: %d уведомлений", notices)
	}
	if starts, _, _ := h.counts(); starts != 1 {
		t.Errorf("ожидался только ручной запуск, получено %d", starts)
	}
}

func TestRestartBotStopsOnlyRunningBot(t *testing.T) {
	h := newHarness(t)

	h.setPID(0)
	h.app.RestartBot()
	if _, stops, _ := h.counts(); stops != 0 {
		t.Errorf("остановка вызвана для незапущенного бота (%d раз)", stops)
	}

	h.setPID(1234)
	h.app.RestartBot()
	if _, stops, _ := h.counts(); stops != 1 {
		t.Errorf("живой бот не остановлен перед перезапуском (stops=%d)", stops)
	}
	if starts, _, _ := h.counts(); starts != 2 {
		t.Errorf("ожидалось 2 запуска, получено %d", starts)
	}
}

// ─── лог ───────────────────────────────────────────────────────────────────

const (
	lineWarn  = "2026-08-07 00:13:04 [WARNING] heroku.modules.spotify: Token refresh took 4.2s"
	lineErr   = "2026-08-07 00:13:05 [ERROR] heroku.modules.spotify: Token refresh failed"
	lineDebug = "2026-08-07 00:13:06 [DEBUG] urllib3.connectionpool: Starting new HTTPS connection"
)

func TestFeedLineCollapsesRepeats(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < 3; i++ {
		h.app.feedLine(lineWarn)
	}

	if n := len(h.app.visible); n != 1 {
		t.Fatalf("три одинаковые строки дали %d записей вместо одной", n)
	}
	if c := h.app.visible[0].Count; c != 3 {
		t.Errorf("счётчик повторов %d, ожидалось 3", c)
	}
}

func TestFeedLineKeepsDistinctRecords(t *testing.T) {
	h := newHarness(t)

	h.app.feedLine(lineWarn)
	h.app.feedLine(lineErr)

	if n := len(h.app.visible); n != 2 {
		t.Fatalf("разные строки схлопнулись: %d записей вместо двух", n)
	}
}

func TestSetShowDebugRebuildsHistory(t *testing.T) {
	h := newHarness(t)

	h.app.feedLine(lineWarn)
	h.app.feedLine(lineDebug)
	if n := len(h.app.visible); n != 1 {
		t.Fatalf("DEBUG виден при выключенном показе: %d записей", n)
	}

	if recs := h.app.SetShowDebug(true); len(recs) != 2 {
		t.Fatalf("после включения DEBUG ожидалось 2 записи, получено %d", len(recs))
	}
	if recs := h.app.SetShowDebug(false); len(recs) != 1 {
		t.Fatalf("после выключения DEBUG ожидалась 1 запись, получено %d", len(recs))
	}
}

func TestSetFilterRebuildsHistory(t *testing.T) {
	h := newHarness(t)

	h.app.feedLine(lineWarn)
	h.app.feedLine(lineErr)

	if recs := h.app.SetFilter("failed"); len(recs) != 1 {
		t.Errorf("фильтр по подстроке дал %d записей, ожидалась 1", len(recs))
	}
	if recs := h.app.SetFilter("re:took [0-9.]+s"); len(recs) != 1 {
		t.Errorf("фильтр-регэксп дал %d записей, ожидалась 1", len(recs))
	}
	if recs := h.app.SetFilter(""); len(recs) != 2 {
		t.Errorf("снятие фильтра дало %d записей, ожидалось 2", len(recs))
	}
}

func TestCycleMinLevel(t *testing.T) {
	h := newHarness(t)
	h.app.feedLine(lineWarn)
	h.app.feedLine(lineErr)

	want := []struct {
		level int
		recs  int
	}{
		{int(logfeed.LevelWarning), 2}, // warning+ — обе записи
		{int(logfeed.LevelError), 1},   // error+ — только ошибка
		{int(logfeed.LevelDebug), 2},   // порог снят
	}
	for i, w := range want {
		recs := h.app.CycleMinLevel()
		if h.app.ui.MinLevel != w.level {
			t.Errorf("шаг %d: порог %d, ожидался %d", i, h.app.ui.MinLevel, w.level)
		}
		if len(recs) != w.recs {
			t.Errorf("шаг %d: %d записей, ожидалось %d", i, len(recs), w.recs)
		}
	}
}

func TestClearLogKeepsSettings(t *testing.T) {
	h := newHarness(t)
	h.app.SetShowDebug(true)
	h.app.feedLine(lineWarn)

	h.app.ClearLog()

	if n := len(h.app.visible); n != 0 {
		t.Errorf("после очистки осталось %d записей", n)
	}
	if !h.app.ui.ShowDebug {
		t.Error("очистка лога сбросила настройку показа DEBUG")
	}
}

func TestBootstrapReportsStatus(t *testing.T) {
	h := newHarness(t)
	h.setPID(1234)
	h.app.feedLine(lineWarn)

	b := h.app.Bootstrap()
	if !b.Status.Alive || b.Status.PID != 1234 {
		t.Errorf("статус: alive=%v pid=%d, ожидалось true/1234", b.Status.Alive, b.Status.PID)
	}
	if len(b.Records) != 1 {
		t.Errorf("история: %d записей, ожидалась 1", len(b.Records))
	}
}
