package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

const leakedSecret = "abcd1234efgh5678secretvalue"

// journalFor пишет журнал бота для сервера из теста. Время записей считается от «сейчас»:
// выборка «за 6 часов» не должна зависеть от даты запуска.
func journalFor(t *testing.T, s *server) {
	t.Helper()
	stamp := func(ago time.Duration) string { return time.Now().Add(-ago).Format("2006-01-02 15:04:05") }
	lines := []string{
		stamp(50*time.Minute) + " [INFO] heroku.core: started",
		stamp(40*time.Minute) + " [ERROR] heroku.updates: manifest failed 1",
		stamp(30*time.Minute) + " [ERROR] heroku.updates: manifest failed 2",
		"Traceback (most recent call last):",
		`  File "u.py", line 3, token=` + leakedSecret,
		stamp(20*time.Minute) + " [WARNING] net: slow reply",
		stamp(10*time.Minute) + " [ERROR] heroku.core: boom",
	}
	if err := os.WriteFile(s.bot.LogFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callbackFrom(markup any, t *testing.T, suffix string) tgUpdate {
	return tgUpdate{Callback: &tgCallback{ID: "cb", From: tgUser{ID: 1, Username: "anya"}, Data: callbackData(t, markup, suffix),
		Message: &tgMessage{MessageID: 77, Chat: tgChat{ID: 1, Type: "private"}}}}
}

// TestMaskSecrets — очевидные секреты не уходят в чат.
func TestMaskSecrets(t *testing.T) {
	cases := []string{
		"bot 123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123 failed",
		"Authorization: Bearer abcdefghijklmnop1234",
		"url https://user:hunter2@example.com/x",
		"api_key=sk-abcdefghijklmnopqrstuvwxyz",
		"password: hunter2hunter2",
		"hkc.abcdefghijklmnopqrstuv",
		"eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4",
		"hash " + strings.Repeat("a1", 24),
	}
	for _, in := range cases {
		out := maskSecrets(in)
		for _, secret := range []string{"ABCDEFGHIJKLMNOP", "abcdefghijklmnop1234", "hunter2", "sk-abcdefgh", "abcdefghijklmnopqrstuv", "SflKxwRJ", strings.Repeat("a1", 24)} {
			if strings.Contains(out, secret) {
				t.Errorf("%q → %q: утёк %q", in, out, secret)
			}
		}
	}
	if got := maskSecrets("обычный текст: 42 запроса за 3.5 с"); got != "обычный текст: 42 запроса за 3.5 с" {
		t.Errorf("безобидный текст изменился: %q", got)
	}
}

// TestTelegramLogsByWords — «покажи логи» даёт записи с кнопками, повторы собраны, секреты скрыты.
func TestTelegramLogsByWords(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	journalFor(t, s)

	control.handleUpdate(message(1, "покажи логи", 0))
	reply := lastSent(t, fake)
	text := reply["text"].(string)
	for _, want := range []string{"<pre>", "manifest failed 1", "slow reply", "boom", "[+2 стр.]"} {
		if !strings.Contains(text, want) {
			t.Errorf("в ответе нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, leakedSecret) {
		t.Error("секрет попал в сообщение")
	}
	for _, suffix := range []string{":fresh", ":errs", ":file"} {
		callbackData(t, reply["reply_markup"], suffix)
	}
}

// TestTelegramLogsOlderPages — «Старее» продолжает с того места, где закончилась страница.
func TestTelegramLogsOlderPages(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	journalFor(t, s)
	control.handleUpdate(message(1, "логи 2", 0))
	first := lastSent(t, fake)
	text := first["text"].(string)
	if !strings.Contains(text, "boom") || !strings.Contains(text, "slow reply") || strings.Contains(text, "manifest failed") {
		t.Fatalf("первая страница: %s", text)
	}
	control.handleUpdate(callbackFrom(first["reply_markup"], t, ":older"))
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("правок %d", len(edits))
	}
	older := edits[0]["text"].(string)
	if !strings.Contains(older, "manifest failed 2") || strings.Contains(older, "boom") {
		t.Fatalf("вторая страница: %s", older)
	}
}

// TestTelegramErrorsGrouped — ошибки собраны по причинам, traceback открывается кнопкой.
func TestTelegramErrorsGrouped(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	journalFor(t, s)
	control.handleUpdate(message(1, "что упало?", 0))
	reply := lastSent(t, fake)
	text := reply["text"].(string)
	// «manifest failed 1» и «… 2» отличаются числом — это одна причина.
	for _, want := range []string{"Ошибки", "×2", "updates", "boom", "2 разных"} {
		if !strings.Contains(text, want) {
			t.Errorf("в ответе нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "slow reply") {
		t.Error("предупреждение попало в ошибки")
	}
	// Группы идут от свежей: «boom» (10 мин назад) раньше «manifest» (30 мин).
	if strings.Index(text, "boom") > strings.Index(text, "manifest") {
		t.Errorf("порядок групп: %s", text)
	}

	// Traceback самой свежей записи группы, секрет скрыт.
	control.handleUpdate(callbackFrom(reply["reply_markup"], t, ":tb1"))
	trace := lastSent(t, fake)["text"].(string)
	if !strings.Contains(trace, "Traceback") || !strings.Contains(trace, "‹скрыто›") || strings.Contains(trace, leakedSecret) {
		t.Fatalf("traceback: %s", trace)
	}
}

// TestTelegramLogsByModuleWord — слово из фразы выбирает модуль по именам из журнала.
func TestTelegramLogsByModuleWord(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	journalFor(t, s)
	control.handleUpdate(message(1, "логи updates", 0))
	text := lastSent(t, fake)["text"].(string)
	if !strings.Contains(text, "manifest failed") || strings.Contains(text, "boom") || strings.Contains(text, "slow reply") {
		t.Fatalf("фильтр по модулю: %s", text)
	}
	if !strings.Contains(text, "heroku.updates") {
		t.Errorf("в заголовке нет модуля: %s", text)
	}
}

// TestTelegramSearch — поиск словами и догадка для непонятной фразы.
func TestTelegramSearch(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	journalFor(t, s)

	control.handleUpdate(message(1, "найди slow", 0))
	text := lastSent(t, fake)["text"].(string)
	if !strings.Contains(text, "slow reply") || strings.Contains(text, "boom") {
		t.Fatalf("поиск: %s", text)
	}
	// Поиск заглядывает и в traceback.
	control.handleUpdate(message(1, "найди u.py", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "manifest failed 2") {
		t.Fatalf("поиск по traceback: %s", lastSent(t, fake)["text"])
	}
	control.handleUpdate(message(1, "найди zzzqqq", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Ничего не нашёл") {
		t.Fatalf("пустой поиск: %s", lastSent(t, fake)["text"])
	}

	// Фраза, не похожая на команду, ищется как есть; не нашлось — честно «не понял».
	control.handleUpdate(message(1, "slow reply", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Не похоже на команду") {
		t.Fatalf("догадка: %s", lastSent(t, fake)["text"])
	}
	control.handleUpdate(message(1, "абракадабра", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Не понял") {
		t.Fatalf("непонятная фраза: %s", lastSent(t, fake)["text"])
	}
}

// TestTelegramLogFile — «Файлом» присылает документ с замаскированными секретами.
func TestTelegramLogFile(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	journalFor(t, s)
	control.handleUpdate(message(1, "покажи логи", 0))
	control.handleUpdate(callbackFrom(lastSent(t, fake)["reply_markup"], t, ":file"))
	bodies := fake.rawBodies("sendDocument")
	if len(bodies) != 1 {
		t.Fatalf("документов %d", len(bodies))
	}
	for _, want := range []string{"manifest failed 1", "slow reply", "Traceback", "‹скрыто›", `name="document"`, `.txt"`} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("в файле нет %q", want)
		}
	}
	if strings.Contains(bodies[0], leakedSecret) {
		t.Error("секрет попал в файл")
	}
}

// TestTelegramStaleLogButtons — кнопка со старым или неизвестным кодом не падает и не работает.
func TestTelegramStaleLogButtons(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "cb", From: tgUser{ID: 1}, Data: "lg:нет:older",
		Message: &tgMessage{MessageID: 1, Chat: tgChat{ID: 1, Type: "private"}}}})
	answers := fake.byMethod("answerCallbackQuery")
	if len(answers) != 1 || !strings.Contains(answers[0]["text"].(string), "устарела") {
		t.Fatalf("ответ на устаревшую кнопку: %v", answers)
	}
	// Чужой не получает ни журнала, ни файла.
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "cb2", From: tgUser{ID: 99}, Data: "lg:x:file",
		Message: &tgMessage{MessageID: 1, Chat: tgChat{ID: 1, Type: "private"}}}})
	if len(fake.byMethod("sendDocument")) != 0 {
		t.Fatal("чужой получил файл")
	}
}

// TestTelegramLogViewsAreBounded — память не растёт: старые просмотры вытесняются.
func TestTelegramLogViewsAreBounded(t *testing.T) {
	_, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	for i := 0; i < logViewMax*3; i++ {
		control.registerView(&logView{mode: "logs"})
	}
	control.mu.Lock()
	n := len(control.views)
	control.mu.Unlock()
	if n > logViewMax {
		t.Fatalf("просмотров %d, лимит %d", n, logViewMax)
	}
}
