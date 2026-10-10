package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// updateTelegram готовит сервер обновлений с ботом, слушающим подменённый Bot API.
func updateTelegram(t *testing.T) (*fakeBotAPI, *server, *telegramControl, *atomic.Value) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("установка из панели работает только в Linux")
	}
	fake, api := newFakeBotAPI(t)
	s := updateTestServer(t)
	s.audit = newAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"))
	var err error
	s.operations, err = openOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HKC_UPDATE_ENABLED", "1")
	previous := startUpdateService
	startUpdateService = func(context.Context) error { return nil }
	t.Cleanup(func() { startUpdateService = previous })
	control := newTelegramControl(s, telegramConfig{Token: testBotToken, APIBase: api.URL, Admins: []int64{1}})
	s.telegram = control
	installed := &atomic.Value{}
	installed.Store("v1.0.0")
	control.installed = func() string { return installed.Load().(string) }
	control.updatePoll = 20 * time.Millisecond
	return fake, s, control, installed
}

func lastText(calls []map[string]any) string {
	if len(calls) == 0 {
		return ""
	}
	text, _ := calls[len(calls)-1]["text"].(string)
	return text
}

// TestTelegramUpdateButtonOnlyWhenBotListens не предлагает кнопку, если нажатие никто не обработает.
func TestTelegramUpdateButtonOnlyWhenBotListens(t *testing.T) {
	_, _, control, _ := updateTelegram(t)
	event := alertEvent{Kind: "update.available", Version: "v9.0.0"}
	if control.updateMarkup(event) != nil {
		t.Fatal("button offered while control is off")
	}
	control.setState("running", "")
	if err := control.s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	keyboard := control.updateMarkup(event)
	if keyboard == nil {
		t.Fatal("no button for a running bot")
	}
	data, _ := json.Marshal(keyboard)
	if !strings.Contains(string(data), `"callback_data":"upd:v9.0.0"`) {
		t.Fatalf("keyboard: %s", data)
	}
	if control.updateMarkup(alertEvent{Kind: "bot.stopped", Version: "v9.0.0"}) != nil || control.updateMarkup(alertEvent{Kind: "update.available", Version: "main"}) != nil {
		t.Fatal("button for the wrong event or an invalid version")
	}
}

// TestTelegramUpdateFlow ставит обновление по кнопке и пишет итог в то же сообщение.
func TestTelegramUpdateFlow(t *testing.T) {
	fake, s, control, installed := updateTelegram(t)
	press := func(from int64, data string) {
		control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c", From: tgUser{ID: from, Username: "anya"}, Data: data,
			Message: &tgMessage{MessageID: 8, Chat: tgChat{ID: 1}}}})
	}

	// Чужой не запускает установку.
	press(99, "upd:v9.0.0")
	if _, err := os.Stat(filepath.Join(s.dataDir, "update-request.json")); err == nil {
		t.Fatal("stranger started an update")
	}
	// Поддельная версия отвергается до запуска службы.
	press(1, "upd:main")
	if _, err := os.Stat(filepath.Join(s.dataDir, "update-request.json")); err == nil {
		t.Fatal("invalid version started an update")
	}

	press(1, "upd:v9.0.0")
	data, err := os.ReadFile(filepath.Join(s.dataDir, "update-request.json"))
	if err != nil || !strings.Contains(string(data), `"version":"v9.0.0"`) {
		t.Fatalf("request: %s %v", data, err)
	}
	if text := lastText(fake.byMethod("editMessageText")); !strings.Contains(text, "Обновляю до v9.0.0") {
		t.Fatalf("progress message: %q", text)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, telegramUpdateFile)); err != nil {
		t.Fatalf("pending update not saved: %v", err)
	}
	events, _ := s.audit.recent(5)
	if len(events) != 1 || events[0].Actor != "telegram:@anya" || events[0].Action != "update.install" {
		t.Fatalf("audit: %+v", events)
	}

	// Новая версия поднялась — наблюдатель дописывает итог и убирает файл.
	installed.Store("v9.0.0")
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(lastText(fake.byMethod("editMessageText")), "Обновлено до v9.0.0") {
		if time.Now().After(deadline) {
			t.Fatalf("no final message: %q", lastText(fake.byMethod("editMessageText")))
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(s.dataDir, telegramUpdateFile)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending file not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTelegramUpdateBlockedKeepsButton объясняет причину отказа и оставляет кнопку для повтора.
func TestTelegramUpdateBlockedKeepsButton(t *testing.T) {
	fake, _, control, _ := updateTelegram(t)
	t.Setenv("HKC_UPDATE_ENABLED", "")
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c", From: tgUser{ID: 1}, Data: "upd:v9.0.0",
		Message: &tgMessage{MessageID: 8, Chat: tgChat{ID: 1}}}})
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 || !strings.Contains(lastText(edits), "Не удалось начать обновление") || edits[0]["reply_markup"] == nil {
		t.Fatalf("blocked update: %v", edits)
	}
}

