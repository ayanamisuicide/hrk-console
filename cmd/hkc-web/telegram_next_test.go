package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heroku-console/internal/telegram"
)

// TestChatKeyboardSentOnce: клавиатура под полем ввода приходит в личный чат один раз, переживает
// перезапуск панели и не приходит в группу.
func TestChatKeyboardSentOnce(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	path := filepath.Join(t.TempDir(), chatKeyboardFile)
	control.keyboard = newChatKeyboardStore(path)

	control.handleUpdate(message(1, "статус", 0))
	sent := fake.byMethod("sendMessage")
	if len(sent) != 2 {
		t.Fatalf("ожидались клавиатура и статус: %v", sent)
	}
	markup, _ := json.Marshal(sent[0]["reply_markup"])
	for _, row := range chatKeyboardRows {
		for _, label := range row {
			if !strings.Contains(string(markup), label) {
				t.Fatalf("на клавиатуре нет %q: %s", label, markup)
			}
		}
	}
	if !strings.Contains(string(markup), `"is_persistent":true`) || !strings.Contains(lastText(sent), "Бот") {
		t.Fatalf("клавиатура или статус: %s / %v", markup, sent)
	}

	control.handleUpdate(message(1, "статус", 0))
	if got := len(fake.byMethod("sendMessage")); got != 3 {
		t.Fatalf("клавиатура пришла повторно: %d сообщений", got)
	}
	// После перезапуска панели состояние читается из файла.
	control.keyboard = newChatKeyboardStore(path)
	control.handleUpdate(message(1, "статус", 0))
	if got := len(fake.byMethod("sendMessage")); got != 4 {
		t.Fatalf("клавиатура пришла после перезапуска: %d сообщений", got)
	}
	control.handleUpdate(groupMessage(1, "@panel_bot статус"))
	for _, call := range fake.byMethod("sendMessage") {
		if call["chat_id"] == float64(-1001) && strings.Contains(call["text"].(string), "быстрые кнопки") {
			t.Fatal("клавиатура отправлена в группу")
		}
	}
}

// TestChatKeyboardLabelsAreUnderstood: кнопки отправляют текст, который разбирается в нужные действия.
func TestChatKeyboardLabelsAreUnderstood(t *testing.T) {
	want := map[string]telegram.IntentKind{
		"📋 Меню": telegram.IntentMenu, "📊 Статус": telegram.IntentStatus, "🔴 Ошибки": telegram.IntentErrors,
		"📜 Логи": telegram.IntentLogs, "🔁 Перезапуск": telegram.IntentRestart, "🔕 Тишина": telegram.IntentMute,
	}
	seen := 0
	for _, row := range chatKeyboardRows {
		for _, label := range row {
			it, ok := telegram.ParseIntent(label)
			if !ok || it.Kind != want[label] {
				t.Errorf("%q разобрано как %q, want %q", label, it.Kind, want[label])
			}
			seen++
		}
	}
	if seen != len(want) {
		t.Fatalf("на клавиатуре %d кнопок, проверено %d", seen, len(want))
	}
}

// TestEveryReplyOffersChoice: ни один ответ не оставляет без кнопок, и на сообщение без текста
// бот отвечает, а не молчит.
func TestEveryReplyOffersChoice(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	phrases := []string{"привет", "спасибо", "помощь", "абракадабра кукареку", "нет", "да", "включи уведомления",
		"тишина на час", "/nosuchcommand", "/find", "приложение"}
	for _, phrase := range phrases {
		before := len(fake.byMethod("sendMessage"))
		control.handleUpdate(message(1, phrase, 0))
		sent := fake.byMethod("sendMessage")
		if len(sent) == before {
			t.Fatalf("%q: бот промолчал", phrase)
		}
		var keyboard struct {
			Rows [][]map[string]any `json:"inline_keyboard"`
		}
		data, _ := json.Marshal(sent[len(sent)-1]["reply_markup"])
		if json.Unmarshal(data, &keyboard) != nil || len(keyboard.Rows) == 0 {
			t.Fatalf("%q: ответ без кнопок: %v", phrase, sent[len(sent)-1])
		}
	}
	before := len(fake.byMethod("sendMessage"))
	control.handleUpdate(message(1, "", 0)) // стикер или фото: текста нет
	sent := fake.byMethod("sendMessage")
	if len(sent) != before+1 || !strings.Contains(lastText(sent), "понимаю текст") {
		t.Fatalf("сообщение без текста: %v", sent[before:])
	}
	callbackData(t, sent[len(sent)-1]["reply_markup"], "nx:s")
}

