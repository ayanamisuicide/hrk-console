package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLoginRateLimit проверяет HTTP-ограничение попыток входа.
func TestLoginRateLimit(t *testing.T) {
	s := newTestServer(t)
	s.authLimiter = newAuthRateLimiter(2, time.Minute)
	invite, _, err := s.auth.createInvite(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.auth.register(invite, "alice", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			bytes.NewBufferString(`{"username":"alice","password":"wrong-password"}`))
		request.RemoteAddr = "192.0.2.10:1234"
		response := httptest.NewRecorder()
		s.login(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d", attempt+1, response.Code)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		bytes.NewBufferString(`{"username":"alice","password":"correct-horse-battery"}`))
	request.RemoteAddr = "192.0.2.10:5678"
	response := httptest.NewRecorder()
	s.login(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit: got %d, Retry-After=%q", response.Code, response.Header().Get("Retry-After"))
	}
}

// TestInvalidInviteRejectedBeforePasswordHash проверяет ранний отказ неизвестного инвайта до bcrypt.
func TestInvalidInviteRejectedBeforePasswordHash(t *testing.T) {
	s := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewBufferString(
		`{"invite":"invalid","username":"alice","password":"`+strings.Repeat("x", 73)+`"}`))
	response := httptest.NewRecorder()
	s.register(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "инвайт недействителен") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

// TestAuthRateLimiterBoundsAndCleansKeys проверяет лимит записей клиентов и очистку истёкших окон.
func TestAuthRateLimiterBoundsAndCleansKeys(t *testing.T) {
	limiter := newAuthRateLimiter(2, time.Minute)
	limiter.maxKeys = 3
	for _, key := range []string{"one", "two", "three"} {
		if ok, _ := limiter.allow(key); !ok {
			t.Fatalf("initial key %q rejected", key)
		}
	}
	if ok, _ := limiter.allow("four"); ok {
		t.Fatal("limiter accepted a key beyond its memory bound")
	}
	limiter.mu.Lock()
	for key, entry := range limiter.entries {
		entry.resetAt = time.Now().Add(-time.Second)
		limiter.entries[key] = entry
	}
	limiter.mu.Unlock()
	if ok, _ := limiter.allow("four"); !ok {
		t.Fatal("expired keys were not collected")
	}
}

// TestSessionStoreLimitsSessionsPerUser проверяет ограничение числа сессий пользователя.
func TestSessionStoreLimitsSessionsPerUser(t *testing.T) {
	store := newSessionStore()
	for i := 0; i < maxSessionsPerUser+5; i++ {
		if _, _, err := store.create("alice"); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	count := sessionCount(store.sessions, "alice")
	store.mu.Unlock()
	if count != maxSessionsPerUser {
		t.Fatalf("got %d sessions, want %d", count, maxSessionsPerUser)
	}
}