// TestTelegramUpdateCommand предлагает кнопку, если вышла новая версия, и молчит о ней, если нет.
func TestTelegramUpdateCommand(t *testing.T) {
	fake, s, control, installed := updateTelegram(t)
	s.updates.fetch = func(context.Context) (remoteVersion, error) { return s.updates.remote, nil }
	control.handleUpdate(message(1, "/update", 0))
	sent := fake.byMethod("sendMessage")
	if len(sent) != 1 || !strings.Contains(lastText(sent), "Доступна hrk-console v9.0.0") || sent[0]["reply_markup"] == nil {
		t.Fatalf("update available: %v", sent)
	}
	installed.Store("v9.0.0")
	// buildUpdateOverview сравнивает с версией сборки, а не с подменой: ставим её тоже.
	previous := buildVersion
	buildVersion = "v9.0.0"
	defer func() { buildVersion = previous }()
	control.handleUpdate(message(1, "/update", 0))
	sent = fake.byMethod("sendMessage")
	if len(sent) != 2 || !strings.Contains(lastText(sent), "Установлена последняя версия") || sent[1]["reply_markup"] != nil {
		t.Fatalf("up to date: %v", sent)
	}
}

// TestTelegramUpdateOutcome разбирает исходы установки по версии и состоянию службы.
func TestTelegramUpdateOutcome(t *testing.T) {
	at := time.Now()
	pending := pendingUpdate{Version: "v2.0.0", At: at}
	stamp := func(offset time.Duration) string { return at.Add(offset).UTC().Format(time.RFC3339Nano) }
	cases := []struct {
		name      string
		installed string
		job       map[string]any
		now       time.Time
		want      string
	}{
		{"идёт", "v1.0.0", map[string]any{"phase": "downloading", "updatedAt": stamp(time.Second)}, at.Add(time.Minute), "waiting"},
		{"версия поднялась, служба ещё работает", "v2.0.0", map[string]any{"phase": "restarting", "updatedAt": stamp(time.Second)}, at.Add(time.Minute), "waiting"},
		{"готово", "v2.0.0", map[string]any{"phase": "complete", "updatedAt": stamp(time.Minute)}, at.Add(2 * time.Minute), "ok"},
		{"готово без данных службы", "v2.0.0", nil, at.Add(time.Minute), "ok"},
		{"неудача", "v1.0.0", map[string]any{"phase": "failed", "message": "не прошла проверка", "updatedAt": stamp(time.Minute)}, at.Add(2 * time.Minute), "failed"},
		{"откат", "v1.0.0", map[string]any{"phase": "rolled_back", "updatedAt": stamp(time.Minute)}, at.Add(2 * time.Minute), "failed"},
		{"итог прошлой установки не наш", "v1.0.0", map[string]any{"phase": "failed", "updatedAt": stamp(-time.Hour)}, at.Add(time.Minute), "waiting"},
		{"слишком долго", "v1.0.0", nil, at.Add(telegramUpdateTimeout + time.Second), "timeout"},
	}
	for _, c := range cases {
		state, text := telegramUpdateOutcome(pending, c.installed, c.job, c.now)
		if state != c.want {
			t.Errorf("%s: got %s (%q), want %s", c.name, state, text, c.want)
		}
		if c.want != "waiting" && text == "" {
			t.Errorf("%s: empty text", c.name)
		}
	}
}

// TestTelegramResumesPendingUpdate после перезапуска панели дописывает итог в прежнее сообщение.
func TestTelegramResumesPendingUpdate(t *testing.T) {
	fake, s, control, installed := updateTelegram(t)
	installed.Store("v9.0.0")
	if err := control.savePending(pendingUpdate{Chat: 1, Message: 8, Version: "v9.0.0", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	control.resumePendingUpdate()
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 || edits[0]["message_id"] != float64(8) || !strings.Contains(lastText(edits), "Обновлено до v9.0.0") {
		t.Fatalf("resume: %v", edits)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, telegramUpdateFile)); !os.IsNotExist(err) {
		t.Fatalf("pending file kept: %v", err)
	}
	// Устаревший файл просто удаляется, ничего не отправляя.
	if err := control.savePending(pendingUpdate{Chat: 1, Message: 8, Version: "v9.0.0", At: time.Now().Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	control.resumePendingUpdate()
	if len(fake.byMethod("editMessageText")) != 1 {
		t.Fatal("stale pending update was reported")
	}
}
