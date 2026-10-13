package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heroku-console/internal/telegram"
)

func newTestSink(apiURL string) *telegramSink {
	return &telegramSink{token: testBotToken, chatID: "7", apiBase: apiURL, client: &http.Client{Timeout: 5 * time.Second}}
}

func stopped(at time.Time) alertEvent {
	return alertEvent{Kind: "bot.stopped", Severity: "critical", Title: "Бот остановлен", Message: "Процесс Heroku не найден.", Time: at}
}

func started(at time.Time) alertEvent {
	return alertEvent{Kind: "bot.started", Severity: "ok", Title: "Бот запущен", Message: "Процесс Heroku работает.", Time: at}
}

// TestIncidentOpensAndResolvesInOneMessage — падение и запуск бота — одно сообщение.
func TestIncidentOpensAndResolvesInOneMessage(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	sink := newTestSink(api.URL)
	t0 := time.Now().Add(-time.Hour)
	if err := sink.send(t.Context(), stopped(t0)); err != nil {
		t.Fatal(err)
	}
	sent := fake.byMethod("sendMessage")
	if len(sent) != 1 || !strings.Contains(sent[0]["text"].(string), "Бот остановлен") {
		t.Fatalf("падение: %v", sent)
	}
	if err := sink.send(t.Context(), started(t0.Add(130*time.Second))); err != nil {
		t.Fatal(err)
	}
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("восстановление пришло новым сообщением")
	}
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("правок %d", len(edits))
	}
	text := edits[0]["text"].(string)
	if !strings.Contains(text, "Бот запущен") || !strings.Contains(text, "Простоял 2 мин 10 с") {
		t.Fatalf("текст после восстановления: %q", text)
	}
	if edits[0]["message_id"].(float64) == 0 {
		t.Fatal("правка без номера сообщения")
	}
	// Без кнопок управления прежние кнопки снимаются, а не остаются под «запущен».
	if markup, ok := edits[0]["reply_markup"].(map[string]any); !ok || len(markup["inline_keyboard"].([]any)) != 0 {
		t.Fatalf("клавиатура после восстановления: %v", edits[0]["reply_markup"])
	}

	// Следующее падение — снова новое сообщение: прежний инцидент закрыт.
	if err := sink.send(t.Context(), stopped(t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("новое падение не открыло новое сообщение")
	}
}

