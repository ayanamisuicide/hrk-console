package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	// aliveAt всегда "нет" — тесты меняют h.pid между тиками через setPID и
	// ожидают, что каждый тик увидит актуальное значение немедленно;
	// настоящий кэш (tickPID) не должен подтверждать устаревший lastPID по
	// реальному /proc, которого в тестовом окружении для этого pid и нет.
	a.aliveAt = func(int) bool { return false }
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
	h.app.maybeWatchdogRestart(h.app.tickPID())
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

// versionLess сравнивает по номерам, а не строками — посимвольное сравнение
// строк наврало бы на переходе через второй знак ("v1.9.0" < "v1.10.0" как
// строки — false, хотя релиз реально новее).
func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.7.6", "v1.7.7", true},
		{"v1.7.7", "v1.7.6", false},
		{"v1.7.6", "v1.7.6", false},
		{"v1.9.0", "v1.10.0", true},
		{"v2.0.0", "v1.99.99", false},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, ожидалось %v", c.a, c.b, got, c.want)
		}
	}
}

// cleanVersionRe отсеивает сборки из рабочего дерева (суффикс git describe,
// "dev") — для них сравнение версий ненадёжно, проверка обновлений должна
// промолчать, а не соврать "обновление доступно" на каждой dev-сборке.
func TestCleanVersionRe(t *testing.T) {
	for _, v := range []string{"v1.7.6", "v0.0.1"} {
		if !cleanVersionRe.MatchString(v) {
			t.Errorf("cleanVersionRe не совпал с чистым тегом %q", v)
		}
	}
	for _, v := range []string{"dev", "v1.7.6-3-gabc1234", "v1.7.6-dirty", "1.7.6"} {
		if cleanVersionRe.MatchString(v) {
			t.Errorf("cleanVersionRe ошибочно совпал с %q", v)
		}
	}
}

// findGUIAsset должен найти именно архив GUI, а не первый попавшийся ассет
// релиза — рядом в том же релизе лежит ещё и hkc-*, с другим содержимым.
func TestFindGUIAsset(t *testing.T) {
	rel := &ghRelease{Assets: []ghAsset{
		{Name: "hkc-v1.8.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example/hkc"},
		{Name: "hrk-console-gui-v1.8.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example/gui"},
	}}
	if got := findGUIAsset(rel); got != "https://example/gui" {
		t.Errorf("findGUIAsset вернул %q, ожидался gui-ассет", got)
	}
	if got := findGUIAsset(&ghRelease{}); got != "" {
		t.Errorf("findGUIAsset на пустом релизе вернул %q, ожидалась пустая строка", got)
	}
}

// downloadGUIBinary разбирает настоящий tar.gz (не заглушку) — важно
// проверить именно распаковку, а не то, что HTTP-клиент умеет ходить по
// URL. Архив собирается in-memory, без реального обращения к GitHub.
func TestDownloadGUIBinaryExtractsExecutable(t *testing.T) {
	want := []byte("fake-binary-content")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "hrk-console-gui", Mode: 0o755, Size: int64(len(want))}); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if _, err := tw.Write(want); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	path, err := downloadGUIBinary(srv.URL)
	if err != nil {
		t.Fatalf("downloadGUIBinary: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение результата: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("содержимое %q, ожидалось %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("результат должен быть исполняемым")
	}
}

func TestDownloadGUIBinaryFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := downloadGUIBinary(srv.URL); err == nil {
		t.Error("ожидалась ошибка на HTTP 404")
	}
}

// checkUpdateOnce молчит на dev-сборке (её версия — "dev", не тег) — не
// с чем сравнивать, а не "всегда есть обновление".
func TestCheckUpdateOnceSilentOnDevBuild(t *testing.T) {
	origVersion := version
	defer func() { version = origVersion }()
	version = "dev"

	h := newHarness(t)
	called := false
	h.app.emit = func(event string, data ...interface{}) {
		if event == "update-available" {
			called = true
		}
	}
	h.app.checkUpdateOnce() // не должен даже пытаться сходить в сеть
	if called {
		t.Error("dev-сборка не должна сообщать о доступном обновлении")
	}
}

