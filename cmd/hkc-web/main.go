package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"heroku-console/botproc"
	"heroku-console/logfeed"
)

//go:embed static/*
var staticFiles embed.FS

type server struct {
	bot        *botproc.Manager
	auth       *authStore
	sessions   *sessionStore
	adminToken string
}

type statusResponse struct {
	Running    bool   `json:"running"`
	PID        int    `json:"pid"`
	Uptime     string `json:"uptime"`
	Version    string `json:"version"`
	HerokuDir  string `json:"herokuDir"`
	LogReady   bool   `json:"logReady"`
	StartupLog string `json:"startupLog,omitempty"`
}

type actionResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	PID     int    `json:"pid,omitempty"`
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	herokuDir := os.Getenv("HEROKU_DIR")
	if herokuDir == "" {
		herokuDir = filepath.Join(home, "Heroku")
	}
	addr := envOr("HKC_WEB_ADDR", "127.0.0.1:8080")
	authFile := os.Getenv("HKC_AUTH_FILE")
	if authFile == "" {
		authFile = filepath.Join(home, ".config", "hkc", "web-auth.json")
	}
	auth, err := openAuthStore(authFile)
	if err != nil {
		log.Fatal(err)
	}
	adminToken := os.Getenv("HKC_ADMIN_TOKEN")
	if adminToken == "" {
		adminToken, err = randomToken(32)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("временный HKC_ADMIN_TOKEN: %s", adminToken)
		log.Print("задайте HKC_ADMIN_TOKEN в окружении для постоянного административного доступа")
	}

	s := &server{bot: botproc.New(herokuDir), auth: auth, sessions: newSessionStore(), adminToken: adminToken}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/me", s.authorize(s.me))
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/logout", s.authorize(s.logout))
	mux.HandleFunc("GET /api/admin/overview", s.adminOverview)
	mux.HandleFunc("POST /api/admin/invites", s.createInvite)
	mux.HandleFunc("POST /api/admin/bot/{action}", s.adminBotAction)
	mux.HandleFunc("DELETE /api/admin/invites/{token}", s.revokeInvite)
	mux.HandleFunc("DELETE /api/admin/users/{username}", s.deleteUser)
	mux.HandleFunc("GET /api/status", s.authorize(s.status))
	mux.HandleFunc("GET /api/logs", s.authorize(s.logs))
	mux.HandleFunc("GET /api/events", s.authorize(s.events))
	mux.HandleFunc("POST /api/bot/{action}", s.authorize(s.action))

	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(assets)))

	log.Printf("hrk-console web: http://%s", addr)
	log.Printf("каталог бота: %s", herokuDir)
	log.Printf("база авторизации: %s", authFile)
	if err := http.ListenAndServe(addr, securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func loopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; script-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "требуется вход"})
			return
		}
		if _, ok := s.sessions.get(cookie.Value); !ok {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "сессия истекла"})
			return
		}
		next(w, r)
	}
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	username, _ := s.sessions.get(cookie.Value)
	writeJSON(w, http.StatusOK, map[string]string{"username": username})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	if !s.auth.authenticate(input.Username, input.Password) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный логин или пароль"})
		return
	}
	token, expires, err := s.sessions.create(strings.TrimSpace(input.Username))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "вход выполнен"})
}

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
	if err := s.auth.register(input.Invite, input.Username, input.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	token, expires, err := s.sessions.create(strings.TrimSpace(input.Username))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusCreated, actionResponse{OK: true, Message: "аккаунт создан"})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.delete(cookie.Value)
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "выход выполнен"})
}