// TestIncidentButtons — кнопки действий появляются, пока инцидент открыт, и «Что случилось» после него.
func TestIncidentButtons(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.setState("running", "")
	if err := control.s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	sink := newTestSink(api.URL)
	sink.incidentMarkup = control.incidentMarkup
	t0 := time.Now().Add(-time.Hour)
	_ = sink.send(t.Context(), stopped(t0))
	opened := fake.byMethod("sendMessage")[0]["reply_markup"]
	callbackData(t, opened, "ic:run")
	callbackData(t, opened, "st:l")

	_ = sink.send(t.Context(), started(t0.Add(time.Minute)))
	closed := fake.byMethod("editMessageText")[0]["reply_markup"]
	data := callbackData(t, closed, "")
	if !strings.HasPrefix(data, "rg:") {
		t.Fatalf("кнопка после восстановления: %q", data)
	}

	// Нажатие «Что случилось» показывает журнал вокруг инцидента.
	journalFor(t, control.s)
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "c", From: telegram.User{ID: 1}, Data: data,
		Message: &telegram.Message{MessageID: 5, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	if len(fake.byMethod("answerCallbackQuery")) == 0 || len(fake.byMethod("sendMessage")) < 2 {
		t.Fatalf("журнал за период не показан: %v", fake.byMethod("sendMessage"))
	}

	// Без включённого управления сообщение идёт без кнопок: их нажатие никто бы не обработал.
	control.setState("off", "")
	if control.incidentMarkup("bot", phaseOpen, time.Time{}, time.Time{}) != nil {
		t.Fatal("кнопки предложены при выключенном управлении")
	}
}

// TestIncidentRepeatsAreCounted — повторы ошибки модуля правят одно сообщение со счётчиком.
func TestIncidentRepeatsAreCounted(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	sink := newTestSink(api.URL)
	t0 := time.Now().Add(-10 * time.Minute)
	event := func(i int, title string) alertEvent {
		return alertEvent{Kind: "module.error", Severity: "warning", Title: title, Message: "ValueError: bad", Time: t0.Add(time.Duration(i) * time.Minute)}
	}
	for i := 0; i < 3; i++ {
		if err := sink.send(t.Context(), event(i, "Модуль Weather упал")); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(fake.byMethod("sendMessage")); n != 1 {
		t.Fatalf("сообщений %d, ожидали одно", n)
	}
	edits := fake.byMethod("editMessageText")
	if len(edits) != 2 || !strings.Contains(edits[0]["text"].(string), "×2") || !strings.Contains(edits[1]["text"].(string), "×3") {
		t.Fatalf("счётчик: %v", edits)
	}
	// Другая ошибка — другое сообщение.
	_ = sink.send(t.Context(), event(3, "Модуль Music упал"))
	if len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("разные ошибки склеились")
	}
	// Повтор спустя долгое время — снова новое сообщение, а не правка старого.
	late := event(0, "Модуль Weather упал")
	late.Time = t0.Add(3 * time.Hour)
	_ = sink.send(t.Context(), late)
	if len(fake.byMethod("sendMessage")) != 3 {
		t.Fatal("давний повтор правит устаревшее сообщение")
	}
}

// TestIncidentMuted — тишина глушит обычные события, критичные приходят.
func TestIncidentMuted(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	sink := newTestSink(api.URL)
	now := time.Now()
	sink.mute = telegram.NewMuteStore("", func() time.Time { return now })
	if err := sink.mute.Set(now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	_ = sink.send(t.Context(), alertEvent{Kind: "module.error", Severity: "warning", Title: "Модуль Weather упал", Time: now})
	_ = sink.send(t.Context(), alertEvent{Kind: "update.available", Severity: "info", Title: "Доступна версия"})
	if len(fake.byMethod("sendMessage")) != 0 {
		t.Fatalf("обычные события пробили тишину: %v", fake.byMethod("sendMessage"))
	}
	_ = sink.send(t.Context(), stopped(now))
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("критичное событие не пришло")
	}
	// Восстановление правит уже показанное сообщение, даже во время тишины: правка не звенит.
	_ = sink.send(t.Context(), started(now.Add(time.Minute)))
	if len(fake.byMethod("editMessageText")) != 1 {
		t.Fatal("сообщение об инциденте не обновилось")
	}
	// «Совсем» глушит и критичное; восстановление при этом ничего не присылает.
	_ = sink.mute.Set(now.Add(time.Hour), true)
	_ = sink.send(t.Context(), stopped(now.Add(2*time.Minute)))
	_ = sink.send(t.Context(), started(now.Add(3*time.Minute)))
	if len(fake.byMethod("sendMessage")) != 1 || len(fake.byMethod("editMessageText")) != 1 {
		t.Fatal("полная тишина пропустила события")
	}
}

// TestIncidentSurvivesRestart — открытый инцидент помнится после перезапуска панели.
func TestIncidentSurvivesRestart(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	path := filepath.Join(t.TempDir(), "incidents.json")
	first := newTestSink(api.URL)
	first.incidentsPath = path
	t0 := time.Now().Add(-time.Hour)
	_ = first.send(t.Context(), stopped(t0))

	second := newTestSink(api.URL)
	second.incidentsPath = path
	_ = second.send(t.Context(), started(t0.Add(time.Minute)))
	if len(fake.byMethod("sendMessage")) != 1 || len(fake.byMethod("editMessageText")) != 1 {
		t.Fatalf("после перезапуска инцидент потерян: send=%d edit=%d", len(fake.byMethod("sendMessage")), len(fake.byMethod("editMessageText")))
	}
	// Устаревший инцидент (сутки и больше) не правится: сообщение давно ушло вверх.
	stale := newTestSink(api.URL)
	stale.incidentsPath = filepath.Join(t.TempDir(), "old.json")
	stale.now = func() time.Time { return time.Now() }
	stale.open = map[string]*openIncident{"bot": {Message: 3, Start: t0.Add(-48 * time.Hour), Last: t0.Add(-48 * time.Hour), Count: 1}}
	_ = stale.send(t.Context(), stopped(time.Now()))
	if len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("давно устаревший инцидент был принят за открытый")
	}
}

// TestIncidentFallsBackToNewMessage — если сообщение править нельзя, о восстановлении сообщают новым.
func TestIncidentFallsBackToNewMessage(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	sink := newTestSink(api.URL)
	t0 := time.Now().Add(-time.Hour)
	_ = sink.send(t.Context(), stopped(t0))
	fake.setFail("editMessageText", true)
	_ = sink.send(t.Context(), started(t0.Add(time.Minute)))
	if len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("восстановление потерялось, когда сообщение нельзя править")
	}
}

// TestHumanDuration — подписи длительности.
func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second: "45 с", 130 * time.Second: "2 мин 10 с", 5 * time.Minute: "5 мин", 25*time.Minute + 30*time.Second: "25 мин",
		3*time.Hour + 5*time.Minute: "3 ч 5 мин",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("%v → %q, ожидали %q", d, got, want)
		}
	}
}
