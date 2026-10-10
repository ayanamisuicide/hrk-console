package main

import (
	"encoding/json"
	"fmt"
	"heroku-console/botproc"
	"net/http"
	"strings"
	"time"
)

// createInvite проверяет административный токен и создаёт приглашение с заданной ролью.
func (s *server) createInvite(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	var input struct {
		ExpiresHours int    `json:"expiresHours"`
		Role         string `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	if input.ExpiresHours == 0 {
		input.ExpiresHours = 24
	}
	if input.ExpiresHours < 1 || input.ExpiresHours > 720 {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "expiresHours должен быть от 1 до 720"})
		return
	}
	if input.Role == "" {
		input.Role = "operator"
	}
	token, expires, err := s.auth.createInviteWithRole(time.Duration(input.ExpiresHours)*time.Hour, input.Role)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	registrationURL := fmt.Sprintf("%s://%s/?invite=%s", scheme, r.Host, token)
	s.record(r, "admin", "invite.create", fmt.Sprintf("Инвайт на %d ч", input.ExpiresHours))
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite":          token,
		"expiresAt":       expires,
		"role":            input.Role,
		"registrationUrl": registrationURL,
	})
}

// adminAuthorized сверяет административный Bearer-токен с настройкой сервера без обычного сравнения строк.
func (s *server) adminAuthorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return constantTimeEqual(provided, s.adminToken)
}

// adminOverview собирает сводку бота, пользователей и приглашений для административной страницы.
func (s *server) adminOverview(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	users, invites := s.auth.snapshot()
	presenceByUser := s.sessions.presence()
	now := time.Now()
	type userView struct {
		Username       string     `json:"username"`
		Role           string     `json:"role"`
		CreatedAt      time.Time  `json:"createdAt"`
		Online         bool       `json:"online"`
		LastSeen       *time.Time `json:"lastSeen,omitempty"`
		ActiveSessions int        `json:"activeSessions"`
	}
	type inviteView struct {
		Token           string    `json:"token"`
		Role            string    `json:"role"`
		CreatedAt       time.Time `json:"createdAt"`
		ExpiresAt       time.Time `json:"expiresAt"`
		RegistrationURL string    `json:"registrationUrl"`
	}
	userViews := make([]userView, 0, len(users))
	for _, user := range users {
		view := userView{Username: user.Username, Role: user.Role, CreatedAt: user.CreatedAt}
		if p, ok := presenceByUser[user.Username]; ok {
			lastSeen := p.LastSeen
			view.LastSeen = &lastSeen
			view.ActiveSessions = p.Sessions
			view.Online = now.Sub(lastSeen) <= 30*time.Second
		}
		userViews = append(userViews, view)
	}
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	inviteViews := make([]inviteView, 0, len(invites))
	for _, invite := range invites {
		inviteViews = append(inviteViews, inviteView{
			Token: invite.Token, Role: invite.Role, CreatedAt: invite.CreatedAt, ExpiresAt: invite.ExpiresAt,
			RegistrationURL: fmt.Sprintf("%s://%s/?invite=%s", scheme, r.Host, invite.Token),
		})
	}
	pid := s.bot.PID()
	writeJSON(w, http.StatusOK, map[string]any{
		"users": userViews, "invites": inviteViews, "onlineWindowSeconds": 30,
		"bot": statusResponse{Running: pid != 0, PID: pid, Uptime: botproc.Uptime(pid), Version: s.bot.Version(), HerokuDir: s.bot.HerokuDir},
	})
}

// adminBotAction выполняет административное действие над ботом и фиксирует успешный результат в аудите.
func (s *server) adminBotAction(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	result, status := s.performAction(r.PathValue("action"))
	if status == http.StatusOK {
		s.notifier.observe(s.bot.PID() != 0)
	}
	if status == http.StatusOK {
		s.record(r, "admin", "bot."+r.PathValue("action"), result.Message)
	}
	writeJSON(w, status, result)
}

// revokeInvite отзывает одноразовое приглашение после проверки административного доступа.
func (s *server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	ok, err := s.auth.revokeInvite(r.PathValue("token"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "инвайт не найден"})
		return
	}
	s.record(r, "admin", "invite.revoke", "Инвайт отозван")
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "инвайт отозван"})
}

// deleteUser удаляет пользователя и завершает его активные сессии.
func (s *server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	username := r.PathValue("username")
	ok, err := s.auth.deleteUser(username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "пользователь не найден"})
		return
	}
	s.sessions.deleteUser(username)
	s.record(r, "admin", "user.delete", username)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "пользователь удалён"})
}

// changeRole меняет роль пользователя после проверки административного доступа.
func (s *server) changeRole(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	username := r.PathValue("username")
	ok, err := s.auth.setRole(username, input.Role)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "пользователь не найден"})
		return
	}
	s.record(r, "admin", "user.role", username+" → "+input.Role)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "роль изменена"})
}
