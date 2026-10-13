package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testClient направляет вызовы в тестовый сервер, отвечающий handler.
func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{Token: testBotToken, Base: server.URL, HTTP: server.Client()}, server
}

// TestCallDecodesResultAndHidesToken: ответ разбирается в result, а токен не попадает в текст ошибки.
func TestCallDecodesResultAndHidesToken(t *testing.T) {
	var path string
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Panel","username":"panel_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: token `+testBotToken+` is odd"}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>bad gateway</html>")
		default:
			_, _ = io.WriteString(w, `{"ok":false,"description":"no code"}`)
		}
	})
	var me User
	if err := client.Call(context.Background(), "getMe", map[string]any{}, &me); err != nil || me.Username != "panel_bot" {
		t.Fatalf("getMe: %+v, %v", me, err)
	}
	if path != "/bot"+testBotToken+"/getMe" {
		t.Fatalf("path: %s", path)
	}

	err := client.Call(context.Background(), "sendMessage", map[string]any{"text": "x"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusBadRequest || strings.Contains(err.Error(), testBotToken) || !strings.Contains(err.Error(), "***") {
		t.Fatalf("sendMessage error: %v", err)
	}
	// Ответ не в формате Bot API — ошибка с кодом HTTP.
	if err := client.Call(context.Background(), "getUpdates", nil, nil); !errors.As(err, &apiErr) || apiErr.Code != http.StatusBadGateway || err.Error() != "Telegram HTTP 502" {
		t.Fatalf("non-json error: %v", err)
	}
	// Без error_code берётся код HTTP.
	if err := client.Call(context.Background(), "other", nil, nil); !errors.As(err, &apiErr) || apiErr.Code != http.StatusOK || apiErr.Description != "no code" {
		t.Fatalf("missing code: %v", err)
	}
}

// TestCallNetworkErrorsHideAddress: адрес с токеном не попадает в сетевые ошибки, а отмена остаётся отменой.
func TestCallNetworkErrorsHideAddress(t *testing.T) {
	client, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
	server.Close()
	err := client.Call(context.Background(), "getMe", nil, nil)
	if err == nil || strings.Contains(err.Error(), testBotToken) || !strings.HasPrefix(err.Error(), "сетевая ошибка") {
		t.Fatalf("network error: %v", err)
	}

	// Обработчик отпускается явно до закрытия сервера: тот ждёт завершения всех запросов.
	release := make(chan struct{})
	slow, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	slow.HTTP.Timeout = 50 * time.Millisecond
	if err := slow.Call(context.Background(), "getUpdates", nil, nil); err == nil || err.Error() != "превышено время ожидания ответа" {
		t.Fatalf("timeout error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := slow.Call(ctx, "getUpdates", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call: %v", err)
	}
	bad := &Client{Token: testBotToken, Base: "http://[::1", HTTP: http.DefaultClient}
	if err := bad.Call(context.Background(), "getMe", nil, nil); err == nil || strings.Contains(err.Error(), testBotToken) {
		t.Fatalf("bad base: %v", err)
	}
}

// TestUploadSendsMultipart: файл и поля уходят формой, отказ Telegram пересказывается без токена.
func TestUploadSendsMultipart(t *testing.T) {
	var got struct{ chat, caption, name, body string }
	fail := false
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			_, _ = io.WriteString(w, `{"ok":false,"error_code":413,"description":"file of `+testBotToken+` too big"}`)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		file, header, err := r.FormFile("document")
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		data, _ := io.ReadAll(file)
		got.chat, got.caption, got.name, got.body = r.FormValue("chat_id"), r.FormValue("caption"), header.Filename, string(data)
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	})
	err := client.Upload(context.Background(), "sendDocument", map[string]string{"chat_id": "7", "caption": "журнал"}, "document", "heroku.log", []byte("строка\n"))
	if err != nil || got.chat != "7" || got.caption != "журнал" || got.name != "heroku.log" || got.body != "строка\n" {
		t.Fatalf("upload: %+v, %v", got, err)
	}
	fail = true
	var apiErr *APIError
	err = client.Upload(context.Background(), "sendDocument", nil, "document", "x.log", nil)
	if !errors.As(err, &apiErr) || apiErr.Code != 413 || strings.Contains(err.Error(), testBotToken) {
		t.Fatalf("upload error: %v", err)
	}
}

// TestUserNames: имя для подписей и автор в журнале действий.
func TestUserNames(t *testing.T) {
	cases := []struct {
		user        User
		name, actor string
	}{
		{User{ID: 7, FirstName: "Аня", LastName: "К", Username: "anya"}, "Аня К", "telegram:@anya"},
		{User{ID: 7, FirstName: "Аня"}, "Аня", "telegram:Аня"},
		{User{ID: 7}, "7", "telegram:7"},
	}
	for _, c := range cases {
		if got := c.user.DisplayName(); got != c.name {
			t.Errorf("DisplayName(%+v) = %q, want %q", c.user, got, c.name)
		}
		if got := c.user.Actor(); got != c.actor {
			t.Errorf("Actor(%+v) = %q, want %q", c.user, got, c.actor)
		}
	}
}

// TestWebSessions: сессия выдаётся на WebSessionTTL, просроченные удаляются при следующей выдаче.
func TestWebSessions(t *testing.T) {
	var store WebSessions
	now := time.Now()
	first, err := store.Create(User{ID: 1}, now)
	if err != nil || len(first) < 40 {
		t.Fatalf("create: %q, %v", first, err)
	}
	second, _ := store.Create(User{ID: 2}, now.Add(time.Minute))
	if first == second {
		t.Fatal("tokens must differ")
	}
	if user, ok := store.Get(first, now.Add(WebSessionTTL-time.Second)); !ok || user.ID != 1 {
		t.Fatalf("live session: %+v %v", user, ok)
	}
	if _, ok := store.Get(first, now.Add(WebSessionTTL+time.Second)); ok {
		t.Fatal("expired session accepted")
	}
	if _, ok := store.Get("unknown", now); ok {
		t.Fatal("unknown token accepted")
	}
	// Выдача новой сессии убирает просроченные.
	if _, err := store.Create(User{ID: 3}, now.Add(2*WebSessionTTL)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.items[second]; ok {
		t.Fatal("expired session kept")
	}
}

// TestTextHelpers: длительность тишины по-русски, обращение к боту в группе, номера черновиков.
func TestTextHelpers(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:           "меньше минуты",
		30 * time.Minute:           "30 мин",
		90 * time.Minute:           "1 ч 30 мин",
		26*time.Hour + time.Minute: "1 д 2 ч",
	} {
		if got := FormatMuteDuration(d); got != want {
			t.Errorf("FormatMuteDuration(%v) = %q, want %q", d, got, want)
		}
	}
	for _, c := range []struct {
		text, user, want string
		ok               bool
	}{
		{"@Panel_Bot, статус", "panel_bot", "статус", true},
		{"покажи логи @panel_bot", "panel_bot", "покажи логи", true},
		{"статус", "panel_bot", "статус", false},
		{"@panel_bot статус", "", "@panel_bot статус", false},
	} {
		if got, ok := StripMention(c.text, c.user); got != c.want || ok != c.ok {
			t.Errorf("StripMention(%q) = %q, %v", c.text, got, ok)
		}
	}
	a, b := NextDraftID(), NextDraftID()
	if a == 0 || b == 0 || a == b {
		t.Fatalf("draft ids: %d %d", a, b)
	}
}
