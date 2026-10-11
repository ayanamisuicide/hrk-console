package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

const leakedToken = "7123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0"

// Оператор видит журнал как есть, наблюдатель и токен чтения — с замаскированными секретами,
// причём поиск по скрытому тексту у них ничего не находит (нельзя подобрать секрет по совпадениям).
func TestJournalMasksSecretsForNonOperators(t *testing.T) {
	f := newFlowServer(t)
	logText := "2026-10-11 10:00:00 [ERROR] loader: не удалось войти, token=" + leakedToken + "\n" +
		"Traceback (most recent call last):\n  File \"x.py\", line 1\nAuthError: Bearer abcdefghijklmnop12345\n" +
		"2026-10-11 10:00:01 [INFO] loader: обычная запись\n"
	if err := os.WriteFile(f.bot.LogFile, []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}
	operator := f.login(t, "olga")
	viewer := f.login(t, "victor")
	want(t, "роль", f.do(t, flowRequest{method: "PATCH", path: "/api/admin/users/victor/role", body: `{"role":"viewer"}`, admin: true}), http.StatusOK)

	raw := f.do(t, flowRequest{method: "GET", path: "/api/logs/entries", cookie: operator})
	want(t, "оператор", raw, http.StatusOK)
	if !strings.Contains(raw.Body.String(), leakedToken) {
		t.Fatal("оператор должен видеть журнал без масок")
	}

	for _, request := range []flowRequest{
		{method: "GET", path: "/api/logs/entries", cookie: viewer},
		{method: "GET", path: "/api/logs", cookie: viewer},
		{method: "GET", path: "/api/incidents", cookie: viewer},
		{method: "GET", path: "/api/logs/problems?from=1", cookie: viewer},
	} {
		response := f.do(t, request)
		want(t, request.path, response, http.StatusOK)
		body := response.Body.String()
		if strings.Contains(body, leakedToken) || strings.Contains(body, "abcdefghijklmnop12345") {
			t.Errorf("%s: секрет виден наблюдателю: %s", request.path, body)
		}
	}
	masked := f.do(t, flowRequest{method: "GET", path: "/api/logs/entries", cookie: viewer})
	if !strings.Contains(masked.Body.String(), "обычная запись") || !strings.Contains(masked.Body.String(), "скрыто") {
		t.Fatalf("маскирование испортило остальной текст: %s", masked.Body.String())
	}

	// Оракул поиска: фрагмент секрета у оператора находит запись, у наблюдателя — нет.
	fragment := "/api/logs/entries?q=AAHdqTcvCH1v"
	if body := f.do(t, flowRequest{method: "GET", path: fragment, cookie: operator}).Body.String(); !strings.Contains(body, "не удалось войти") {
		t.Fatalf("оператор не нашёл запись по фрагменту: %s", body)
	}
	if body := f.do(t, flowRequest{method: "GET", path: fragment, cookie: viewer}).Body.String(); strings.Contains(body, "не удалось войти") {
		t.Fatalf("наблюдатель нашёл запись по скрытому фрагменту: %s", body)
	}
	// Но по видимому тексту тот же наблюдатель ищет как обычно.
	if body := f.do(t, flowRequest{method: "GET", path: "/api/logs/entries?q=" + "не%20удалось", cookie: viewer}).Body.String(); !strings.Contains(body, "не удалось войти") {
		t.Fatalf("поиск по видимому тексту сломан: %s", body)
	}

	// Токен чтения — тоже не оператор.
	created := f.do(t, flowRequest{method: "POST", path: "/api/admin/tokens", body: `{"label":"ci","scope":"read"}`, admin: true})
	secret, _ := decode(t, created)["token"].(string)
	response := f.do(t, flowRequest{method: "GET", path: "/api/v1/logs", bearer: secret})
	want(t, "api-токен", response, http.StatusOK)
	if strings.Contains(response.Body.String(), leakedToken) {
		t.Fatalf("API-токен видит секрет: %s", response.Body.String())
	}
}
