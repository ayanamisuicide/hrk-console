package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "hkc_session"

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

type userRecord struct {
	PasswordHash string    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`
	Role         string    `json:"role,omitempty"`
}

type inviteRecord struct {
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Role      string    `json:"role,omitempty"`
}

type authData struct {
	Users   map[string]userRecord     `json:"users"`
	Invites map[string]inviteRecord   `json:"invites"`
	Tokens  map[string]apiTokenRecord `json:"tokens,omitempty"`
}

type authStore struct {
	mu   sync.Mutex
	path string
	data authData
}

func openAuthStore(path string) (*authStore, error) {
	s := &authStore{path: path, data: authData{
		Users:   make(map[string]userRecord),
		Invites: make(map[string]inviteRecord),
		Tokens:  make(map[string]apiTokenRecord),
	}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("чтение базы авторизации: %w", err)
	}
	if s.data.Users == nil {
		s.data.Users = make(map[string]userRecord)
	}
	if s.data.Invites == nil {
		s.data.Invites = make(map[string]inviteRecord)
	}
	if s.data.Tokens == nil {
		s.data.Tokens = make(map[string]apiTokenRecord)
	}
	return s, nil
}

func (s *authStore) createInvite(validFor time.Duration) (string, time.Time, error) {
	return s.createInviteWithRole(validFor, "operator")
}

func (s *authStore) createInviteWithRole(validFor time.Duration, role string) (string, time.Time, error) {
	if role != "operator" && role != "viewer" {
		return "", time.Time{}, errors.New("неизвестная роль")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(validFor)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, invite := range s.data.Invites {
		if now.After(invite.ExpiresAt) {
			delete(s.data.Invites, key)
		}
	}
	s.data.Invites[token] = inviteRecord{CreatedAt: now, ExpiresAt: expires, Role: role}
	if err := s.saveLocked(); err != nil {
		delete(s.data.Invites, token)
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (s *authStore) register(invite, username, password string) error {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return errors.New("логин: 3–32 символа; разрешены буквы, цифры, точка, дефис и подчёркивание")
	}
	if len(password) < 10 {
		return errors.New("пароль должен содержать минимум 10 символов")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Users[username]; exists {
		return errors.New("такой логин уже занят")
	}
	record, exists := s.data.Invites[invite]
	if !exists || time.Now().After(record.ExpiresAt) {
		return errors.New("инвайт недействителен или истёк")
	}
	role := record.Role
	if role == "" {
		role = "operator"
	}
	s.data.Users[username] = userRecord{PasswordHash: string(hash), CreatedAt: time.Now().UTC(), Role: role}
	delete(s.data.Invites, invite)
	if err := s.saveLocked(); err != nil {
		delete(s.data.Users, username)
		s.data.Invites[invite] = record
		return err
	}
	return nil
}

func (s *authStore) authenticate(username, password string) bool {
	s.mu.Lock()
	record, exists := s.data.Users[strings.TrimSpace(username)]
	s.mu.Unlock()
	return exists && bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(password)) == nil
}

type storedUser struct {
	Username  string
	CreatedAt time.Time
	Role      string
}

type storedInvite struct {
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
	Role      string
}

func (s *authStore) snapshot() ([]storedUser, []storedInvite) {
	s.mu.Lock()
	defer s.mu.Unlock()
	users := make([]storedUser, 0, len(s.data.Users))
	for username, record := range s.data.Users {
		role := record.Role
		if role == "" {
			role = "operator"
		}
		users = append(users, storedUser{Username: username, CreatedAt: record.CreatedAt, Role: role})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].CreatedAt.Before(users[j].CreatedAt) })

	now := time.Now()
	invites := make([]storedInvite, 0, len(s.data.Invites))
	for token, record := range s.data.Invites {
		if now.Before(record.ExpiresAt) {
			role := record.Role
			if role == "" {
				role = "operator"
			}
			invites = append(invites, storedInvite{Token: token, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt, Role: role})
		}
	}
	sort.Slice(invites, func(i, j int) bool { return invites[i].CreatedAt.After(invites[j].CreatedAt) })
	return users, invites
}

func (s *authStore) role(username string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.data.Users[username]
	if !ok {
		return ""
	}
	if record.Role == "" {
		return "operator"
	}
	return record.Role
}

func (s *authStore) setRole(username, role string) (bool, error) {
	if role != "operator" && role != "viewer" {
		return false, errors.New("неизвестная роль")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.data.Users[username]
	if !ok {
		return false, nil
	}
	previous := record
	record.Role = role
	s.data.Users[username] = record
	if err := s.saveLocked(); err != nil {
		s.data.Users[username] = previous
		return false, err
	}
	return true, nil
}

func (s *authStore) revokeInvite(token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.data.Invites[token]
	if !exists {
		return false, nil
	}
	delete(s.data.Invites, token)
	if err := s.saveLocked(); err != nil {
		s.data.Invites[token] = record
		return false, err
	}
	return true, nil
}

func (s *authStore) deleteUser(username string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.data.Users[username]
	if !exists {
		return false, nil
	}
	delete(s.data.Users, username)
	if err := s.saveLocked(); err != nil {
		s.data.Users[username] = record
		return false, err
	}
	return true, nil
}

func (s *authStore) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

type session struct {
	Username  string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]session)}
}

func (s *sessionStore) create(username string) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	now := time.Now()
	s.mu.Lock()
	s.sessions[token] = session{Username: username, CreatedAt: now, LastSeen: now, ExpiresAt: expires}
	s.mu.Unlock()
	return token, expires, nil
}

func (s *sessionStore) get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.sessions[token]
	if !exists || time.Now().After(entry.ExpiresAt) {
		delete(s.sessions, token)
		return "", false
	}
	entry.LastSeen = time.Now()
	s.sessions[token] = entry
	return entry.Username, true
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

type presence struct {
	Sessions int
	LastSeen time.Time
}

func (s *sessionStore) presence() map[string]presence {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	result := make(map[string]presence)
	for token, entry := range s.sessions {
		if now.After(entry.ExpiresAt) {
			delete(s.sessions, token)
			continue
		}
		current := result[entry.Username]
		current.Sessions++
		if entry.LastSeen.After(current.LastSeen) {
			current.LastSeen = entry.LastSeen
		}
		result[entry.Username] = current
	}
	return result
}

func (s *sessionStore) deleteUser(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, entry := range s.sessions {
		if entry.Username == username {
			delete(s.sessions, token)
		}
	}
}

func (s *sessionStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = make(map[string]session)
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
