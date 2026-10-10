package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"heroku-console/botproc"
)

// newTestServer создаёт сервер с отдельными временными файлами, не затрагивая настоящую установку.
func newTestServer(t *testing.T) *server {
	t.Helper()
	auth, err := openAuthStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &server{
		bot:         botproc.New(t.TempDir()),
		auth:        auth,
		sessions:    newSessionStore(),
		adminToken:  "admin-secret",
		authLimiter: newAuthRateLimiter(20, 5*time.Minute),
	}
}

// TestHTTPInviteRegisterLogin проверяет весь HTTP-путь приглашения, регистрации, входа и выхода.
func TestHTTPInviteRegisterLogin(t *testing.T) {
	s := newTestServer(t)

	unauthorized := httptest.NewRecorder()
	s.createInvite(unauthorized, httptest.NewRequest(http.MethodPost, "/api/admin/invites", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("admin without token: got %d", unauthorized.Code)
	}

	inviteReq := httptest.NewRequest(http.MethodPost, "https://console.example/api/admin/invites", bytes.NewBufferString(`{"expiresHours":2}`))
	inviteReq.Header.Set("Authorization", "Bearer admin-secret")
	inviteRes := httptest.NewRecorder()
	s.createInvite(inviteRes, inviteReq)
	if inviteRes.Code != http.StatusCreated {
		t.Fatalf("create invite: got %d: %s", inviteRes.Code, inviteRes.Body.String())
	}
	var inviteBody struct {
		Invite          string `json:"invite"`
		RegistrationURL string `json:"registrationUrl"`
	}
	if err := json.Unmarshal(inviteRes.Body.Bytes(), &inviteBody); err != nil {
		t.Fatal(err)
	}
	if inviteBody.Invite == "" || inviteBody.RegistrationURL == "" {
		t.Fatalf("incomplete invite response: %+v", inviteBody)
	}

	registerPayload, _ := json.Marshal(map[string]string{
		"invite": inviteBody.Invite, "username": "alice", "password": "correct-horse-battery",
	})
	registerReq := httptest.NewRequest(http.MethodPost, "https://console.example/api/auth/register", bytes.NewReader(registerPayload))
	registerRes := httptest.NewRecorder()
	s.register(registerRes, registerReq)
	if registerRes.Code != http.StatusCreated {
		t.Fatalf("register: got %d: %s", registerRes.Code, registerRes.Body.String())
	}
	cookies := registerRes.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unsafe session cookie: %+v", cookies)
	}

	meReq := httptest.NewRequest(http.MethodGet, "https://console.example/api/auth/me", nil)
	meReq.AddCookie(cookies[0])
	meRes := httptest.NewRecorder()
	s.authorize(s.me)(meRes, meReq)
	if meRes.Code != http.StatusOK || !bytes.Contains(meRes.Body.Bytes(), []byte(`"alice"`)) {
		t.Fatalf("me: got %d: %s", meRes.Code, meRes.Body.String())
	}

	overviewReq := httptest.NewRequest(http.MethodGet, "https://console.example/api/admin/overview", nil)
	overviewReq.Header.Set("Authorization", "Bearer admin-secret")
	overviewRes := httptest.NewRecorder()
	s.adminOverview(overviewRes, overviewReq)
	if overviewRes.Code != http.StatusOK || !bytes.Contains(overviewRes.Body.Bytes(), []byte(`"username":"alice"`)) || !bytes.Contains(overviewRes.Body.Bytes(), []byte(`"online":true`)) {
		t.Fatalf("overview: got %d: %s", overviewRes.Code, overviewRes.Body.String())
	}

	reuseReq := httptest.NewRequest(http.MethodPost, "https://console.example/api/auth/register", bytes.NewReader(registerPayload))
	reuseRes := httptest.NewRecorder()
	s.register(reuseRes, reuseReq)
	if reuseRes.Code != http.StatusBadRequest {
		t.Fatalf("reused invite: got %d", reuseRes.Code)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "https://console.example/api/admin/users/alice", nil)
	deleteReq.SetPathValue("username", "alice")
	deleteReq.Header.Set("Authorization", "Bearer admin-secret")
	deleteRes := httptest.NewRecorder()
	s.deleteUser(deleteRes, deleteReq)
	if deleteRes.Code != http.StatusOK || s.auth.authenticate("alice", "correct-horse-battery") {
		t.Fatalf("delete user: got %d: %s", deleteRes.Code, deleteRes.Body.String())
	}
}

// TestLiveMetricsRequireSession проверяет защиту метрик сессионной авторизацией.
func TestLiveMetricsRequireSession(t *testing.T) {
	s := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	response := httptest.NewRecorder()
	s.authorize(s.liveMetrics)(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("metrics without session: got %d", response.Code)
	}
	invite, _, err := s.auth.createInvite(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.auth.register(invite, "viewer", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	user, _, err := s.sessions.create("viewer")
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: user})
	response = httptest.NewRecorder()
	s.authorize(s.liveMetrics)(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"rssBytes"`)) {
		t.Fatalf("metrics with session: %d %s", response.Code, response.Body.String())
	}
}
