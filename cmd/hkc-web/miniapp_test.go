package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// miniApp — сервер с ботом Telegram и сессией мини-приложения администратора 7.
type miniApp struct {
	s       *server
	control *telegramControl
	fake    *fakeBotAPI
	handler http.Handler
	token   string
}

func newMiniApp(t *testing.T) *miniApp {
	t.Helper()
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 7)
	control.config.WebAppURL = "https://panel.example.com"
	s.dataDir = t.TempDir()
	if err := s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	app := &miniApp{s: s, control: control, fake: fake, handler: s.routes()}
	session := startWebSession(t, app.handler, signInitData(testBotToken, 7, time.Now()))
	if session.Code != http.StatusOK {
		t.Fatalf("сессия: %d %s", session.Code, session.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(session.Body.Bytes(), &issued)
	app.token = issued.Token
	return app
}

func (app *miniApp) call(method, path, body, bearer string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, request)
	return response
}

func decodeBody(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("ответ не JSON: %s", response.Body.String())
	}
	return body
}

// TestMiniAppIsThePanel: мини-приложение — страница панели, а её API пускает сессию Telegram как оператора.
// Журнал при этом приходит с замаскированными секретами, а список процессов хоста скрыт.
func TestMiniAppIsThePanel(t *testing.T) {
	app := newMiniApp(t)
	journalFor(t, app.s)
	page := app.call("GET", "/tg/", "", "")
	index, _ := staticFiles.ReadFile("static/index.html")
	if page.Code != http.StatusOK || page.Body.String() != string(index) {
		t.Fatalf("страница мини-приложения: %d", page.Code)
	}
	me := decodeBody(t, app.call("GET", "/api/auth/me", "", app.token))
	if me["username"] != "Аня" || me["role"] != "operator" || me["share"] != true {
		t.Fatalf("кто вошёл: %v", me)
	}
	entries := app.call("GET", "/api/logs/entries?limit=50", "", app.token)
	if entries.Code != http.StatusOK || !strings.Contains(entries.Body.String(), "manifest failed") {
		t.Fatalf("журнал: %d %s", entries.Code, entries.Body.String())
	}
	if strings.Contains(entries.Body.String(), leakedSecret) {
		t.Fatal("секрет ушёл в мини-приложение")
	}
	details := decodeBody(t, app.call("GET", "/api/system/details", "", app.token))
	if details["processesHidden"] != true {
		t.Fatalf("процессы хоста видны мини-приложению: %v", details["processesHidden"])
	}
	// Чужая или пустая сессия — как отсутствие входа.
	for _, bearer := range []string{"wrong", ""} {
		if code := app.call("GET", "/api/status", "", bearer).Code; code != http.StatusUnauthorized {
			t.Fatalf("сессия %q: %d", bearer, code)
		}
	}
	// Неизвестное действие отвергается до обращения к процессу бота.
	if code := app.call("POST", "/api/bot/explode", "", app.token).Code; code == http.StatusOK {
		t.Fatal("неизвестное действие выполнено")
	}
}

