package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"heroku-console/internal/telegram"
)

// press нажимает кнопку под сообщением 55 в личном чате администратора 1.
func press(control *telegramControl, data string) {
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "cb", From: telegram.User{ID: 1, Username: "anya"}, Data: data,
		Message: &telegram.Message{MessageID: 55, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
}

func lastAnswer(fake *fakeBotAPI) string {
	answers := fake.byMethod("answerCallbackQuery")
	if len(answers) == 0 {
		return ""
	}
	text, _ := answers[len(answers)-1]["text"].(string)
	return text
}

// Кнопки под статусом: журнал и ошибки открываются, устаревшая кнопка вежливо отвергается,
// а без сообщения (кнопка из inline-режима) бот не падает.
func TestStatusCallbacksJournalErrorsAndStale(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	logLines := "2026-10-11 10:00:00 [INFO] loader: старт\n2026-10-11 10:00:01 [ERROR] updates: сбой сети\nTraceback (most recent call last):\n  ValueError: x\n"
	if err := os.WriteFile(s.bot.LogFile, []byte(logLines), 0o600); err != nil {
		t.Fatal(err)
	}

	press(control, "st:l")
	if got := lastAnswer(fake); got != "Журнал" {
		t.Fatalf("ответ на «журнал»: %q", got)
	}
	if text := lastText(fake.byMethod("sendMessage")); !strings.Contains(text, "сбой сети") {
		t.Fatalf("журнал не показан: %q", text)
	}
	press(control, "st:e")
	if got := lastAnswer(fake); got != "Ошибки" {
		t.Fatalf("ответ на «ошибки»: %q", got)
	}
	press(control, "st:zzz")
	if got := lastAnswer(fake); got != "Кнопка устарела" {
		t.Fatalf("устаревшая кнопка: %q", got)
	}

	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "cb2", From: telegram.User{ID: 1}, Data: "st:r"}})
	if got := lastAnswer(fake); got != "Сообщение недоступно" {
		t.Fatalf("кнопка без сообщения: %q", got)
	}
}

// Кнопки под уведомлением об инциденте: тишина на час включается, неизвестная кнопка отвергается.
func TestIncidentButtonsMuteAndStale(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)

	press(control, "ic:mute")
	if !strings.Contains(lastAnswer(fake), "Тишина на час") {
		t.Fatalf("ответ: %q", lastAnswer(fake))
	}
	if _, active := control.mute.Active(); !active {
		t.Fatal("тишина не включилась")
	}
	press(control, "ic:zzz")
	if got := lastAnswer(fake); got != "Кнопка устарела" {
		t.Fatalf("устаревшая кнопка: %q", got)
	}
}

// Экран «Ещё» в меню показывает настройки автообновления и закрепления.
func TestMenuMoreScreen(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	menuCallback(control, fake, t, "more")
	edit := lastEdit(t, fake)
	text, _ := edit["text"].(string)
	for _, part := range []string{"Настройки меню", "Автообновление", "Закреплено"} {
		if !strings.Contains(text, part) {
			t.Errorf("на экране «Ещё» нет %q: %s", part, text)
		}
	}
	data, _ := json.Marshal(edit["reply_markup"])
	for _, want := range []string{"mn:iv1", "mn:iv5", "mn:pin", "mn:m"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("на экране «Ещё» нет кнопки %s: %s", want, data)
		}
	}
}

// Кнопка обновления подключается к уведомлениям Telegram вместе с тишиной и инцидентами.
func TestAttachUpdateButtonWiresTelegramSink(t *testing.T) {
	_, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	s.dataDir = t.TempDir()
	sink := &telegramSink{}
	s.alerts = &alertDispatcher{sinks: []alertSink{sink}}
	control.attachUpdateButton()
	if sink.markup == nil || sink.mute == nil || sink.incidentMarkup == nil || sink.incidentsPath == "" {
		t.Fatalf("приёмник не подключён: %+v", sink)
	}
	// Без диспетчера уведомлений метод ничего не делает и не падает.
	s.alerts = nil
	control.attachUpdateButton()
}

// Настройки Telegram в админке: чтение, запись, отказ на лишние поля и на чужого.
func TestTelegramSettingsOverHTTP(t *testing.T) {
	f := newFlowServer(t)
	var err error
	f.operations, err = openOperationStore(f.dataDir + "/operations.json")
	if err != nil {
		t.Fatal(err)
	}
	want(t, "чтение без токена", f.do(t, flowRequest{method: "GET", path: "/api/admin/telegram"}), http.StatusUnauthorized)
	read := f.do(t, flowRequest{method: "GET", path: "/api/admin/telegram", admin: true})
	want(t, "чтение", read, http.StatusOK)
	if _, ok := decode(t, read)["settings"]; !ok {
		t.Fatalf("в ответе нет settings: %s", read.Body.String())
	}
	want(t, "запись без токена", f.do(t, flowRequest{method: "PUT", path: "/api/admin/telegram", body: `{"controlEnabled":true}`}), http.StatusUnauthorized)
	want(t, "лишнее поле", f.do(t, flowRequest{method: "PUT", path: "/api/admin/telegram", body: `{"evil":1}`, admin: true}), http.StatusBadRequest)
	write := f.do(t, flowRequest{method: "PUT", path: "/api/admin/telegram", body: `{"controlEnabled":true,"confirm":true}`, admin: true})
	want(t, "запись", write, http.StatusOK)
	if !f.operations.telegramSettings().ControlEnabled {
		t.Fatal("настройка не сохранилась")
	}
}
