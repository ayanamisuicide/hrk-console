package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"heroku-console/internal/telegram"
)

// principal — кто обращается к API панели: пользователь, вошедший по паролю (сессионная cookie),
// или администратор Telegram из мини-приложения (сессия за подписанные данные запуска). Мини-приложение —
// та же панель, поэтому оба ходят в одни и те же обработчики.
type principal struct {
	// name — подпись в журнале действий: логин или telegram:@username.
	name string
	role string
	// telegram — пользователь Telegram, если запрос пришёл из мини-приложения.
	telegram *telegram.User
}

type principalKey struct{}

// principalOf определяет, кто сделал запрос. status != 0 — доступа нет, message объясняет почему.
func (s *server) principalOf(r *http.Request) (p principal, status int, message string) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		username, ok := s.sessions.get(cookie.Value)
		if !ok || s.auth.role(username) == "" {
			return principal{}, http.StatusUnauthorized, "сессия истекла"
		}
		return principal{name: username, role: s.auth.role(username)}, 0, ""
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		user, status, message := s.telegramSession(r)
		if status != 0 {
			// Мини-приложение по 401 заново входит через /api/tg/session и там узнаёт, почему нельзя.
			return principal{}, http.StatusUnauthorized, message
		}
		// Администраторы Telegram управляют ботом и в чате, поэтому в панели они операторы.
		return principal{name: user.Actor(), role: "operator", telegram: &user}, 0, ""
	}
	return principal{}, http.StatusUnauthorized, "требуется вход"
}

// requestPrincipal — кто сделал запрос, прошедший authorize.
func requestPrincipal(r *http.Request) (principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(principal)
	return p, ok
}

// authorize пропускает запрос вошедшего пользователя панели или администратора Telegram.
func (s *server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, status, message := s.principalOf(r)
		if status != 0 {
			writeJSON(w, status, actionResponse{Message: message})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// authorizeControl дополнительно проверяет право управления: роль viewer может только читать данные.
func (s *server) authorizeControl(next http.HandlerFunc) http.HandlerFunc {
	return s.authorize(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := requestPrincipal(r); p.role != "operator" {
			writeJSON(w, http.StatusForbidden, actionResponse{Message: "недостаточно прав для управления ботом"})
			return
		}
		next(w, r)
	})
}

// me возвращает имя и роль вошедшего пользователя, не раскрывая запись пароля. Для мини-приложения
// это имя в Telegram; share — можно ли отправить запись журнала в Telegram (себе или в чат уведомлений).
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	p, _ := requestPrincipal(r)
	view := map[string]any{"username": p.name, "role": p.role, "share": s.shareChat(p) != 0}
	if p.telegram != nil {
		view["username"] = p.telegram.DisplayName()
		view["telegram"] = map[string]any{"id": strconv.FormatInt(p.telegram.ID, 10), "username": p.telegram.Username}
	}
	writeJSON(w, http.StatusOK, view)
}

// login проверяет ограничение попыток и пароль, затем выдаёт сессионную cookie.
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	limitKey := "login:" + clientAddress(r)
	if !s.allowAuthAttempt(w, limitKey) {
		return
	}
	if !s.auth.authenticate(input.Username, input.Password) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный логин или пароль"})
		return
	}
	s.authLimiter.success(limitKey)
	token, expires, err := s.sessions.create(strings.TrimSpace(input.Username))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	setSessionCookie(w, r, token, expires)
	s.record(r, input.Username, "auth.login", "Вход в панель")
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "вход выполнен"})
}

// register создаёт пользователя по одноразовому приглашению и сразу открывает его сессию.
func (s *server) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Invite   string `json:"invite"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	limitKey := "register:" + clientAddress(r)
	if !s.allowAuthAttempt(w, limitKey) {
		return
	}
	if err := s.auth.register(input.Invite, input.Username, input.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	s.authLimiter.success(limitKey)
	token, expires, err := s.sessions.create(strings.TrimSpace(input.Username))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	setSessionCookie(w, r, token, expires)
	s.record(r, input.Username, "auth.register", "Создан аккаунт")
	writeJSON(w, http.StatusCreated, actionResponse{OK: true, Message: "аккаунт создан"})
}

// logout удаляет текущую сессию на сервере и очищает cookie в браузере.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.delete(cookie.Value)
	}
	clearSessionCookie(w, r)
	s.record(r, "user", "auth.logout", "Выход из панели")
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "выход выполнен"})
}
