package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"heroku-console/botproc"
)

// Мини-приложение Telegram — та же панель в компактном виде внутри Telegram. Входа нет: клиент
// Telegram подписывает initData токеном бота, сервер проверяет подпись и выдаёт короткую сессию
// только администраторам из HKC_TELEGRAM_ADMIN_IDS. Сессия живёт в памяти и передаётся заголовком,
// потому что cookie внутри веб-вида Telegram ненадёжны.

const (
	// initData старше суток не принимается: подпись не должна жить вечно.
	telegramInitDataMaxAge = 24 * time.Hour
	telegramWebSessionTTL  = 12 * time.Hour
)

type telegramWebSession struct {
	user    tgUser
	expires time.Time
}

type telegramWebSessions struct {
	mu    sync.Mutex
	items map[string]telegramWebSession
}

func (store *telegramWebSessions) create(user tgUser, now time.Time) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.items == nil {
		store.items = map[string]telegramWebSession{}
	}
	for key, session := range store.items {
		if now.After(session.expires) {
			delete(store.items, key)
		}
	}
	store.items[token] = telegramWebSession{user: user, expires: now.Add(telegramWebSessionTTL)}
	return token, nil
}

func (store *telegramWebSessions) get(token string, now time.Time) (tgUser, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.items[token]
	if !ok || now.After(session.expires) {
		delete(store.items, token)
		return tgUser{}, false
	}
	return session.user, true
}

// verifyTelegramInitData проверяет подпись initData по алгоритму Telegram: ключ —
// HMAC-SHA256("WebAppData", токен бота), подпись — HMAC-SHA256 от отсортированных полей без hash.
func verifyTelegramInitData(initData, botToken string, now time.Time) (tgUser, error) {
	values, err := url.ParseQuery(initData)
	if err != nil {
		return tgUser{}, errors.New("повреждённые данные запуска")
	}
	hash := values.Get("hash")
	if hash == "" {
		return tgUser{}, errors.New("нет подписи Telegram")
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
		return tgUser{}, errors.New("подпись Telegram не совпала")
	}
	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return tgUser{}, errors.New("нет времени запуска")
	}
	if age := now.Sub(time.Unix(authDate, 0)); age > telegramInitDataMaxAge || age < -5*time.Minute {
		return tgUser{}, errors.New("данные запуска устарели — откройте приложение заново")
	}
	var user tgUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return tgUser{}, errors.New("нет пользователя в данных запуска")
	}
	return user, nil
}

// webAppReady — мини-приложение доступно: есть токен, адрес и включено управление.
func (s *server) webAppReady() (string, bool) {
	c := s.telegram
	switch {
	case c == nil || c.api == nil:
		return "Telegram-бот не настроен", false
	case c.webAppURL() == "":
		return "мини-приложение не настроено: нужен HKC_TELEGRAM_WEBAPP_URL", false
	case !c.settings().ControlEnabled:
		return "управление из Telegram выключено в админке", false
	}
	return "", true
}

// telegramSessionStart обменивает подписанные данные запуска на сессию мини-приложения.
func (s *server) telegramSessionStart(w http.ResponseWriter, r *http.Request) {
	limitKey := "telegram:" + clientAddress(r)
	if !s.allowAuthAttempt(w, limitKey) {
		return
	}
	if reason, ok := s.webAppReady(); !ok {
		writeJSON(w, http.StatusServiceUnavailable, actionResponse{Message: reason})
		return
	}
	var input struct {
		InitData string `json:"initData"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&input); err != nil || input.InitData == "" {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "откройте приложение из Telegram"})
		return
	}
	user, err := verifyTelegramInitData(input.InitData, s.telegram.config.Token, time.Now())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: err.Error()})
		return
	}
	if !s.telegram.admins[user.ID] {
		s.telegram.rememberUnknown(user, "mini app")
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "у этого аккаунта Telegram нет доступа к панели", "id": strconv.FormatInt(user.ID, 10)})
		return
	}
	s.authLimiter.success(limitKey)
	token, err := s.telegram.web.create(user, time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось создать сессию"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": map[string]any{"name": user.displayName(), "username": user.Username},
		"expiresIn": int(telegramWebSessionTTL.Seconds())})
}

type telegramUserKey struct{}

// telegramAuthorize пропускает запрос с действующей сессией мини-приложения. Доступ перепроверяется
// каждый раз: выключенное управление или убранный из списка ID закрывают его сразу.
func (s *server) telegramAuthorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reason, ok := s.webAppReady(); !ok {
			writeJSON(w, http.StatusServiceUnavailable, actionResponse{Message: reason})
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		user, ok := s.telegram.web.get(token, time.Now())
		if !ok || !s.telegram.admins[user.ID] {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "сессия истекла — откройте приложение заново"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), telegramUserKey{}, user)))
	}
}

// telegramOverview — всё для главного экрана одним запросом: процесс, сервер, автовосстановление.
func (s *server) telegramOverview(w http.ResponseWriter, r *http.Request) {
	pid := s.bot.PID()
	s.systemMu.RLock()
	system := s.latestSystem
	live := s.latestLive
	s.systemMu.RUnlock()
	watchdog := map[string]any{"enabled": false}
	if s.operations != nil {
		snapshot := s.watchdog.snapshot()
		watchdog = map[string]any{"enabled": s.operations.watchdogSettings().Enabled, "state": snapshot.State,
			"message": snapshot.Message, "attempts": snapshot.Attempts}
	}
	user, _ := r.Context().Value(telegramUserKey{}).(tgUser)
	host, _ := os.Hostname()
	bot := map[string]any{"running": pid != 0, "pid": pid, "uptime": "—", "version": s.bot.Version()}
	if pid != 0 {
		bot["uptime"] = botproc.Uptime(pid)
		bot["rssBytes"] = live.bot.RSS
		bot["cpuPercent"] = live.bot.CPUPercent
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bot": bot, "watchdog": watchdog, "host": host, "panel": currentVersion(),
		"system": map[string]any{"supported": system.Supported, "cpuPercent": system.CPUPercent,
			"memoryPercent": hostPercent(system.MemoryUsed, system.MemoryTotal), "diskPercent": hostPercent(system.DiskUsed, system.DiskTotal),
			"memoryUsedBytes": system.MemoryUsed, "memoryTotalBytes": system.MemoryTotal, "diskUsedBytes": system.DiskUsed,
			"diskTotalBytes": system.DiskTotal, "load1": system.Load1, "cores": system.CPUCores, "uptimeSeconds": system.UptimeSeconds},
		"user":    map[string]any{"name": user.displayName(), "username": user.Username},
		"confirm": s.telegram.settings().Confirm,
	})
}

// telegramBotAction выполняет действие из мини-приложения и пишет его в журнал от имени пользователя Telegram.
func (s *server) telegramBotAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "неизвестное действие"})
		return
	}
	user, _ := r.Context().Value(telegramUserKey{}).(tgUser)
	result, status := s.performAction(action)
	if status == http.StatusOK && s.notifier != nil {
		s.notifier.observe(s.bot.PID() != 0)
	}
	s.record(r, user.actor(), "bot."+action, result.Message)
	writeJSON(w, status, result)
}

// telegramLogs — хвост журнала бота; во внешний мир строки уходят только владельцу по HTTPS.
func (s *server) telegramLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if limit, err := strconv.Atoi(query.Get("limit")); err != nil || limit <= 0 || limit > 400 {
		query.Set("limit", "200")
		r.URL.RawQuery = query.Encode()
	}
	s.logs(w, r)
}
