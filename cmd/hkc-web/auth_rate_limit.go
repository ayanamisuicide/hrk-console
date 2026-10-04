package main

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type authAttempt struct {
	failures int
	resetAt  time.Time
}

type authRateLimiter struct {
	mu      sync.Mutex
	entries map[string]authAttempt
	limit   int
	window  time.Duration
	lastGC  time.Time
	maxKeys int
}

func newAuthRateLimiter(limit int, window time.Duration) *authRateLimiter {
	return &authRateLimiter{entries: make(map[string]authAttempt), limit: limit, window: window, maxKeys: 4096}
}

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
	// Reserve the attempt before the password hash starts so a burst of
	// concurrent requests cannot all pass the limit at once.
	entry.failures++
	l.entries[key] = entry
	return true, 0
}

func (l *authRateLimiter) success(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

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
