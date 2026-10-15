package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"heroku-console/internal/telegram"
)

// Мини-приложение Telegram — та же панель в компактном виде внутри Telegram. Входа нет: клиент
// Telegram подписывает initData токеном бота, сервер проверяет подпись и выдаёт короткую сессию
// только администраторам из HKC_TELEGRAM_ADMIN_IDS. Сессия живёт в памяти и передаётся заголовком,
// потому что cookie внутри веб-вида Telegram ненадёжны.

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
	user, err := telegram.VerifyInitData(input.InitData, s.telegram.config.Token, time.Now())
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
	token, err := s.telegram.web.Create(user, time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось создать сессию"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": map[string]any{"name": user.DisplayName(), "username": user.Username},
		"expiresIn": int(telegram.WebSessionTTL.Seconds())})
}

// telegramSession проверяет сессию мини-приложения из заголовка Authorization. Доступ перепроверяется
// каждый раз: выключенное управление или убранный из списка ID закрывают его сразу.
func (s *server) telegramSession(r *http.Request) (telegram.User, int, string) {
	if reason, ok := s.webAppReady(); !ok {
		return telegram.User{}, http.StatusServiceUnavailable, reason
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	user, ok := s.telegram.web.Get(token, time.Now())
	if !ok || !s.telegram.admins[user.ID] {
		return telegram.User{}, http.StatusUnauthorized, "сессия истекла — откройте приложение заново"
	}
	return user, 0, ""
}

// miniApp отдаёт мини-приложению страницу панели: это та же панель, только вход — через Telegram.
// Заголовки /tg/ разрешают встраивание в веб-клиент Telegram и скрипт его моста.
func (s *server) miniApp(w http.ResponseWriter, _ *http.Request) {
	page, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "страница панели недоступна", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}
