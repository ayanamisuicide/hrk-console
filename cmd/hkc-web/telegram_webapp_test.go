package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signInitData собирает initData так же, как клиент Telegram.
func signInitData(token string, userID int64, at time.Time) string {
	values := url.Values{}
	values.Set("auth_date", strconv.FormatInt(at.Unix(), 10))
	values.Set("query_id", "AAE")
	values.Set("user", `{"id":`+strconv.FormatInt(userID, 10)+`,"first_name":"Аня","username":"anya"}`)
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{}
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}

// TestVerifyTelegramInitData принимает подпись Telegram и отвергает подделку и старые данные.
func TestVerifyTelegramInitData(t *testing.T) {
	now := time.Now()
	user, err := verifyTelegramInitData(signInitData(testBotToken, 7, now), testBotToken, now)
	if err != nil || user.ID != 7 || user.Username != "anya" {
		t.Fatalf("valid data rejected: %v %+v", err, user)
	}
	if _, err := verifyTelegramInitData(signInitData("999:other", 7, now), testBotToken, now); err == nil {
		t.Fatal("foreign signature accepted")
	}
	tampered := strings.Replace(signInitData(testBotToken, 7, now), "anya", "evil", 1)
	if _, err := verifyTelegramInitData(tampered, testBotToken, now); err == nil {
		t.Fatal("tampered data accepted")
	}
	if _, err := verifyTelegramInitData(signInitData(testBotToken, 7, now.Add(-48*time.Hour)), testBotToken, now); err == nil {
		t.Fatal("stale data accepted")
	}
}

func startWebSession(t *testing.T, handler http.Handler, initData string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"initData": initData})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/tg/session", bytes.NewReader(body)))
	return response
}

// TestTelegramWebAppSession выдаёт сессию только администратору и закрывает её при выключении управления.
func TestTelegramWebAppSession(t *testing.T) {
	_, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 7)
	control.config.WebAppURL = "https://panel.example.com"
	handler := s.routes()

	// Управление выключено — приложение недоступно, даже с верной подписью.
	if response := startWebSession(t, handler, signInitData(testBotToken, 7, time.Now())); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled control: %d", response.Code)
	}
	if err := s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if response := startWebSession(t, handler, signInitData(testBotToken, 8, time.Now())); response.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d", response.Code)
	}
	response := startWebSession(t, handler, signInitData(testBotToken, 7, time.Now()))
	if response.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", response.Code, response.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &session)

	overview := func() int {
		request := httptest.NewRequest(http.MethodGet, "/api/tg/overview", nil)
		request.Header.Set("Authorization", "Bearer "+session.Token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if code := overview(); code != http.StatusOK {
		t.Fatalf("overview with session: %d", code)
	}
	if err := s.operations.setTelegram(telegramSettings{ControlEnabled: false, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if code := overview(); code != http.StatusServiceUnavailable {
		t.Fatalf("overview after disabling: %d", code)
	}
}

// TestTelegramPagesAllowTelegramFrame разрешает встраивание только мини-приложению.
func TestTelegramPagesAllowTelegramFrame(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for path, framed := range map[string]bool{"/tg/": true, "/": false, "/admin/": false} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		csp := response.Header().Get("Content-Security-Policy")
		if framed != strings.Contains(csp, "frame-ancestors https://web.telegram.org") || framed == (response.Header().Get("X-Frame-Options") == "DENY") {
			t.Fatalf("%s: csp=%q xfo=%q", path, csp, response.Header().Get("X-Frame-Options"))
		}
	}
}