// TestNextStepButtons: кнопки следующего шага открывают статус, журнал, ошибки, меню и справку.
func TestNextStepButtons(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	cases := []struct{ data, answer, text string }{
		{"nx:s", "Статус", "Бот остановлен"},
		{"nx:h", "Помощь", "Управление Heroku"},
		{"nx:m", "Меню", ""},
	}
	for _, c := range cases {
		before := len(fake.byMethod("sendMessage"))
		press(control, c.data)
		sent := fake.byMethod("sendMessage")
		if lastAnswer(fake) != c.answer || len(sent) <= before || !strings.Contains(lastText(sent), c.text) {
			t.Fatalf("%s: ответ %q, сообщения %v", c.data, lastAnswer(fake), sent[before:])
		}
	}
	callbackData(t, lastSent(t, fake)["reply_markup"], "mn:more") // меню — с кнопками своих экранов
	press(control, "nx:zz")
	if lastAnswer(fake) != "Кнопка устарела" {
		t.Fatalf("неизвестная кнопка: %q", lastAnswer(fake))
	}
}

// TestNextStepsInGroup: в группе меню не предлагается, а обращение по имени без слов — не тишина.
func TestNextStepsInGroup(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.mu.Lock()
	control.bot = &telegram.User{ID: 42, IsBot: true, Username: "panel_bot"}
	control.mu.Unlock()
	control.handleUpdate(groupMessage(1, "@panel_bot"))
	reply := lastSent(t, fake)
	if !strings.Contains(reply["text"].(string), "Слушаю") {
		t.Fatalf("обращение без слов: %v", reply)
	}
	data, _ := json.Marshal(reply["reply_markup"])
	if strings.Contains(string(data), "nx:m") || !strings.Contains(string(data), "nx:s") {
		t.Fatalf("кнопки в группе: %s", data)
	}
	// Реплики людей без обращения бот по-прежнему не трогает.
	before := len(fake.byMethod("sendMessage"))
	control.handleUpdate(groupMessage(1, "статус"))
	if len(fake.byMethod("sendMessage")) != before {
		t.Fatal("бот ответил в группе без обращения")
	}
}

// TestConfirmOutcomeOffersNextStep: после подтверждения или отмены под итогом — следующий шаг.
func TestConfirmOutcomeOffersNextStep(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "/restart", 0))
	cancel := callbackData(t, lastSent(t, fake)["reply_markup"], "")
	cancel = "no:" + strings.SplitN(cancel, ":", 2)[1]
	press(control, cancel)
	edit := lastEdit(t, fake)
	if edit["text"] != "Отменено." {
		t.Fatalf("отмена: %v", edit)
	}
	callbackData(t, edit["reply_markup"], "nx:s")

	// Устаревшая кнопка подтверждения тоже не оставляет без выбора.
	press(control, cancel)
	edit = lastEdit(t, fake)
	if !strings.Contains(edit["text"].(string), "устарела") {
		t.Fatalf("устаревшая кнопка: %v", edit)
	}
	callbackData(t, edit["reply_markup"], "nx:h")
}

// TestStaleNoticeOncePerBatch: о пачке старых сообщений бот говорит один раз, а после паузы — снова.
func TestStaleNoticeOncePerBatch(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	now := time.Now()
	control.now = func() time.Time { return now }
	for range 3 {
		control.handleUpdate(message(1, "/restart", 10*time.Minute))
	}
	if sent := fake.byMethod("sendMessage"); len(sent) != 1 {
		t.Fatalf("на пачку старых сообщений: %d ответов", len(sent))
	}
	now = now.Add(staleNoticeEvery + time.Second)
	control.handleUpdate(message(1, "/restart", 30*time.Minute))
	if sent := fake.byMethod("sendMessage"); len(sent) != 2 {
		t.Fatalf("после паузы: %d ответов", len(sent))
	}
	// Старое сообщение в группе — не повод писать туда.
	group := groupMessage(1, "@panel_bot статус")
	group.Message.Date = now.Add(-time.Hour).Unix()
	before := len(fake.byMethod("sendMessage"))
	control.handleUpdate(group)
	if len(fake.byMethod("sendMessage")) != before {
		t.Fatal("ответ на старое сообщение в группе")
	}
}
