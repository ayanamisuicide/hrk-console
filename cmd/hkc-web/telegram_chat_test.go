package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"heroku-console/internal/telegram"
)

// groupMessage — сообщение в группе.
func groupMessage(from int64, text string) telegram.Update {
	update := message(from, text, 0)
	update.Message.Chat = telegram.Chat{ID: -1001, Type: "supergroup"}
	return update
}

func lastSent(t *testing.T, fake *fakeBotAPI) map[string]any {
	t.Helper()
	sent := fake.byMethod("sendMessage")
	if len(sent) == 0 {
		t.Fatal("бот ничего не ответил")
	}
	return sent[len(sent)-1]
}

func callbackData(t *testing.T, markup any, suffix string) string {
	t.Helper()
	data, _ := json.Marshal(markup)
	var keyboard struct {
		Rows [][]struct {
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(data, &keyboard); err != nil {
		t.Fatal(err)
	}
	for _, row := range keyboard.Rows {
		for _, button := range row {
			if strings.HasSuffix(button.Data, suffix) {
				return button.Data
			}
		}
	}
	t.Fatalf("нет кнопки …%s в %s", suffix, data)
	return ""
}

// TestTelegramStreamsRepliesInPrivateChat — ответ набирается черновиком, а в конце приходит обычным сообщением.
func TestTelegramStreamsRepliesInPrivateChat(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "помощь", 0))

	drafts := fake.byMethod("sendMessageDraft")
	if len(drafts) < 3 {
		t.Fatalf("шагов набора %d, ожидали несколько", len(drafts))
	}
	final := lastSent(t, fake)["text"].(string)
	previous := 0
	for i, draft := range drafts {
		if draft["draft_id"] != drafts[0]["draft_id"] || draft["draft_id"].(float64) == 0 {
			t.Fatalf("шаг %d: draft_id %v, должен быть один и ненулевой", i, draft["draft_id"])
		}
		text := draft["text"].(string)
		if n := telegram.HTMLVisibleLen(text); n < previous || n > telegram.HTMLVisibleLen(final) {
			t.Fatalf("шаг %d: текст растёт неправильно (%d после %d из %d)", i, n, previous, telegram.HTMLVisibleLen(final))
		}
		previous = telegram.HTMLVisibleLen(text)
		if !strings.HasPrefix(final, strings.SplitN(text, "</", 2)[0][:min(len(text), 3)]) {
			t.Fatalf("шаг %d: черновик не начало ответа: %q", i, text)
		}
	}
	if !strings.Contains(final, "Пишите словами") || len(fake.byMethod("sendMessage")) != 1 {
		t.Fatalf("итоговое сообщение: %q", final)
	}
}

// TestTelegramThinksWhileWorking — для работы с ожиданием сначала показывается «Думаю…».
func TestTelegramThinksWhileWorking(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "статус", 0))
	drafts := fake.byMethod("sendMessageDraft")
	if len(drafts) == 0 || drafts[0]["text"] != "" {
		t.Fatalf("первым должен идти пустой черновик «Думаю…»: %v", drafts)
	}
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Бот остановлен") {
		t.Fatalf("статус: %v", lastSent(t, fake))
	}
}

// TestTelegramNoStreamInGroups — в группах черновиков нет, ответ обычный.
func TestTelegramNoStreamInGroups(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.mu.Lock()
	control.bot = &telegram.User{ID: 42, Username: "panel_bot", IsBot: true}
	control.mu.Unlock()
	control.handleUpdate(groupMessage(1, "@panel_bot помощь"))
	if len(fake.byMethod("sendMessageDraft")) != 0 {
		t.Fatal("в группе отправлены черновики")
	}
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("в группе нет ответа")
	}
}

// TestTelegramStreamFallback — если Telegram отверг черновики, ответы идут обычными сообщениями и
// анимацию больше не пробуют.
func TestTelegramStreamFallback(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	fake.setFail("sendMessageDraft", true)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "помощь", 0))
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("ответ потерян из-за отказа черновиков")
	}
	tried := len(fake.byMethod("sendMessageDraft"))
	if tried == 0 || !control.draftOff.Load() {
		t.Fatalf("после отказа анимация должна отключиться (попыток %d)", tried)
	}
	control.handleUpdate(message(1, "помощь", 0))
	if len(fake.byMethod("sendMessageDraft")) != tried || len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("отключённая анимация всё равно пробуется")
	}
}

