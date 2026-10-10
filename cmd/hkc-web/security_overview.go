package main

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// securityOverviewResponse — Сводные счётчики и включённые возможности без секретов и идентификаторов
// сессий.
type securityOverviewResponse struct {
	Users         int             `json:"users"`
	ActiveInvites int             `json:"activeInvites"`
	APITokens     int             `json:"apiTokens"`
	Sessions      int             `json:"sessions"`
	SessionUsers  int             `json:"sessionUsers"`
	RateLimiter   map[string]any  `json:"rateLimiter"`
	Features      map[string]bool `json:"features"`
}

// overview считает действующие записи под блокировкой, удаляя истёкшие элементы.
func (l *authRateLimiter) overview() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	blocked := 0
	for key, entry := range l.entries {
		if !now.Before(entry.resetAt) {
			delete(l.entries, key)
			continue
		}
		if entry.failures >= l.limit {
			blocked++
		}
	}
	return map[string]any{
		"trackedClients": len(l.entries),
		"blockedClients": blocked,
		"limit":          l.limit,
		"windowSeconds":  int(l.window.Seconds()),
		"capacity":       l.maxKeys,
	}
}

// overview считает действующие записи под блокировкой, удаляя истёкшие элементы.
func (s *sessionStore) overview() (sessions, users int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	unique := make(map[string]struct{})
	for token, entry := range s.sessions {
		if now.After(entry.ExpiresAt) {
			delete(s.sessions, token)
			continue
		}
		sessions++
		unique[entry.Username] = struct{}{}
	}
	return sessions, len(unique)
}

// securityCounts считает пользователей, ещё действующие приглашения и API-токены без раскрытия секретов.
func (s *authStore) securityCounts() (users, invites, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, invite := range s.data.Invites {
		if now.Before(invite.ExpiresAt) {
			invites++
		}
	}
	return len(s.data.Users), invites, len(s.data.Tokens)
}

// enabledEnv распознаёт явное включение возможности значением 1.
func enabledEnv(name string) bool { return strings.TrimSpace(os.Getenv(name)) == "1" }

// securityOverview возвращает администратору численную сводку доступа и включённых возможностей.
func (s *server) securityOverview(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	users, invites, tokens := s.auth.securityCounts()
	sessions, sessionUsers := s.sessions.overview()
	writeJSON(w, http.StatusOK, securityOverviewResponse{
		Users: users, ActiveInvites: invites, APITokens: tokens,
		Sessions: sessions, SessionUsers: sessionUsers,
		RateLimiter: s.authLimiter.overview(),
		Features: map[string]bool{
			"terminal":     enabledEnv("HKC_TERMINAL_ENABLED"),
			"trustedProxy": enabledEnv("HKC_TRUST_PROXY"),
			"publicStatus": enabledEnv("HKC_PUBLIC_STATUS"),
			"updates":      enabledEnv("HKC_UPDATE_ENABLED"),
		},
	})
}