func (s *server) createInvite(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	var input struct {
		ExpiresHours int `json:"expiresHours"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	if input.ExpiresHours == 0 {
		input.ExpiresHours = 24
	}
	if input.ExpiresHours < 1 || input.ExpiresHours > 720 {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "expiresHours должен быть от 1 до 720"})
		return
	}
	token, expires, err := s.auth.createInvite(time.Duration(input.ExpiresHours) * time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	registrationURL := fmt.Sprintf("%s://%s/?invite=%s", scheme, r.Host, token)
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite":          token,
		"expiresAt":       expires,
		"registrationUrl": registrationURL,
	})
}

func (s *server) adminAuthorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return constantTimeEqual(provided, s.adminToken)
}

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
		CreatedAt      time.Time  `json:"createdAt"`
		Online         bool       `json:"online"`
		LastSeen       *time.Time `json:"lastSeen,omitempty"`
		ActiveSessions int        `json:"activeSessions"`
	}
	type inviteView struct {
		Token           string    `json:"token"`
		CreatedAt       time.Time `json:"createdAt"`
		ExpiresAt       time.Time `json:"expiresAt"`
		RegistrationURL string    `json:"registrationUrl"`
	}
	userViews := make([]userView, 0, len(users))
	for _, user := range users {
		view := userView{Username: user.Username, CreatedAt: user.CreatedAt}
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
			Token: invite.Token, CreatedAt: invite.CreatedAt, ExpiresAt: invite.ExpiresAt,
			RegistrationURL: fmt.Sprintf("%s://%s/?invite=%s", scheme, r.Host, invite.Token),
		})
	}
	pid := botproc.PID()
	writeJSON(w, http.StatusOK, map[string]any{
		"users": userViews, "invites": inviteViews, "onlineWindowSeconds": 30,
		"bot": statusResponse{Running: pid != 0, PID: pid, Uptime: botproc.Uptime(pid), Version: s.bot.Version(), HerokuDir: s.bot.HerokuDir},
	})
}

func (s *server) adminBotAction(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	result, status := s.performAction(r.PathValue("action"))
	writeJSON(w, status, result)
}

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
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "инвайт отозван"})
}

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
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "пользователь удалён"})
}

func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	pid := botproc.PID()
	_, logErr := os.Stat(s.bot.LogFile)
	writeJSON(w, http.StatusOK, statusResponse{
		Running:    pid != 0,
		PID:        pid,
		Uptime:     botproc.Uptime(pid),
		Version:    s.bot.Version(),
		HerokuDir:  s.bot.HerokuDir,
		LogReady:   logErr == nil,
		StartupLog: tailText(s.bot.StartupLog, 12),
	})
}

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 5000 {
			limit = parsed
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": logfeed.TailLines(s.bot.LogFile, limit)})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	follower, err := logfeed.Follow(s.bot.LogFile, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer follower.Stop()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, open := <-follower.Lines:
			if !open {
				return
			}
			payload, _ := json.Marshal(line)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *server) action(w http.ResponseWriter, r *http.Request) {
	result, status := s.performAction(r.PathValue("action"))
	writeJSON(w, status, result)
}

func (s *server) performAction(action string) (actionResponse, int) {
	result := actionResponse{OK: true}
	switch action {
	case "start":
		started := s.bot.Start()
		if started.Err != nil {
			result.OK = false
			result.Message = started.Err.Error()
		} else if started.AlreadyStarting {
			result.Message = "запуск уже выполняется"
		} else {
			result.PID = started.PID
			result.Message = "бот запущен"
		}
	case "stop":
		code := s.bot.Stop()
		result.OK = code != 2
		if code == 1 {
			result.Message = "бот уже остановлен"
		} else if code == 2 {
			result.Message = "бот остановлен принудительно"
		} else {
			result.Message = "бот остановлен"
		}
	case "restart":
		s.bot.Stop()
		started := s.bot.Start()
		if started.Err != nil {
			result.OK = false
			result.Message = started.Err.Error()
		} else {
			result.PID = started.PID
			result.Message = "бот перезапущен"
		}
	default:
		return actionResponse{Message: "неизвестное действие"}, http.StatusNotFound
	}
	status := http.StatusOK
	if !result.OK {
		status = http.StatusConflict
	}
	return result, status
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func tailText(path string, maxLines int) string {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