func TestCheckUpdateOnceEmitsOnNewerRelease(t *testing.T) {
	origURL := updateCheckURL
	defer func() { updateCheckURL = origURL }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"https://example/release"}`))
	}))
	defer srv.Close()
	updateCheckURL = srv.URL

	origVersion := version
	defer func() { version = origVersion }()
	version = "v1.0.0"

	h := newHarness(t)
	var got []UpdateInfo
	h.app.emit = func(event string, data ...interface{}) {
		if event == "update-available" && len(data) == 1 {
			if info, ok := data[0].(UpdateInfo); ok {
				got = append(got, info)
			}
		}
	}

	h.app.checkUpdateOnce()

	if len(got) != 1 || got[0].Version != "v9.9.9" {
		t.Errorf("update-available = %+v, ожидался один эвент с v9.9.9", got)
	}
}

// tickPID кэширует pid между тиками, пока aliveAt подтверждает его — иначе
// вся оптимизация (не сканировать /proc заново на каждой секунде) ничего
// не даёт. bot/mu не нужны: tickPID их не трогает.
func TestTickPIDCachesWhileAlive(t *testing.T) {
	a := &App{}
	pidCalls := 0
	a.pid = func() int {
		pidCalls++
		return 4242
	}
	a.aliveAt = func(pid int) bool { return pid == 4242 }

	if got := a.tickPID(); got != 4242 || pidCalls != 1 {
		t.Fatalf("первый тик: pid=%d calls=%d, ожидалось 4242/1", got, pidCalls)
	}
	for i := 0; i < 5; i++ {
		if got := a.tickPID(); got != 4242 {
			t.Fatalf("тик %d: pid=%d, ожидалось 4242", i, got)
		}
	}
	if pidCalls != 1 {
		t.Errorf("полный обход (a.pid) вызван %d раз, пока бот жив на том же pid — ожидался 1", pidCalls)
	}

	// Бот упал — кэш обязан не соврать, что pid ещё жив, и вернуться к
	// полному обходу.
	a.aliveAt = func(int) bool { return false }
	a.pid = func() int {
		pidCalls++
		return 0
	}
	if got := a.tickPID(); got != 0 {
		t.Errorf("после падения бота tickPID вернул %d, ожидался 0", got)
	}
	if pidCalls != 2 {
		t.Errorf("после падения ожидался повторный полный обход, calls=%d", pidCalls)
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

// Окно, которое просто оставили открытым, копило показанные записи всю свою
// жизнь: обрезала их только пересборка при смене фильтра и кнопка очистки.
func TestFeedLineBoundsVisibleHistory(t *testing.T) {
	h := newHarness(t)

	// Записи заведомо разные, иначе они схлопнутся в одну и расти будет нечему.
	for i := 0; i < ringCapacity+2000; i++ {
		h.app.feedLine(fmt.Sprintf(
			"2026-08-08 00:13:04 [WARNING] heroku.modules.spotify: попытка %d", i))
	}

	if n := len(h.app.visible); n > ringCapacity+500 {
		t.Errorf("показанных записей %d, предел %d — история растёт без границы", n, ringCapacity+500)
	}
	if n := len(h.app.ring); n > ringCapacity+500 {
		t.Errorf("сырых строк %d, предел %d", n, ringCapacity+500)
	}

	// Обрезка идёт с начала: свежие записи должны остаться на месте.
	last := h.app.visible[len(h.app.visible)-1]
	if want := fmt.Sprintf("попытка %d", ringCapacity+2000-1); last.Lines[0] != want {
		t.Errorf("последняя запись %q, ожидалась %q", last.Lines[0], want)
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
