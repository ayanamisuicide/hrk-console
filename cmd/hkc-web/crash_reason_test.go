package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heroku-console/internal/telegram"
)

const crashOutput = `Heroku 2.3.0 started
обычная строка print()
Traceback (most recent call last):
  File "/home/u/Heroku/heroku/main.py", line 40, in run
    await self.load()
  File "/home/u/Heroku/heroku/loaded_modules/weather.py", line 12, in load
    import missing
ImportError: cannot import name 'x' from 'y' token=123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123
`

// TestCrashSummary: из traceback — строка исключения и место, из «Killed» — понятная причина.
func TestCrashSummary(t *testing.T) {
	summary, details, trace := crashSummary(strings.Split(crashOutput, "\n"))
	if !strings.HasPrefix(summary, "ImportError: cannot import name 'x'") || details != "в модуле weather · weather.py:12, в load" || len(trace) != 6 {
		t.Fatalf("traceback: %q / %q / %d строк", summary, details, len(trace))
	}
	// Цепочка исключений: важен последний traceback.
	chained := crashOutput + "\nDuring handling of the above exception, another exception occurred:\n\nTraceback (most recent call last):\n  File \"/srv/bot/core.py\", line 3, in main\nRuntimeError: всё сломалось\n"
	if summary, details, _ := crashSummary(strings.Split(chained, "\n")); summary != "RuntimeError: всё сломалось" || details != "core.py:3, в main" {
		t.Fatalf("цепочка: %q / %q", summary, details)
	}
	if summary, _, trace := crashSummary([]string{"работаю", "Killed", ""}); !strings.HasPrefix(summary, "Killed — процесс убит системой") || trace != nil {
		t.Fatalf("Killed: %q", summary)
	}
	if summary, _, _ := crashSummary([]string{"Fatal Python error: Segmentation fault"}); summary != "Fatal Python error: Segmentation fault" {
		t.Fatalf("фатальная ошибка: %q", summary)
	}
	if summary, _, _ := crashSummary([]string{"просто вывод", ""}); summary != "" {
		t.Fatalf("причина без причины: %q", summary)
	}
}

// TestExplainStopManual: недавняя остановка из аудита называет, кто и откуда; давняя не в счёт.
func TestExplainStopManual(t *testing.T) {
	s := newTestServer(t)
	s.audit = newAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"))
	now := time.Now()
	add := func(event auditEvent) {
		if err := s.audit.add(event); err != nil {
			t.Fatal(err)
		}
	}
	add(auditEvent{Time: now.Add(-10 * time.Minute).UTC(), Actor: "admin", Action: "bot.stop", IP: "127.0.0.1"})
	if report := s.explainStop(now); report.Title != "Бот упал" {
		t.Fatalf("давняя остановка объяснила падение: %+v", report)
	}
	cases := []struct {
		event       auditEvent
		title, want string
	}{
		{auditEvent{Actor: "telegram:@anya", Action: "bot.stop", IP: "telegram"}, "Бот остановлен", "Остановил @anya из Telegram"},
		{auditEvent{Actor: "telegram:@anya", Action: "bot.stop", IP: "10.0.0.5"}, "Бот остановлен", "@anya из мини-приложения Telegram"},
		{auditEvent{Actor: "admin", Action: "bot.stop"}, "Бот остановлен", "администратор из админки"},
		{auditEvent{Actor: "ivan", Action: "bot.restart"}, "Бот перезапускается", "Перезапуск: ivan из панели"},
		{auditEvent{Actor: "watchdog", Action: "bot.recover", Detail: "бот не отвечал 180 с"}, "Бот перезапускается", "бот не отвечал 180 с"},
	}
	for _, c := range cases {
		c.event.Time = now.Add(-20 * time.Second).UTC()
		add(c.event)
		report := s.explainStop(now)
		if report.Title != c.title || report.Severity != "warning" || !strings.Contains(report.Message, c.want) {
			t.Errorf("%+v: %+v", c.event, report)
		}
	}
}

