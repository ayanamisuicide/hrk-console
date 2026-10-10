package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// authorize проверяет сессионную cookie перед передачей запроса защищённому обработчику.
func (s *server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "требуется вход"})
			return
		}
		username, ok := s.sessions.get(cookie.Value)
		if !ok || s.auth.role(username) == "" {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "сессия истекла"})
			return
		}
		next(w, r)
	}
}

// authorizeControl дополнительно проверяет право управления: роль viewer может только читать данные.
func (s *server) authorizeControl(next http.HandlerFunc) http.HandlerFunc {
	return s.authorize(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(sessionCookie)
		username, _ := s.sessions.get(cookie.Value)
		if s.auth.role(username) != "operator" {
			writeJSON(w, http.StatusForbidden, actionResponse{Message: "недостаточно прав для управления ботом"})
			return
		}
		next(w, r)
	})
}

// me возвращает имя и роль вошедшего пользователя, не раскрывая запись пароля.
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	username, _ := s.sessions.get(cookie.Value)
	writeJSON(w, http.StatusOK, map[string]string{"username": username, "role": s.auth.role(username)})
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
