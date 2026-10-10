package main

import (
	"context"
	"embed"
	"encoding/json"
	"heroku-console/botproc"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Ресурсы, включая подкаталоги модулей и стилей, входят в бинарник.
// На сервере не нужен отдельный каталог для раздачи интерфейса.
//
//go:embed static/*
var staticFiles embed.FS

// server — Общие зависимости HTTP-обработчиков. configMu защищает операции с конфигурацией, systemMu —
// последний системный замер; у хранилищ собственные блокировки.
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
	authLimiter  *authRateLimiter
	operations   *operationStore
	botActionMu  sync.Mutex
	watchdog     watchdogMonitor
}

// statusResponse — Состояние бота для браузера; поля JSON сохраняют контракт API.
type statusResponse struct {
	Running    bool   `json:"running"`
	PID        int    `json:"pid"`
	Uptime     string `json:"uptime"`
	Version    string `json:"version"`
	HerokuDir  string `json:"herokuDir"`
	LogReady   bool   `json:"logReady"`
	StartupLog string `json:"startupLog,omitempty"`
}

// actionResponse — Единый результат управляющего действия, включая пояснение ошибки и возможный PID.
type actionResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	PID     int    `json:"pid,omitempty"`
}

// main читает окружение, открывает хранилища, запускает фоновые задачи и HTTP-сервер.
func main() {
	// Установщик читает метаданные без открытия портов и фоновых задач.
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
	if len(os.Args) == 2 && os.Args[1] == "--install-modules-bridge" {
		if err := botproc.New(herokuDir).InstallModulesBridge(); err != nil {
			log.Fatal(err)
		}
		log.Print("мониторинг модулей подключён; данные появятся после следующего запуска Heroku")
		return
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
	// Переменная имеет приоритет над файлом. Без постоянного секрета
	// выдаётся временный токен; такой режим не подходит для постоянного развёртывания.
	adminToken := os.Getenv("HKC_ADMIN_TOKEN")
	if path := os.Getenv("HKC_ADMIN_TOKEN_FILE"); adminToken == "" && path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal("не удалось прочитать файл административного токена")
		}
		adminToken = strings.TrimSpace(string(data))
		if adminToken == "" {
			log.Fatal("файл административного токена пуст")
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
		notifier.observe(botproc.New(herokuDir).PID() != 0)
	}
	s := &server{bot: botproc.New(herokuDir), auth: auth, sessions: newSessionStore(), adminToken: adminToken,
		authLimiter: newAuthRateLimiter(20, 5*time.Minute),
		audit:       newAuditStore(filepath.Join(filepath.Dir(authFile), "audit.jsonl")), metrics: newMetricStore(), notifier: notifier}
	s.operations, err = openOperationStore(filepath.Join(filepath.Dir(authFile), "operations.json"))
	if err != nil {
		log.Fatal(err)
	}
	s.hostHistory = newHostHistoryStore(filepath.Join(filepath.Dir(authFile), "host-history.jsonl"))
	// Циклы живут до остановки процесса. Их методы поддерживают отмену
	// контекстом, но здесь общий жизненный цикл задаёт сама служба systemd.
	go s.collectHostHistory(context.Background())
	go watchBotState(context.Background(), notifier, s.bot)
	go s.runWatchdog(context.Background())
	s.updates = &updateChecker{}
	s.updates.check()
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.updates.check()
		}
	}()

	log.Printf("hrk-console web: http://%s", addr)
	log.Printf("каталог бота: %s", herokuDir)
	log.Printf("база авторизации: %s", authFile)
	// Ограничиваем заголовки и чтение запроса. Общий WriteTimeout не задаём:
	// SSE держит ответ открытым, пока клиент слушает журнал.
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(s.routes()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// envOr возвращает значение переменной окружения или запасное значение, если она пуста.
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
