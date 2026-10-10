package main

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// authAttempt — Число зарезервированных попыток и время сброса окна клиента.
type authAttempt struct {
	failures int
	resetAt  time.Time
}

// authRateLimiter — Ограниченный по размеру набор клиентов; мьютекс объединяет проверку лимита и
// резервирование попытки.
type authRateLimiter struct {
	mu      sync.Mutex
	entries map[string]authAttempt
	limit   int
	window  time.Duration
	lastGC  time.Time
	maxKeys int
}

// newAuthRateLimiter создаёт ограничитель попыток с временным окном и ограниченным числом клиентов.
func newAuthRateLimiter(limit int, window time.Duration) *authRateLimiter {
	return &authRateLimiter{entries: make(map[string]authAttempt), limit: limit, window: window, maxKeys: 4096}
}

// allow резервирует попытку до проверки пароля под мьютексом. Параллельный всплеск не может одновременно
// пройти один и тот же лимит.
func (l *authRateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.lastGC.IsZero() || now.Sub(l.lastGC) >= time.Minute || len(l.entries) >= l.maxKeys {
		for candidate, value := range l.entries {
			if !now.Before(value.resetAt) {
				delete(l.entries, candidate)
			}
		}
		l.lastGC = now
	}
	entry, ok := l.entries[key]
	if ok && !now.Before(entry.resetAt) {
		delete(l.entries, key)
		entry, ok = authAttempt{}, false
	}
	if ok && entry.failures >= l.limit {
		return false, time.Until(entry.resetAt)
	}
	if !ok && len(l.entries) >= l.maxKeys {
		return false, l.window
	}
	if !ok {
		entry.resetAt = now.Add(l.window)
	}
	// Резервируем попытку до вычисления хеша, чтобы параллельные запросы
	// не прошли один и тот же лимит одновременно.
	entry.failures++
	l.entries[key] = entry
	return true, 0
}

// success сбрасывает накопленные попытки клиента после успешного входа.
func (l *authRateLimiter) success(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// clientAddress выделяет адрес клиента из RemoteAddr, не доверяя произвольному заголовку прокси.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// allowAuthAttempt применяет лимит входа и возвращает HTTP 429 с Retry-After при блокировке.
func (s *server) allowAuthAttempt(w http.ResponseWriter, key string) bool {
	allowed, retryAfter := s.authLimiter.allow(key)
	if allowed {
		return true
	}
	seconds := int(retryAfter.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, actionResponse{Message: "слишком много попыток; повторите позже"})
	return false
}
