package telegram

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Мини-приложение Telegram: клиент подписывает initData токеном бота, а короткая сессия
// после проверки подписи живёт в памяти.

const (
	// initData старше суток не принимается: подпись не должна жить вечно.
	initDataMaxAge = 24 * time.Hour
	// WebSessionTTL — срок сессии мини-приложения.
	WebSessionTTL = 12 * time.Hour
)

type webSession struct {
	user    User
	expires time.Time
}

// WebSessions — сессии мини-приложения в памяти: токен → пользователь Telegram.
type WebSessions struct {
	mu    sync.Mutex
	items map[string]webSession
}

// Create выдаёт сессию на WebSessionTTL и попутно убирает просроченные.
func (store *WebSessions) Create(user User, now time.Time) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.items == nil {
		store.items = map[string]webSession{}
	}
	for key, session := range store.items {
		if now.After(session.expires) {
			delete(store.items, key)
		}
	}
	store.items[token] = webSession{user: user, expires: now.Add(WebSessionTTL)}
	return token, nil
}

// Get возвращает пользователя действующей сессии.
func (store *WebSessions) Get(token string, now time.Time) (User, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.items[token]
	if !ok || now.After(session.expires) {
		delete(store.items, token)
		return User{}, false
	}
	return session.user, true
}

// VerifyInitData проверяет подпись initData по алгоритму Telegram: ключ —
// HMAC-SHA256("WebAppData", токен бота), подпись — HMAC-SHA256 от отсортированных полей без hash.
func VerifyInitData(initData, botToken string, now time.Time) (User, error) {
	values, err := url.ParseQuery(initData)
	if err != nil {
		return User{}, errors.New("повреждённые данные запуска")
	}
	hash := values.Get("hash")
	if hash == "" {
		return User{}, errors.New("нет подписи Telegram")
	}
	values.Del("hash")
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(strings.ToLower(hash))) {
		return User{}, errors.New("подпись Telegram не совпала")
	}
	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return User{}, errors.New("нет времени запуска")
	}
	if age := now.Sub(time.Unix(authDate, 0)); age > initDataMaxAge || age < -5*time.Minute {
		return User{}, errors.New("данные запуска устарели — откройте приложение заново")
	}
	var user User
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return User{}, errors.New("нет пользователя в данных запуска")
	}
	return user, nil
}

// randomToken — случайный токен сессии в кодировке, безопасной для заголовков и URL.
func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
