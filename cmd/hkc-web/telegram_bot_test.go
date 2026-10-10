package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testBotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123"

// fakeBotAPI записывает вызовы Bot API; getUpdates отвечает заданной функцией.
type fakeBotAPI struct {
	mu      sync.Mutex
	calls   []fakeCall
	updates func() (int, string)
}

type fakeCall struct {
	method  string
	payload map[string]any
}

func (f *fakeBotAPI) byMethod(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := []map[string]any{}
	for _, call := range f.calls {
		if call.method == method {
			result = append(result, call.payload)
		}
	}
	return result
}

func newFakeBotAPI(t *testing.T) (*fakeBotAPI, *httptest.Server) {
	t.Helper()
	fake := &fakeBotAPI{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fake.mu.Lock()
		fake.calls = append(fake.calls, fakeCall{method, payload})
		updates := fake.updates
		fake.mu.Unlock()
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Panel","username":"panel_bot"}}`))
		case "getUpdates":
			if updates == nil {
				_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
				return
			}
			code, body := updates()
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(api.Close)
	return fake, api
}

func newTestTelegram(t *testing.T, apiURL string, admins ...int64) (*server, *telegramControl) {
	t.Helper()
	s := newTestServer(t)
	dir := t.TempDir()
	s.audit = newAuditStore(filepath.Join(dir, "audit.jsonl"))
	var err error
	s.operations, err = openOperationStore(filepath.Join(dir, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	control := newTelegramControl(s, telegramConfig{Token: testBotToken, APIBase: apiURL, Admins: admins})
	s.telegram = control
	return s, control
}

func message(from int64, text string, age time.Duration) tgUpdate {
	return tgUpdate{Message: &tgMessage{MessageID: 7, From: &tgUser{ID: from, FirstName: "Аня", Username: "anya"},
		Chat: tgChat{ID: from, Type: "private"}, Date: time.Now().Add(-age).Unix(), Text: text}}
}

// TestTelegramIgnoresStrangers не отвечает чужим, но запоминает их ID для настройки.
func TestTelegramIgnoresStrangers(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(99, "/stop", 0))
	if sent := fake.byMethod("sendMessage"); len(sent) != 0 {
		t.Fatalf("stranger got a reply: %v", sent)
	}
	view := control.view()
	unknown := view["unknown"].([]telegramSender)
	if len(unknown) != 1 || unknown[0].ID != "99" || unknown[0].Username != "anya" {
		t.Fatalf("unknown senders: %+v", unknown)
	}
}

// TestTelegramStatusAndStaleCommands отвечает администратору и пропускает старые команды.
func TestTelegramStatusAndStaleCommands(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "/status@panel_bot", 0))
	sent := fake.byMethod("sendMessage")
	if len(sent) != 1 || !strings.Contains(sent[0]["text"].(string), "Бот остановлен") {
		t.Fatalf("status reply: %v", sent)
	}
	control.handleUpdate(message(1, "/status", 10*time.Minute))
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("stale command was answered")
	}
}

// TestTelegramConfirmFlow спрашивает подтверждение, выполняет по кнопке и пишет журнал.
func TestTelegramConfirmFlow(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "/stop", 0))
	sent := fake.byMethod("sendMessage")
	if len(sent) != 1 {
		t.Fatalf("expected confirmation, got %v", sent)
	}
	rows := sent[0]["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	data := rows[0].([]any)[0].(map[string]any)["callback_data"].(string)
	if !strings.HasPrefix(data, "ok:") {
		t.Fatalf("callback data %q", data)
	}

	// Чужой не может нажать чужую кнопку, и кнопка после этого остаётся рабочей.
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c0", From: tgUser{ID: 99}, Data: data}})
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c1", From: tgUser{ID: 1, Username: "anya"}, Data: data,
		Message: &tgMessage{MessageID: 8, Chat: tgChat{ID: 1}}}})
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 || !strings.Contains(edits[0]["text"].(string), "уже остановлен") {
		t.Fatalf("edit after confirm: %v", edits)
	}
	events, _ := s.audit.recent(10)
	if len(events) != 1 || events[0].Actor != "telegram:@anya" || events[0].Action != "bot.stop" {
		t.Fatalf("audit: %+v", events)
	}

	// Повторное нажатие той же кнопки ничего не выполняет.
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c2", From: tgUser{ID: 1}, Data: data,
		Message: &tgMessage{MessageID: 8, Chat: tgChat{ID: 1}}}})
	if edits := fake.byMethod("editMessageText"); !strings.Contains(edits[len(edits)-1]["text"].(string), "устарела") {
		t.Fatalf("reused button: %v", edits)
	}
	if events, _ := s.audit.recent(10); len(events) != 1 {
		t.Fatalf("reused button executed again: %+v", events)
	}
}

// TestTelegramConflictStopsPolling не спорит с другой программой за того же бота.
func TestTelegramConflictStopsPolling(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	fake.updates = func() (int, string) {
		return http.StatusConflict, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`
	}
	s, control := newTestTelegram(t, api.URL, 1)
	if err := s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go control.run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for control.view()["state"] != "conflict" {
		if time.Now().After(deadline) {
			t.Fatalf("state %v", control.view()["state"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	polls := len(fake.byMethod("getUpdates"))
	time.Sleep(200 * time.Millisecond)
	if len(fake.byMethod("getUpdates")) != polls {
		t.Fatal("polling continued after conflict")
	}
}

// TestReadTelegramConfig разбирает список администраторов и адрес мини-приложения.
func TestReadTelegramConfig(t *testing.T) {
	env := map[string]string{"HKC_TELEGRAM_BOT_TOKEN": testBotToken, "HKC_TELEGRAM_ADMIN_IDS": "12, 34;12 abc",
		"HKC_TELEGRAM_WEBAPP_URL": "https://panel.example.com/"}
	config := readTelegramConfig(func(key string) string { return env[key] })
	if config.Token != testBotToken || len(config.Admins) != 2 || config.Admins[1] != 34 || config.AdminsIssue == "" {
		t.Fatalf("config: %+v", config)
	}
	if config.WebAppURL != "https://panel.example.com" || config.ChatID != "" {
		t.Fatalf("config: %+v", config)
	}
	env["HKC_TELEGRAM_WEBAPP_URL"] = "http://panel.example.com"
	if config := readTelegramConfig(func(key string) string { return env[key] }); config.WebAppURL != "" || config.WebAppIssue == "" {
		t.Fatalf("http web app accepted: %+v", config)
	}
}
