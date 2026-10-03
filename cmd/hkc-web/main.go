package main

import (
	"context"
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
	"sync"
	"time"

	"heroku-console/botproc"
	"heroku-console/logfeed"
)

//go:embed static/*
var staticFiles embed.FS

type server struct {
	bot          *botproc.Manager
	auth         *authStore
	sessions     *sessionStore
	adminToken   string
	audit        *auditStore
	metrics      *metricStore
	hostHistory  *hostHistoryStore
	notifier     *stateNotifier
	configMu     sync.Mutex
	systemMu     sync.RWMutex
	latestSystem systemStatus
	updates      *updateChecker
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
	if len(os.Args) == 2 && os.Args[1] == "--version-json" {
		_ = json.NewEncoder(os.Stdout).Encode(currentVersion())
		return
	}
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
	if path := os.Getenv("HKC_ADMIN_TOKEN_FILE"); adminToken == "" && path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal("cannot read administrative token file")
		}
		adminToken = strings.TrimSpace(string(data))
		if adminToken == "" {
			log.Fatal("administrative token file is empty")
		}
	}
	if adminToken == "" {
		adminToken, err = randomToken(32)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("временный HKC_ADMIN_TOKEN: %s", adminToken)
		log.Print("задайте HKC_ADMIN_TOKEN в окружении для постоянного административного доступа")
	}

	notifier := configuredWebhook()
	if notifier != nil {
		notifier.observe(botproc.PID() != 0)
	}
	s := &server{bot: botproc.New(herokuDir), auth: auth, sessions: newSessionStore(), adminToken: adminToken,
		audit: newAuditStore(filepath.Join(filepath.Dir(authFile), "audit.jsonl")), metrics: newMetricStore(), notifier: notifier}
	s.hostHistory = newHostHistoryStore(filepath.Join(filepath.Dir(authFile), "host-history.jsonl"))
	go s.collectHostHistory(context.Background())
	go watchBotState(context.Background(), notifier)
	s.updates = &updateChecker{}
	s.updates.check()
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.updates.check()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, currentVersion()) })
	mux.HandleFunc("GET /api/admin/updates", s.updateStatus)
	mux.HandleFunc("GET /api/admin/updates/progress", s.updateProgress)
	mux.HandleFunc("POST /api/admin/updates/check", s.updateStatus)
	mux.HandleFunc("POST /api/admin/updates/install", s.installUpdate)
	mux.HandleFunc("GET /api/auth/me", s.authorize(s.me))
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/logout", s.authorize(s.logout))
	mux.HandleFunc("GET /api/admin/overview", s.adminOverview)
	mux.HandleFunc("GET /api/admin/audit", s.adminAudit)
	mux.HandleFunc("GET /api/admin/backups", s.listBackups)
	mux.HandleFunc("POST /api/admin/backups", s.createBackup)
	mux.HandleFunc("POST /api/admin/backups/{name}/restore", s.restoreBackup)
	mux.HandleFunc("GET /api/admin/config", s.adminConfig)
	mux.HandleFunc("PATCH /api/admin/config", s.updateConfig)
	mux.HandleFunc("DELETE /api/admin/config/{key}", s.deleteConfigKey)
	mux.HandleFunc("POST /api/admin/diagnostics/{command}", s.adminDiagnosticCommand)
	mux.HandleFunc("POST /api/admin/terminal", s.adminTerminal)
	mux.HandleFunc("GET /api/admin/tokens", s.listAPITokens)
	mux.HandleFunc("POST /api/admin/tokens", s.createAPIToken)
	mux.HandleFunc("DELETE /api/admin/tokens/{id}", s.revokeAPIToken)
	mux.HandleFunc("GET /api/v1/status", s.apiAuthorize("read", s.status))
	mux.HandleFunc("GET /api/v1/logs", s.apiAuthorize("read", s.logs))
	mux.HandleFunc("POST /api/v1/bot/{action}", s.apiAuthorize("control", s.action))
	mux.HandleFunc("POST /api/admin/invites", s.createInvite)
	mux.HandleFunc("POST /api/admin/bot/{action}", s.adminBotAction)
	mux.HandleFunc("DELETE /api/admin/invites/{token}", s.revokeInvite)
	mux.HandleFunc("DELETE /api/admin/users/{username}", s.deleteUser)
	mux.HandleFunc("PATCH /api/admin/users/{username}/role", s.changeRole)
	mux.HandleFunc("GET /api/status", s.authorize(s.status))
	mux.HandleFunc("GET /api/insights", s.authorize(s.insights))
	mux.HandleFunc("GET /api/metrics", s.authorize(s.liveMetrics))
	mux.HandleFunc("GET /api/system", s.authorize(s.systemHealth))
	mux.HandleFunc("GET /api/system/history", s.authorize(s.systemHistory))
	mux.HandleFunc("GET /api/incidents", s.authorize(s.incidents))
	mux.HandleFunc("GET /api/diagnostics", s.authorize(s.diagnostics))
	mux.HandleFunc("GET /api/public/status", s.publicStatus)
	mux.HandleFunc("GET /api/logs", s.authorize(s.logs))
	mux.HandleFunc("GET /api/events", s.authorize(s.events))
	mux.HandleFunc("POST /api/bot/{action}", s.authorizeControl(s.action))

	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(assets)))

	log.Printf("hrk-console web: http://%s", addr)
	log.Printf("каталог бота: %s", herokuDir)
	log.Printf("база авторизации: %s", authFile)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if err := httpServer.ListenAndServe(); err != nil {
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
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; script-src 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
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
		username, ok := s.sessions.get(cookie.Value)
		if !ok || s.auth.role(username) == "" {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "сессия истекла"})
			return
		}
		next(w, r)
	}
}

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

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	username, _ := s.sessions.get(cookie.Value)
	writeJSON(w, http.StatusOK, map[string]string{"username": username, "role": s.auth.role(username)})
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
	s.record(r, input.Username, "auth.login", "Вход в панель")
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
	s.record(r, input.Username, "auth.register", "Создан аккаунт")
	writeJSON(w, http.StatusCreated, actionResponse{OK: true, Message: "аккаунт создан"})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.delete(cookie.Value)
	}
	clearSessionCookie(w, r)
	s.record(r, "user", "auth.logout", "Выход из панели")
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "выход выполнен"})
}

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
	if status == http.StatusOK {
		s.notifier.observe(botproc.PID() != 0)
	}
	if status == http.StatusOK {
		s.record(r, "admin", "bot."+r.PathValue("action"), result.Message)
	}
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
	s.record(r, "admin", "invite.revoke", "Инвайт отозван")
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
	s.record(r, "admin", "user.delete", username)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "пользователь удалён"})
}

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
	if status == http.StatusOK {
		s.notifier.observe(botproc.PID() != 0)
	}
	if status == http.StatusOK {
		actor := "user"
		if apiActor, ok := r.Context().Value(apiActorKey{}).(string); ok {
			actor = apiActor
		}
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if username, ok := s.sessions.get(cookie.Value); ok {
				actor = username
			}
		}
		s.record(r, actor, "bot."+r.PathValue("action"), result.Message)
	}
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