// TestExplainStopFromOutput: причина из свежего вывода процесса без секретов, иначе — честное
// «не написал» с подсказкой.
func TestExplainStopFromOutput(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	if report := s.explainStop(now); !strings.Contains(report.Message, "запущен не из неё") {
		t.Fatalf("без вывода: %+v", report)
	}
	if err := os.WriteFile(s.bot.StartupLog, []byte(crashOutput), 0o600); err != nil {
		t.Fatal(err)
	}
	report := s.explainStop(now)
	first, rest, _ := strings.Cut(report.Message, "\n")
	if report.Title != "Бот упал" || report.Severity != "critical" || !strings.HasPrefix(first, "ImportError") || !strings.Contains(rest, "в модуле weather") {
		t.Fatalf("падение: %+v", report)
	}
	if strings.Contains(report.Message, "ABCDEFGHIJ") || strings.Contains(strings.Join(report.Trace, "\n"), "ABCDEFGHIJ") {
		t.Fatalf("секрет попал в уведомление: %+v", report)
	}
	if saved := s.lastStopReport(); len(saved.Trace) == 0 || !saved.At.Equal(now) {
		t.Fatalf("причина не запомнена: %+v", saved)
	}

	// Старый вывод к этой остановке не относится.
	old := now.Add(-time.Hour)
	if err := os.Chtimes(s.bot.StartupLog, old, old); err != nil {
		t.Fatal(err)
	}
	report = s.explainStop(now)
	if !strings.HasPrefix(report.Message, "Процесс завершился, не написав причину") || !strings.Contains(report.Message, "kill -9") || len(report.Trace) != 0 {
		t.Fatalf("старый вывод: %+v", report)
	}
	s.latestSystem = systemStatus{Supported: true, MemoryUsed: 96, MemoryTotal: 100}
	if report := s.explainStop(now); !strings.Contains(report.Message, "Память сервера занята на 96%") {
		t.Fatalf("подсказка про память: %+v", report)
	}
}

// TestStateNotifierExplainsStop: уведомление об остановке берёт заголовок, важность и текст из объяснения.
func TestStateNotifierExplainsStop(t *testing.T) {
	dispatcher, events := recordedAlerts()
	notifier := newStateNotifier(dispatcher)
	notifier.explain = func(time.Time) stopReport {
		return stopReport{Title: "Бот упал", Severity: "critical", Message: "ImportError: x"}
	}
	notifier.observe(true)
	notifier.observe(false)
	got := events()
	if len(got) != 1 || got[0].Kind != "bot.stopped" || got[0].Title != "Бот упал" || got[0].Message != "ImportError: x" {
		t.Fatalf("событие: %+v", got)
	}
}

// TestCrashIncidentTraceAndCause: под падением с traceback есть кнопка «Traceback», она присылает
// traceback, а после запуска в сообщении остаётся причина.
func TestCrashIncidentTraceAndCause(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	control.setState("running", "")
	if err := s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	sink := newTestSink(api.URL)
	sink.incidentMarkup = control.incidentMarkup
	if err := os.WriteFile(s.bot.StartupLog, []byte(crashOutput), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	report := s.explainStop(now)
	event := alertEvent{Kind: "bot.stopped", Severity: report.Severity, Title: report.Title, Message: report.Message, Time: now}
	if err := sink.send(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	opened := fake.byMethod("sendMessage")[0]
	if !strings.Contains(opened["text"].(string), "Бот упал") || !strings.Contains(opened["text"].(string), "ImportError") {
		t.Fatalf("уведомление: %v", opened["text"])
	}
	callbackData(t, opened["reply_markup"], "ic:tb")

	press(control, "ic:tb")
	trace := lastSent(t, fake)["text"].(string)
	if lastAnswer(fake) != "Traceback" || !strings.Contains(trace, "<pre>Traceback (most recent call last):") || !strings.Contains(trace, "weather.py") {
		t.Fatalf("traceback: %q / %q", lastAnswer(fake), trace)
	}

	if err := sink.send(t.Context(), started(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if text := lastEdit(t, fake)["text"].(string); !strings.Contains(text, "Причина остановки: ImportError: cannot import name") {
		t.Fatalf("причина после запуска: %q", text)
	}

	// Без traceback кнопки нет, а нажатие старой кнопки объясняет, где искать.
	s.stopMu.Lock()
	s.lastStop = stopReport{}
	s.stopMu.Unlock()
	if markup, _ := json.Marshal(control.incidentMarkup("bot", phaseOpen, time.Time{}, time.Time{})); strings.Contains(string(markup), "ic:tb") {
		t.Fatalf("кнопка Traceback без traceback: %s", markup)
	}
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "c", From: telegram.User{ID: 1}, Data: "ic:tb",
		Message: &telegram.Message{MessageID: 5, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	if !strings.Contains(lastAnswer(fake), "уже недоступен") {
		t.Fatalf("старая кнопка: %q", lastAnswer(fake))
	}
}