// TestShareLogEntry: запись уходит в личный чат администратора, текст берётся на сервере и маскируется.
func TestShareLogEntry(t *testing.T) {
	app := newMiniApp(t)
	journalFor(t, app.s)
	var page struct {
		Entries []struct {
			TS      int64    `json:"ts"`
			ID      string   `json:"id"`
			Message string   `json:"message"`
			Trace   []string `json:"trace"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(app.call("GET", "/api/logs/entries?q=manifest%20failed%202", "", app.token).Body.Bytes(), &page)
	if len(page.Entries) != 1 {
		t.Fatalf("запись для отправки: %+v", page.Entries)
	}
	entry := page.Entries[0]
	body, _ := json.Marshal(map[string]any{"ts": entry.TS, "id": entry.ID})
	response := app.call("POST", "/api/logs/share", string(body), app.token)
	if response.Code != http.StatusOK {
		t.Fatalf("отправка: %d %s", response.Code, response.Body.String())
	}
	sent := lastSent(t, app.fake)
	text, _ := sent["text"].(string)
	if sent["chat_id"] != float64(7) || !strings.Contains(text, "manifest failed 2") || !strings.Contains(text, "🔴 <b>ERROR</b>") {
		t.Fatalf("сообщение: %v", sent)
	}
	if strings.Contains(text, leakedSecret) {
		t.Fatal("секрет ушёл в Telegram")
	}
	body, _ = json.Marshal(map[string]any{"ts": entry.TS, "id": "нет-такой"})
	if code := app.call("POST", "/api/logs/share", string(body), app.token).Code; code != http.StatusNotFound {
		t.Fatalf("несуществующая запись: %d", code)
	}
}

// TestTelegramPageSharedWithAdmin: страница Telegram одна для админки и мини-приложения; главный
// переключатель из мини-приложения не меняется, тишина и автовосстановление — меняются.
func TestTelegramPageSharedWithAdmin(t *testing.T) {
	app := newMiniApp(t)
	app.s.adminToken = "admin-token"
	fromTelegram := decodeBody(t, app.call("GET", "/api/telegram", "", app.token))
	fromAdmin := decodeBody(t, app.call("GET", "/api/telegram", "", "admin-token"))
	if fromTelegram["viewer"].(map[string]any)["telegram"] != true || fromAdmin["viewer"].(map[string]any)["telegram"] != false {
		t.Fatalf("кто смотрит: %v / %v", fromTelegram["viewer"], fromAdmin["viewer"])
	}
	for _, key := range []string{"mute", "watchdog", "update", "digest", "commands", "activity"} {
		if fromTelegram[key] == nil || fromAdmin[key] == nil {
			t.Fatalf("на странице нет %q", key)
		}
	}
	if code := app.call("GET", "/api/telegram", "", "").Code; code != http.StatusUnauthorized {
		t.Fatalf("без входа: %d", code)
	}

	if code := app.call("PUT", "/api/telegram", `{"controlEnabled":false,"confirm":true}`, app.token).Code; code != http.StatusForbidden {
		t.Fatalf("выключение управления из мини-приложения: %d", code)
	}
	if response := app.call("PUT", "/api/telegram", `{"controlEnabled":true,"confirm":false}`, app.token); response.Code != http.StatusOK {
		t.Fatalf("подтверждение: %d %s", response.Code, response.Body.String())
	}
	if settings := app.s.operations.telegramSettings(); settings.Confirm || !settings.ControlEnabled {
		t.Fatalf("настройки: %+v", settings)
	}

	mute := decodeBody(t, app.call("POST", "/api/telegram/mute", `{"for":"3h","all":true}`, app.token))["mute"].(map[string]any)
	if state, ok := app.control.mute.Active(); !ok || !state.All || mute["active"] != true {
		t.Fatalf("тишина: %+v %v", state, mute)
	}
	app.call("POST", "/api/telegram/mute", `{"off":true}`, app.token)
	if _, ok := app.control.mute.Active(); ok {
		t.Fatal("тишина не снята")
	}
	if code := app.call("POST", "/api/telegram/mute", `{"for":"навсегда"}`, app.token).Code; code != http.StatusBadRequest {
		t.Fatalf("неизвестный срок: %d", code)
	}

	app.call("POST", "/api/telegram/watchdog", `{"enabled":false}`, app.token)
	if app.s.operations.watchdogSettings().Enabled {
		t.Fatal("автовосстановление не выключено")
	}
	if code := app.call("POST", "/api/telegram/watchdog", `{}`, app.token).Code; code != http.StatusBadRequest {
		t.Fatalf("пустой запрос к автовосстановлению: %d", code)
	}
	if code := app.call("POST", "/api/telegram/update", `{"version":"v0.0.1"}`, app.token).Code; code != http.StatusConflict {
		t.Fatalf("обновление без службы: %d", code)
	}

	// Действия попадают в журнал от имени администратора Telegram.
	actions := map[string]bool{}
	for _, event := range app.s.telegramActivity(20) {
		if event.Actor == "telegram:@anya" {
			actions[event.Action] = true
		}
	}
	for _, action := range []string{"telegram.settings", "telegram.mute", "telegram.unmute", "watchdog.update"} {
		if !actions[action] {
			t.Errorf("в журнале действий нет %s: %v", action, actions)
		}
	}
}

// TestDigestPage: страница «Сводка» получает те же данные, что сводка в Telegram.
func TestDigestPage(t *testing.T) {
	app := newMiniApp(t)
	journalFor(t, app.s)
	writePanelEvents(t, app.s, time.Now().Add(-2*time.Hour))
	view := decodeBody(t, app.call("GET", "/api/digest?span=24h", "", app.token))
	if view["label"] != "сутки" || view["crashes"] != float64(1) || view["errors"] != float64(3) {
		t.Fatalf("сводка: %v", view)
	}
	stops := view["stops"].([]any)
	first := stops[0].(map[string]any)
	if len(stops) != 2 || first["crash"] != true || first["downSeconds"] != float64(180) || !strings.Contains(first["reason"].(string), "ImportError") {
		t.Fatalf("остановки: %v", stops)
	}
	problems := view["problems"].([]any)
	if len(problems) == 0 || problems[0].(map[string]any)["query"] == "" {
		t.Fatalf("частые ошибки: %v", problems)
	}
	if strings.Contains(app.call("GET", "/api/digest", "", app.token).Body.String(), leakedSecret) {
		t.Fatal("секрет в сводке")
	}
	if week := decodeBody(t, app.call("GET", "/api/digest?span=168h", "", app.token)); week["label"] != "неделю" {
		t.Fatalf("неделя: %v", week["label"])
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	for at, want := range map[time.Time]string{now.Add(-time.Hour): "11:00", now.Add(-20 * time.Hour): "вчера 16:00", now.AddDate(0, 0, -3): "12 окт 12:00"} {
		if got := digestWhen(at, now); got != want {
			t.Errorf("digestWhen(%v) = %q, want %q", at, got, want)
		}
	}
}

// TestStatusExplainsStop: пока бот не работает, состояние объясняет почему — из памяти или из журнала панели.
func TestStatusExplainsStop(t *testing.T) {
	app := newMiniApp(t)
	writePanelEvents(t, app.s, time.Now().Add(-time.Hour))
	status := decodeBody(t, app.call("GET", "/api/status", "", app.token))
	stop, _ := status["lastStop"].(map[string]any)
	if status["running"] != false || stop["title"] != "Бот остановлен" || !strings.Contains(stop["message"].(string), "Остановил @anya") {
		t.Fatalf("по журналу панели: %v", status)
	}
	app.s.stopMu.Lock()
	app.s.lastStop = stopReport{At: time.Now(), Title: "Бот упал", Severity: "critical", Message: "MemoryError", Trace: []string{"Traceback (most recent call last):", "MemoryError"}}
	app.s.stopMu.Unlock()
	stop = decodeBody(t, app.call("GET", "/api/status", "", app.token))["lastStop"].(map[string]any)
	if stop["title"] != "Бот упал" || len(stop["trace"].([]any)) != 2 {
		t.Fatalf("из памяти: %v", stop)
	}
	// Запуск после остановки: журнал больше ничего не объясняет.
	app.s.panelLog.write(time.Now(), "INFO", modulePanel, "Бот запущен — Процесс Heroku работает.", nil)
	if got := app.s.stopFromPanelJournal(); got != nil {
		t.Fatalf("после запуска: %+v", got)
	}
	_ = os.Remove(app.s.panelLog.path)
}
