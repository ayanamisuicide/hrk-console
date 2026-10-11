package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// flowServer — сервер с журналом действий и помощником для запросов через настоящий маршрутизатор.
type flowServer struct {
	*server
	handler http.Handler
}

func newFlowServer(t *testing.T) *flowServer {
	t.Helper()
	s := newTestServer(t)
	s.audit = newAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"))
	s.dataDir = t.TempDir()
	return &flowServer{server: s, handler: s.routes()}
}

type flowRequest struct {
	method, path, body string
	admin              bool
	cookie             *http.Cookie
	bearer             string
}

func (f *flowServer) do(t *testing.T, r flowRequest) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.admin {
		request.Header.Set("Authorization", "Bearer admin-secret")
	} else if r.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	if r.cookie != nil {
		request.AddCookie(r.cookie)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("ответ не JSON: %q (%v)", w.Body.String(), err)
	}
	return out
}

func want(t *testing.T, what string, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("%s: код %d, ждали %d: %s", what, w.Code, code, w.Body.String())
	}
}

// login регистрирует пользователя по приглашению и возвращает его сессионную cookie.
func (f *flowServer) login(t *testing.T, username string) *http.Cookie {
	t.Helper()
	invite, _, err := f.auth.createInvite(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.auth.register(invite, username, "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	response := f.do(t, flowRequest{method: "POST", path: "/api/auth/login", body: `{"username":"` + username + `","password":"long-enough-password"}`})
	want(t, "вход", response, http.StatusOK)
	for _, c := range response.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("вход не выдал cookie сессии")
	return nil
}

// Жизненный цикл пользователя: приглашение, роль, удаление и запись в журнал действий.
func TestAdminUserLifecycleOverHTTP(t *testing.T) {
	f := newFlowServer(t)
	cookie := f.login(t, "alice")

	// Приглашение: создать и отозвать, повторный отзыв — 404.
	created := f.do(t, flowRequest{method: "POST", path: "/api/admin/invites", body: `{"expiresHours":1}`, admin: true})
	want(t, "создание приглашения", created, http.StatusCreated)
	token, _ := decode(t, created)["invite"].(string)
	if token == "" {
		t.Fatal("нет токена приглашения")
	}
	want(t, "отзыв", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/invites/" + token, admin: true}), http.StatusOK)
	want(t, "повторный отзыв", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/invites/" + token, admin: true}), http.StatusNotFound)

	// Роль: неверная — 400, неизвестный пользователь — 404, верная — 200 и видна в /me.
	want(t, "кривое тело", f.do(t, flowRequest{method: "PATCH", path: "/api/admin/users/alice/role", body: `{`, admin: true}), http.StatusBadRequest)
	want(t, "неверная роль", f.do(t, flowRequest{method: "PATCH", path: "/api/admin/users/alice/role", body: `{"role":"root"}`, admin: true}), http.StatusBadRequest)
	want(t, "нет пользователя", f.do(t, flowRequest{method: "PATCH", path: "/api/admin/users/ghost/role", body: `{"role":"viewer"}`, admin: true}), http.StatusNotFound)
	want(t, "смена роли", f.do(t, flowRequest{method: "PATCH", path: "/api/admin/users/alice/role", body: `{"role":"viewer"}`, admin: true}), http.StatusOK)
	me := f.do(t, flowRequest{method: "GET", path: "/api/auth/me", cookie: cookie})
	want(t, "me", me, http.StatusOK)
	if got := decode(t, me)["role"]; got != "viewer" {
		t.Fatalf("роль в /me: %v", got)
	}
	// Наблюдатель может смотреть, но не управлять ботом.
	want(t, "наблюдатель смотрит", f.do(t, flowRequest{method: "GET", path: "/api/status", cookie: cookie}), http.StatusOK)
	if w := f.do(t, flowRequest{method: "POST", path: "/api/bot/explode", cookie: cookie}); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Fatalf("наблюдатель управляет ботом: %d", w.Code)
	}

	// Выход закрывает сессию.
	want(t, "выход", f.do(t, flowRequest{method: "POST", path: "/api/auth/logout", cookie: cookie}), http.StatusOK)
	want(t, "после выхода", f.do(t, flowRequest{method: "GET", path: "/api/auth/me", cookie: cookie}), http.StatusUnauthorized)

	// Удаление: неизвестный — 404, известный — 200.
	want(t, "удаление чужого", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/users/ghost", admin: true}), http.StatusNotFound)
	want(t, "удаление", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/users/alice", admin: true}), http.StatusOK)

	// Журнал действий видит всё перечисленное и требует токена.
	want(t, "аудит без токена", f.do(t, flowRequest{method: "GET", path: "/api/admin/audit"}), http.StatusUnauthorized)
	audit := f.do(t, flowRequest{method: "GET", path: "/api/admin/audit", admin: true})
	want(t, "аудит", audit, http.StatusOK)
	body := audit.Body.String()
	for _, action := range []string{"auth.login", "invite.revoke", "user.role", "user.delete"} {
		if !strings.Contains(body, action) {
			t.Errorf("в журнале действий нет %s: %s", action, body)
		}
	}
}

func TestLoginRejectsBadInput(t *testing.T) {
	f := newFlowServer(t)
	f.login(t, "bob")
	want(t, "кривое тело", f.do(t, flowRequest{method: "POST", path: "/api/auth/login", body: `nope`}), http.StatusBadRequest)
	want(t, "неверный пароль", f.do(t, flowRequest{method: "POST", path: "/api/auth/login", body: `{"username":"bob","password":"wrong"}`}), http.StatusUnauthorized)
	want(t, "нет пользователя", f.do(t, flowRequest{method: "POST", path: "/api/auth/login", body: `{"username":"nobody","password":"x"}`}), http.StatusUnauthorized)
}

// API-токены: выдача, использование по области доступа, отзыв.
func TestAPITokenLifecycleOverHTTP(t *testing.T) {
	f := newFlowServer(t)
	want(t, "без токена", f.do(t, flowRequest{method: "POST", path: "/api/admin/tokens", body: `{"label":"x","scope":"read"}`}), http.StatusUnauthorized)
	want(t, "кривое тело", f.do(t, flowRequest{method: "POST", path: "/api/admin/tokens", body: `{`, admin: true}), http.StatusBadRequest)
	want(t, "неверная область", f.do(t, flowRequest{method: "POST", path: "/api/admin/tokens", body: `{"label":"x","scope":"god"}`, admin: true}), http.StatusBadRequest)

	created := f.do(t, flowRequest{method: "POST", path: "/api/admin/tokens", body: `{"label":"ci","scope":"read"}`, admin: true})
	want(t, "выдача", created, http.StatusCreated)
	body := decode(t, created)
	secret, _ := body["token"].(string)
	info, _ := body["info"].(map[string]any)
	id, _ := info["id"].(string)
	if secret == "" || id == "" {
		t.Fatalf("ответ выдачи: %v", body)
	}
	list := f.do(t, flowRequest{method: "GET", path: "/api/admin/tokens", admin: true})
	want(t, "список", list, http.StatusOK)
	if strings.Contains(list.Body.String(), secret) {
		t.Fatal("список токенов раскрывает секрет")
	}

	want(t, "чтение по токену", f.do(t, flowRequest{method: "GET", path: "/api/v1/status", bearer: secret}), http.StatusOK)
	want(t, "журнал по токену", f.do(t, flowRequest{method: "GET", path: "/api/v1/logs", bearer: secret}), http.StatusOK)
	if w := f.do(t, flowRequest{method: "POST", path: "/api/v1/bot/explode", bearer: secret}); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Fatalf("токен «read» управляет ботом: %d", w.Code)
	}

	want(t, "отзыв неизвестного", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/tokens/nope", admin: true}), http.StatusNotFound)
	want(t, "отзыв", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/tokens/" + id, admin: true}), http.StatusOK)
	want(t, "после отзыва", f.do(t, flowRequest{method: "GET", path: "/api/v1/status", bearer: secret}), http.StatusUnauthorized)
}

// Резервные копии базы доступа: создание, список, восстановление и отказ на чужое имя.
func TestBackupHandlersOverHTTP(t *testing.T) {
	f := newFlowServer(t)
	f.login(t, "carol")
	want(t, "без токена", f.do(t, flowRequest{method: "POST", path: "/api/admin/backups"}), http.StatusUnauthorized)
	created := f.do(t, flowRequest{method: "POST", path: "/api/admin/backups", admin: true})
	want(t, "создание", created, http.StatusCreated)
	name, _ := decode(t, created)["name"].(string)
	if name == "" {
		t.Fatalf("имя копии: %s", created.Body.String())
	}
	list := f.do(t, flowRequest{method: "GET", path: "/api/admin/backups", admin: true})
	want(t, "список", list, http.StatusOK)
	if !strings.Contains(list.Body.String(), name) {
		t.Fatalf("копии нет в списке: %s", list.Body.String())
	}
	want(t, "чужое имя", f.do(t, flowRequest{method: "POST", path: "/api/admin/backups/nonexistent/restore", admin: true}), http.StatusBadRequest)
	want(t, "восстановление", f.do(t, flowRequest{method: "POST", path: "/api/admin/backups/" + name + "/restore", admin: true}), http.StatusOK)
	if !f.auth.authenticate("carol", "long-enough-password") {
		t.Fatal("после восстановления пользователь пропал")
	}
}

// Настройки бота: удаление ключа, история версий, отказ на неизвестный ключ.
func TestConfigDeleteAndHistoryOverHTTP(t *testing.T) {
	f := newFlowServer(t)
	if err := os.WriteFile(f.configPath(), []byte(`{"api_id": 1, "app_name": "bot"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want(t, "без токена", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/config/app_name"}), http.StatusUnauthorized)
	want(t, "неизвестный ключ", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/config/evil", admin: true}), http.StatusBadRequest)
	want(t, "удаление", f.do(t, flowRequest{method: "DELETE", path: "/api/admin/config/app_name", admin: true}), http.StatusOK)
	data, _ := os.ReadFile(f.configPath())
	if strings.Contains(string(data), "app_name") {
		t.Fatalf("ключ не удалён: %s", data)
	}
	history := f.do(t, flowRequest{method: "GET", path: "/api/admin/config/history", admin: true})
	want(t, "история", history, http.StatusOK)
	if got, _ := decode(t, history)["history"].([]any); len(got) != 1 {
		t.Fatalf("история после удаления: %s", history.Body.String())
	}
	want(t, "история без токена", f.do(t, flowRequest{method: "GET", path: "/api/admin/config/history"}), http.StatusUnauthorized)
}

// Бот: состояние, неизвестное действие и защита административных действий. Запуск и остановка
// не вызываются: тест не должен трогать настоящие процессы.
func TestBotStatusAndUnknownAction(t *testing.T) {
	f := newFlowServer(t)
	cookie := f.login(t, "dave")
	status := f.do(t, flowRequest{method: "GET", path: "/api/status", cookie: cookie})
	want(t, "состояние", status, http.StatusOK)
	if _, ok := decode(t, status)["running"]; !ok {
		t.Fatalf("в состоянии нет running: %s", status.Body.String())
	}
	want(t, "админ: неизвестное действие", f.do(t, flowRequest{method: "POST", path: "/api/admin/bot/explode", admin: true}), http.StatusNotFound)
	want(t, "оператор: неизвестное действие", f.do(t, flowRequest{method: "POST", path: "/api/bot/explode", cookie: cookie}), http.StatusNotFound)
	want(t, "админ без токена", f.do(t, flowRequest{method: "POST", path: "/api/admin/bot/start"}), http.StatusUnauthorized)
}

// Старый журнал-хвост отдаёт последние строки и держит лимит.
func TestLegacyLogsTail(t *testing.T) {
	f := newFlowServer(t)
	cookie := f.login(t, "erin")
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("2026-10-11 10:00:00 [INFO] m: строка\n")
	}
	if err := os.WriteFile(f.bot.LogFile, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	response := f.do(t, flowRequest{method: "GET", path: "/api/logs?limit=7", cookie: cookie})
	want(t, "хвост", response, http.StatusOK)
	if lines, _ := decode(t, response)["lines"].([]any); len(lines) != 7 {
		t.Fatalf("строк %d, ждали 7", len(lines))
	}
	// Недопустимый limit заменяется значением по умолчанию, а не ошибкой.
	response = f.do(t, flowRequest{method: "GET", path: "/api/logs?limit=999999", cookie: cookie})
	want(t, "большой limit", response, http.StatusOK)
	if lines, _ := decode(t, response)["lines"].([]any); len(lines) != 50 {
		t.Fatalf("строк %d, ждали 50", len(lines))
	}
}
