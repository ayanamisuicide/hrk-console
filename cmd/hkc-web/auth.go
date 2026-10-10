package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// Сравниваем хеш bcrypt даже для неизвестного логина: время ответа
// не должно раскрывать существование аккаунта.
var dummyPasswordHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// userRecord — Запись пользователя на диске: хранится хеш bcrypt, а не открытый пароль.
type userRecord struct {
	PasswordHash string    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`
	Role         string    `json:"role,omitempty"`
}

// inviteRecord — Одноразовый инвайт с назначаемой ролью и сроком действия.
type inviteRecord struct {
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Role      string    `json:"role,omitempty"`
}

// authData — Сохраняемые данные доступа; сессии в этот файл не входят.
type authData struct {
	Users   map[string]userRecord     `json:"users"`
	Invites map[string]inviteRecord   `json:"invites"`
	Tokens  map[string]apiTokenRecord `json:"tokens,omitempty"`
}

// authStore — Владеет базой доступа в памяти и её файлом. Изменения сериализуются мьютексом.
type authStore struct {
	mu   sync.Mutex
	path string
	data authData
}

// openAuthStore загружает базу доступа с диска и подготавливает коллекции для пользователей, инвайтов и
// токенов.
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

// createInvite создаёт приглашение с ролью управления по умолчанию.
func (s *authStore) createInvite(validFor time.Duration) (string, time.Time, error) {
	return s.createInviteWithRole(validFor, "operator")
}

// createInviteWithRole генерирует одноразовый инвайт с ограниченным сроком действия и сохраняет его под
// блокировкой.
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

// register проверяет инвайт до дорогого bcrypt, затем повторно проверяет его под блокировкой. Пользователь
// и расходование инвайта сохраняются вместе; ошибка записи откатывает память.
func (s *authStore) register(invite, username, password string) error {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return errors.New("логин: 3–32 символа; разрешены буквы, цифры, точка, дефис и подчёркивание")
	}
	if len(password) < 10 {
		return errors.New("пароль должен содержать минимум 10 символов")
	}
	// Отклоняем отсутствующий или истёкший инвайт до дорогого хеширования.
	// Перед расходованием повторяем проверку под блокировкой: другой запрос
	// мог использовать тот же инвайт, пока вычислялся хеш.
	s.mu.Lock()
	record, exists := s.data.Invites[invite]
	s.mu.Unlock()
	if !exists || time.Now().After(record.ExpiresAt) {
		return errors.New("инвайт недействителен или истёк")
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
	record, exists = s.data.Invites[invite]
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

// authenticate проверяет пароль bcrypt. Для неизвестного логина тоже выполняется сравнение с хешем, чтобы
// не выдавать существование аккаунта временем ответа.
func (s *authStore) authenticate(username, password string) bool {
	s.mu.Lock()
	record, exists := s.data.Users[strings.TrimSpace(username)]
	s.mu.Unlock()
	hash := dummyPasswordHash
	if exists {
		hash = []byte(record.PasswordHash)
	}
	valid := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	return exists && valid
}

// storedUser — Представление пользователя без хеша пароля для административной сводки.
type storedUser struct {
	Username  string
	CreatedAt time.Time
	Role      string
}

// storedInvite — Действующее приглашение с токеном, временем и назначаемой ролью.
type storedInvite struct {
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
	Role      string
}

// snapshot возвращает представления пользователей и действующих приглашений без хешей паролей.
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

// role читает роль аккаунта под блокировкой хранилища.
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

// setRole проверяет допустимую роль и сохраняет изменение; ошибка записи возвращает прежнюю запись.
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

// revokeInvite удаляет инвайт и восстанавливает его в памяти, если сохранение не удалось.
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

// deleteUser удаляет аккаунт из базы; ошибка записи возвращает прежнее состояние.
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

// saveLocked записывает базу в закрытый временный файл и заменяет основной файл переименованием. Мьютекс
// authStore уже должен быть захвачен вызывающим кодом.
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

// session — Сессия одного пользователя со сроком действия и временем активности.
type session struct {
	Username  string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

// sessionStore — Сессии только в памяти с защитой от параллельных запросов.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]session
}

const (
	maxSessionsPerUser = 10
	maxSessionsTotal   = 10000
)

// newSessionStore создаёт хранилище сессий в памяти; перезапуск сервера завершает все такие сессии.
func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]session)}
}

// create создаёт случайную сессию со сроком действия, удаляя истёкшие и лишние записи при достижении
// лимитов.
func (s *sessionStore) create(username string) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	now := time.Now()
	s.mu.Lock()
	for existingToken, entry := range s.sessions {
		if now.After(entry.ExpiresAt) {
			delete(s.sessions, existingToken)
		}
	}
	for sessionCount(s.sessions, username) >= maxSessionsPerUser || len(s.sessions) >= maxSessionsTotal {
		oldestToken := ""
		var oldest time.Time
		for existingToken, entry := range s.sessions {
			if len(s.sessions) < maxSessionsTotal && entry.Username != username {
				continue
			}
			if oldestToken == "" || entry.CreatedAt.Before(oldest) {
				oldestToken, oldest = existingToken, entry.CreatedAt
			}
		}
		if oldestToken == "" {
			break
		}
		delete(s.sessions, oldestToken)
	}
	s.sessions[token] = session{Username: username, CreatedAt: now, LastSeen: now, ExpiresAt: expires}
	s.mu.Unlock()
	return token, expires, nil
}

// sessionCount считает сессии указанного пользователя в переданном наборе.
func sessionCount(sessions map[string]session, username string) int {
	count := 0
	for _, entry := range sessions {
		if entry.Username == username {
			count++
		}
	}
	return count
}

// get проверяет наличие и срок сессии, возвращая связанный логин.
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

// delete удаляет одну сессию при выходе.
func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// presence — Число активных сессий и последнее обращение пользователя.
type presence struct {
	Sessions int
	LastSeen time.Time
}

// presence считает действующие сессии пользователей и последнее время активности.
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

// deleteUser завершает все сессии удаляемого пользователя.
func (s *sessionStore) deleteUser(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, entry := range s.sessions {
		if entry.Username == username {
			delete(s.sessions, token)
		}
	}
}

// clear завершает все сессии, например после восстановления базы доступа.
func (s *sessionStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = make(map[string]session)
}

// randomToken получает криптографически случайные байты и кодирует их для безопасного использования в URL.
func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// secureRequest распознаёт HTTPS. Заголовку прокси доверяет только при явном включении HKC_TRUST_PROXY и
// локальном адресе отправителя.
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	proxyIP := net.ParseIP(host)
	trustedProxy := os.Getenv("HKC_TRUST_PROXY") == "1" && err == nil && proxyIP != nil && proxyIP.IsLoopback()
	return trustedProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSessionCookie выдаёт cookie с HttpOnly, SameSite и признаком Secure для HTTPS.
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

// clearSessionCookie истекает cookie с тем же именем и областью действия.
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

// constantTimeEqual сравнивает равные по длине секреты за время, не зависящее от совпадения их байтов.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
