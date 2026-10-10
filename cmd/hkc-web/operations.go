package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type watchdogSettings struct {
	Enabled        bool `json:"enabled"`
	TimeoutSeconds int  `json:"timeoutSeconds"`
}

// Старые поля operations.json игнорируются: удалённые задания больше не исполняются.
type operationStore struct {
	mu       sync.Mutex
	path     string
	settings watchdogSettings
}

func openOperationStore(path string) (*operationStore, error) {
	store := &operationStore{path: path, settings: watchdogSettings{Enabled: true, TimeoutSeconds: 180}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, errors.New("operations.json слишком большой")
	}
	var saved struct {
		Watchdog *watchdogSettings `json:"watchdog"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("чтение operations.json: %w", err)
	}
	if saved.Watchdog != nil {
		if err := validateWatchdog(*saved.Watchdog); err != nil {
			return nil, err
		}
		store.settings = *saved.Watchdog
	}
	return store, nil
}

func validateWatchdog(settings watchdogSettings) error {
	if settings.TimeoutSeconds < 30 || settings.TimeoutSeconds > 3600 {
		return errors.New("время ожидания должно быть от 30 до 3600 секунд")
	}
	return nil
}

func (store *operationStore) watchdogSettings() watchdogSettings {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.settings
}

func (store *operationStore) setWatchdog(settings watchdogSettings) error {
	if err := validateWatchdog(settings); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(store.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Watchdog watchdogSettings `json:"watchdog"`
	}{settings}, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateAtomic(store.path, data); err != nil {
		return err
	}
	store.settings = settings
	return nil
}

// Закрытый временный файл и атомарная замена сохраняют настройки при сбое записи.
func writePrivateAtomic(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".watchdog-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

type watchdogStatus struct {
	State            string     `json:"state"`
	Message          string     `json:"message"`
	RemainingSeconds int        `json:"remainingSeconds"`
	Attempts         int        `json:"attempts"`
	LastAttempt      *time.Time `json:"lastAttempt,omitempty"`
	LastResult       string     `json:"lastResult,omitempty"`
}

// Одна машина состояний ограничивает частоту повторов и даёт новому процессу
// полный срок на запуск. Настройки на диске; отсчёт и итоги текущего сеанса — в памяти.
type watchdogMonitor struct {
	mu          sync.Mutex
	status      watchdogStatus
	settings    watchdogSettings
	pid         int
	since       time.Time
	nextAttempt time.Time
}

func (monitor *watchdogMonitor) snapshot() watchdogStatus {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	result := monitor.status
	if result.State == "" {
		result.State = "starting"
		result.Message = "Начинаем наблюдение за ботом."
	}
	return result
}

func (monitor *watchdogMonitor) tick(now time.Time, settings watchdogSettings, pid int, healthy bool, reason string) bool {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if settings != monitor.settings || (pid != 0 && pid != monitor.pid) {
		monitor.since = time.Time{}
		monitor.nextAttempt = time.Time{}
	}
	monitor.settings = settings
	monitor.pid = pid
	monitor.status.RemainingSeconds = 0
	if !settings.Enabled {
		monitor.since = time.Time{}
		monitor.status.State = "disabled"
		monitor.status.Message = "Автоматическое восстановление выключено."
		return false
	}
	if healthy {
		monitor.since = time.Time{}
		monitor.status.State = "healthy"
		monitor.status.Message = "Бот работает, основной цикл Python отвечает."
		return false
	}
	if monitor.since.IsZero() {
		monitor.since = now
	}
	deadline := monitor.since.Add(time.Duration(settings.TimeoutSeconds) * time.Second)
	if monitor.nextAttempt.After(deadline) {
		deadline = monitor.nextAttempt
	}
	monitor.status.Message = reason
	if now.Before(deadline) {
		monitor.status.State = "waiting"
		monitor.status.RemainingSeconds = int(deadline.Sub(now).Seconds() + 0.999)
		return false
	}
	monitor.status.State = "recovering"
	monitor.status.Message = "Принудительно останавливаем старый процесс и запускаем Heroku заново."
	return true
}

func (monitor *watchdogMonitor) complete(now time.Time, result actionResponse) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.status.Attempts++
	monitor.status.LastAttempt = &now
	monitor.status.LastResult = result.Message
	monitor.since = now
	monitor.nextAttempt = now.Add(time.Duration(monitor.settings.TimeoutSeconds) * time.Second)
	monitor.status.State = "waiting"
	if !result.OK {
		monitor.status.State = "failed"
	}
	monitor.status.Message = result.Message
}

func (s *server) watchdogObservation(now time.Time) (int, bool, string) {
	pid := s.bot.PID()
	if pid == 0 {
		return 0, false, "Процесс бота остановлен. Ожидаем перед повторным запуском."
	}
	snapshot := readModulesSnapshot(s.bot.HerokuDir, pid, now)
	age := float64(now.UnixMilli())/1000 - snapshot.LoopAt
	if snapshot.Status == "live" && snapshot.LoopAt > 0 && age >= -2 && age <= 5 {
		return pid, true, ""
	}
	return pid, false, "Основной цикл Python не отвечает или сигнал наблюдения ещё не подключён."
}

func (s *server) runWatchdog(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if s.operations == nil {
				continue
			}
			settings := s.operations.watchdogSettings()
			pid, healthy, reason := s.watchdogObservation(now)
			if !s.watchdog.tick(now, settings, pid, healthy, reason) {
				continue
			}
			// Действия пользователя и восстановление не могут одновременно остановить/запустить бота.
			s.botActionMu.Lock()
			settings = s.operations.watchdogSettings()
			current := time.Now()
			pid, healthy, reason = s.watchdogObservation(current)
			if !s.watchdog.tick(current, settings, pid, healthy, reason) {
				s.botActionMu.Unlock()
				continue
			}
			result := s.forceBotRecovery()
			s.botActionMu.Unlock()
			s.watchdog.complete(time.Now(), result)
			if s.audit != nil {
				_ = s.audit.add(auditEvent{Time: time.Now().UTC(), Actor: "watchdog", Action: "bot.recover", Detail: result.Message, IP: "local"})
			}
			if s.notifier != nil {
				s.notifier.observe(s.bot.PID() != 0)
			}
		}
	}
}

// Stop уже применяет SIGKILL после пяти секунд. Если процесс остался (например,
// непрерываемое ожидание ядра), не выдаём его за новый запуск: повторим позже.
func (s *server) forceBotRecovery() actionResponse {
	s.bot.Stop()
	if s.bot.PID() != 0 {
		return actionResponse{Message: "Старый процесс не завершился даже после принудительной остановки. Повторим попытку."}
	}
	started := s.bot.Start()
	if started.Err != nil {
		return actionResponse{Message: "Повторный запуск не удался: " + started.Err.Error()}
	}
	if started.AlreadyStarting || started.PID == 0 {
		return actionResponse{Message: "Запуск уже выполняется. Проверим результат на следующем цикле."}
	}
	return actionResponse{OK: true, PID: started.PID, Message: "Heroku запущен заново. Ожидаем ответ основного цикла."}
}

func (s *server) requireOperations(w http.ResponseWriter, r *http.Request) bool {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return false
	}
	if s.operations == nil {
		writeJSON(w, http.StatusServiceUnavailable, actionResponse{Message: "хранилище операций недоступно"})
		return false
	}
	return true
}

func (s *server) getWatchdog(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	status := s.watchdog.snapshot()
	if runtime.GOOS != "linux" {
		status.State = "unsupported"
		status.Message = "Автовосстановление работает на сервере Linux/WSL."
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Settings watchdogSettings `json:"settings"`
		Status   watchdogStatus   `json:"status"`
	}{s.operations.watchdogSettings(), status})
}

func (s *server) setWatchdog(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	var settings watchdogSettings
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректные настройки"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "ожидался один объект настроек"})
		return
	}
	s.botActionMu.Lock()
	err := s.operations.setWatchdog(settings)
	s.botActionMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	pid, healthy, reason := s.watchdogObservation(time.Now())
	s.watchdog.tick(time.Now(), settings, pid, healthy, reason)
	s.record(r, "admin", "watchdog.update", fmt.Sprintf("enabled=%t timeout=%d", settings.Enabled, settings.TimeoutSeconds))
	s.getWatchdog(w, r)
}