// TestTelegramFreeTextControl — действия словами, подтверждение словом «да» и отказ на «не».
func TestTelegramFreeTextControl(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)

	control.handleUpdate(message(1, "останови бота", 0))
	ask := lastSent(t, fake)
	if !strings.Contains(ask["text"].(string), "Остановить бота?") {
		t.Fatalf("нет вопроса: %v", ask)
	}
	callbackData(t, ask["reply_markup"], "") // кнопки подтверждения есть

	control.handleUpdate(message(1, "да", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "уже остановлен") {
		t.Fatalf("действие не выполнено: %v", lastSent(t, fake))
	}
	events, _ := s.audit.recent(10)
	if len(events) != 1 || events[0].Action != "bot.stop" || events[0].Actor != "telegram:@anya" {
		t.Fatalf("журнал действий: %+v", events)
	}

	// Повторное «да» ничего не делает: подтверждение одноразовое.
	control.handleUpdate(message(1, "да", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Подтверждать нечего") {
		t.Fatalf("повторное да: %v", lastSent(t, fake))
	}

	// «Не останавливай» — это отказ, а не команда остановки.
	control.handleUpdate(message(1, "не останавливай", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "ничего не делаю") {
		t.Fatalf("отрицание: %v", lastSent(t, fake))
	}
	// «Нет» снимает вопрос в ожидании.
	control.handleUpdate(message(1, "останови", 0))
	control.handleUpdate(message(1, "нет", 0))
	control.handleUpdate(message(1, "да", 0))
	if events, _ := s.audit.recent(10); len(events) != 1 {
		t.Fatalf("после «нет» действие всё же выполнилось: %+v", events)
	}
}

// TestTelegramGroupNeedsMention — в группе обычные слова без обращения к боту игнорируются.
func TestTelegramGroupNeedsMention(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.mu.Lock()
	control.bot = &telegram.User{ID: 42, Username: "panel_bot", IsBot: true}
	control.mu.Unlock()
	control.handleUpdate(groupMessage(1, "статус"))
	control.handleUpdate(groupMessage(1, "останови бота"))
	if len(fake.byMethod("sendMessage")) != 0 {
		t.Fatalf("бот отозвался на разговор людей: %v", fake.byMethod("sendMessage"))
	}
	control.handleUpdate(groupMessage(1, "@panel_bot статус"))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Бот остановлен") {
		t.Fatalf("на обращение нет ответа: %v", fake.byMethod("sendMessage"))
	}
	// Чужие по-прежнему не получают ничего.
	control.handleUpdate(groupMessage(99, "@panel_bot статус"))
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("чужому ответили")
	}
}

// TestTelegramStatusKeyboardAndRefresh — статус с кнопками; «Обновить» правит то же сообщение.
func TestTelegramStatusKeyboardAndRefresh(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "статус", 0))
	markup := lastSent(t, fake)["reply_markup"]
	for _, suffix := range []string{":r", ":l", ":e", ":run"} {
		callbackData(t, markup, suffix)
	}
	refresh := callbackData(t, markup, ":r")
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "c1", From: telegram.User{ID: 1}, Data: refresh,
		Message: &telegram.Message{MessageID: 55, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 || edits[0]["message_id"].(float64) != 55 || !strings.Contains(edits[0]["text"].(string), "Бот остановлен") {
		t.Fatalf("обновление статуса: %v", edits)
	}
	// Telegram не принимает правку без изменений: это не сбой, и нового сообщения быть не должно.
	fake.mu.Lock()
	fake.failDescription = "Bad Request: message is not modified"
	fake.mu.Unlock()
	fake.setFail("editMessageText", true)
	sentBefore := len(fake.byMethod("sendMessage"))
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "c2", From: telegram.User{ID: 1}, Data: refresh,
		Message: &telegram.Message{MessageID: 55, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	if len(fake.byMethod("sendMessage")) != sentBefore {
		t.Fatal("правка без изменений породила новое сообщение")
	}
	// Любая другая ошибка правки (сообщение удалено) допускает новое сообщение.
	fake.mu.Lock()
	fake.failDescription = "Bad Request: message to edit not found"
	fake.mu.Unlock()
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "c3", From: telegram.User{ID: 1}, Data: refresh,
		Message: &telegram.Message{MessageID: 55, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	if len(fake.byMethod("sendMessage")) != sentBefore+1 {
		t.Fatal("при недоступном сообщении статус потерялся")
	}
}

// TestTelegramMuteByWords — «тишина на 2 часа» глушит уведомления, «включи уведомления» возвращает.
func TestTelegramMuteByWords(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "тишина на 2 часа", 0))
	state, ok := control.mute.Active()
	if !ok || state.All {
		t.Fatalf("тишина: %+v %t", state, ok)
	}
	if until := time.Until(state.Until); until < 119*time.Minute || until > 121*time.Minute {
		t.Fatalf("срок тишины %v", until)
	}
	reply := lastSent(t, fake)
	if !strings.Contains(reply["text"].(string), "Тишина на 2 ч") || !strings.Contains(reply["text"].(string), "Критичные") {
		t.Fatalf("ответ: %v", reply["text"])
	}
	callbackData(t, reply["reply_markup"], "mu:off")
	if events, _ := s.audit.recent(10); len(events) != 1 || events[0].Action != "telegram.mute" {
		t.Fatalf("журнал действий: %+v", events)
	}

	control.handleUpdate(message(1, "выключи уведомления совсем на час", 0))
	if state, _ := control.mute.Active(); !state.All {
		t.Fatalf("«совсем»: %+v", state)
	}
	control.handleUpdate(message(1, "включи уведомления", 0))
	if _, ok := control.mute.Active(); ok {
		t.Fatal("уведомления не включились")
	}
	// Кнопка «Включить» под ответом тоже снимает тишину.
	control.handleUpdate(message(1, "/mute 30m", 0))
	if _, ok := control.mute.Active(); !ok {
		t.Fatal("/mute 30m не сработала")
	}
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "c1", From: telegram.User{ID: 1}, Data: "mu:off",
		Message: &telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	if _, ok := control.mute.Active(); ok {
		t.Fatal("кнопка не сняла тишину")
	}
}
