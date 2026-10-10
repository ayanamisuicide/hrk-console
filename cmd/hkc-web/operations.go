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
	"sort"
	"strings"
	"sync"
	"time"
)

const maxSchedules = 50

// maintenanceState — Сохраняемый запрет запуска и перезапуска с сообщением администратора.
type maintenanceState struct {
	Enabled   bool      `json:"enabled"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// scheduledAction — Сохранённое действие с временем и этапом pending/running/завершения.
type scheduledAction struct {
	ID          string     `json:"id"`
	Action      string     `json:"action"`
	RunAt       time.Time  `json:"runAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Result      string     `json:"result,omitempty"`
}

// operationData — Режим обслуживания и очередь задач, сохраняемые вместе.
type operationData struct {
	Maintenance maintenanceState  `json:"maintenance"`
	Schedules   []scheduledAction `json:"schedules"`
}

// operationStore — Состояние операций в памяти и на диске с одной блокировкой изменений.
type operationStore struct {
	mu   sync.Mutex
	path string
	data operationData
}

// openOperationStore восстанавливает обслуживание и расписания с диска; незавершённые running-задачи
// возвращает в очередь после сбоя.
func openOperationStore(path string) (*operationStore, error) {
	store := &operationStore{path: path, data: operationData{Schedules: []scheduledAction{}}}
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
	if err := json.Unmarshal(data, &store.data); err != nil {
		return nil, fmt.Errorf("чтение operations.json: %w", err)
	}
	if store.data.Schedules == nil {
		store.data.Schedules = []scheduledAction{}
	}
	// Сервер мог остановиться после сохранения running, но до завершения действия.
	// Возвращаем такие задачи в очередь: действие может выполниться повторно,
	// поэтому это восстановление после сбоя, а не гарантия ровно одного выполнения.
	for index := range store.data.Schedules {
		if store.data.Schedules[index].Status == "running" {
			store.data.Schedules[index].Status = "pending"
		}
	}
	return store, nil
}

// saveLocked сохраняет операции через временный файл; мьютекс хранилища уже должен быть захвачен.
func (s *operationStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// maintenanceState возвращает копию режима обслуживания под блокировкой.
func (s *operationStore) maintenanceState() maintenanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Maintenance
}

// setMaintenance сохраняет режим и ограниченное по длине сообщение; ошибка записи откатывает изменение.
func (s *operationStore) setMaintenance(enabled bool, message string) (maintenanceState, error) {
	message = strings.TrimSpace(message)
	if len(message) > 240 {
		return maintenanceState{}, errors.New("сообщение не должно превышать 240 символов")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.Maintenance
	s.data.Maintenance = maintenanceState{Enabled: enabled, Message: message, UpdatedAt: time.Now().UTC()}
	if err := s.saveLocked(); err != nil {
		s.data.Maintenance = previous
		return maintenanceState{}, err
	}
	return s.data.Maintenance, nil
}

// validScheduledAction разрешает только известные действия над ботом.
func validScheduledAction(action string) bool {
	return action == "start" || action == "stop" || action == "restart"
}

// createSchedule проверяет время, действие и лимит очереди, затем сохраняет новую pending-задачу.
func (s *operationStore) createSchedule(action string, runAt time.Time) (scheduledAction, error) {
	if !validScheduledAction(action) {
		return scheduledAction{}, errors.New("неизвестное действие")
	}
	now := time.Now().UTC()
	runAt = runAt.UTC()
	if runAt.Before(now.Add(5*time.Second)) || runAt.After(now.Add(366*24*time.Hour)) {
		return scheduledAction{}, errors.New("время запуска должно быть в диапазоне от 5 секунд до одного года")
	}
	id, err := randomToken(9)
	if err != nil {
		return scheduledAction{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := 0
	for _, item := range s.data.Schedules {
		if item.Status == "pending" || item.Status == "running" {
			pending++
		}
	}
	if pending >= maxSchedules {
		return scheduledAction{}, errors.New("достигнут лимит 50 активных расписаний")
	}
	item := scheduledAction{ID: id, Action: action, RunAt: runAt, CreatedAt: now, Status: "pending"}
	s.data.Schedules = append(s.data.Schedules, item)
	if err := s.saveLocked(); err != nil {
		s.data.Schedules = s.data.Schedules[:len(s.data.Schedules)-1]
		return scheduledAction{}, err
	}
	return item, nil
}

// listSchedules возвращает копию расписаний в установленном порядке.
func (s *operationStore) listSchedules() []scheduledAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append([]scheduledAction(nil), s.data.Schedules...)
	sort.Slice(items, func(i, j int) bool { return items[i].RunAt.After(items[j].RunAt) })
	return items
}

// deleteSchedule удаляет доступную для отмены задачу и сохраняет очередь.
func (s *operationStore) deleteSchedule(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, item := range s.data.Schedules {
		if item.ID != id || item.Status != "pending" {
			continue
		}
		previous := append([]scheduledAction(nil), s.data.Schedules...)
		s.data.Schedules = append(s.data.Schedules[:index], s.data.Schedules[index+1:]...)
		if err := s.saveLocked(); err != nil {
			s.data.Schedules = previous
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// claimDue под блокировкой отмечает подошедшие задачи running и сохраняет это до выполнения.
func (s *operationStore) claimDue(now time.Time) []scheduledAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	due := make([]scheduledAction, 0)
	claimed := make(map[string]struct{})
	changed := false
	for index := range s.data.Schedules {
		item := &s.data.Schedules[index]
		if item.Status == "pending" && !item.RunAt.After(now) {
			item.Status = "running"
			due = append(due, *item)
			claimed[item.ID] = struct{}{}
			changed = true
		}
	}
	if changed && s.saveLocked() != nil {
		for index := range s.data.Schedules {
			if _, ok := claimed[s.data.Schedules[index].ID]; ok {
				s.data.Schedules[index].Status = "pending"
			}
		}
		return nil
	}
	return due
}

// complete обновляет итог выполнения задачи и ограничивает размер истории.
// Ошибка завершающего сохранения не возвращается вызывающей стороне:
// результат уже изменён в памяти, но при сбое записи может не пережить перезапуск.
func (s *operationStore) complete(id, result string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for index := range s.data.Schedules {
		if s.data.Schedules[index].ID == id {
			s.data.Schedules[index].CompletedAt = &now
			s.data.Schedules[index].Result = result
			if ok {
				s.data.Schedules[index].Status = "completed"
			} else {
				s.data.Schedules[index].Status = "failed"
			}
			break
		}
	}
	if len(s.data.Schedules) > 200 {
		s.data.Schedules = append([]scheduledAction(nil), s.data.Schedules[len(s.data.Schedules)-200:]...)
	}
	_ = s.saveLocked()
}

// runSchedules проверяет очередь раз в секунду и выполняет захваченные задачи; завершается по контексту.
func (s *server) runSchedules(ctx context.Context) {
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
			for _, item := range s.operations.claimDue(now.UTC()) {
				result, status := s.performAction(item.Action)
				s.operations.complete(item.ID, result.Message, status == http.StatusOK)
				if s.audit != nil {
					_ = s.audit.add(auditEvent{Time: time.Now().UTC(), Actor: "scheduler", Action: "bot." + item.Action, Detail: result.Message, IP: "local"})
				}
				if status == http.StatusOK && s.notifier != nil {
					s.notifier.observe(s.bot.PID() != 0)
				}
			}
		}
	}
}

// requireOperations проверяет административный доступ и наличие хранилища операций.
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

// getMaintenance возвращает администратору состояние обслуживания.
func (s *server) getMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.operations.maintenanceState())
}

// setMaintenance проверяет административный запрос, сохраняет обслуживание и записывает событие аудита.
func (s *server) setMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	var input struct {
		Enabled bool   `json:"enabled"`
		Message string `json:"message"`
		StopBot bool   `json:"stopBot"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	state, err := s.operations.setMaintenance(input.Enabled, input.Message)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	if input.Enabled && input.StopBot {
		_, _ = s.performActionUnchecked("stop")
	}
	s.record(r, "admin", "maintenance.update", fmt.Sprintf("enabled=%t stopBot=%t", input.Enabled, input.StopBot))
	writeJSON(w, http.StatusOK, state)
}

// listSchedules возвращает администратору текущую очередь и историю задач.
func (s *server) listSchedules(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": s.operations.listSchedules()})
}

// createSchedule разбирает административный запрос и сохраняет проверенное отложенное действие.
func (s *server) createSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	var input struct {
		Action string    `json:"action"`
		RunAt  time.Time `json:"runAt"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.RunAt.IsZero() {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректное действие или время"})
		return
	}
	item, err := s.operations.createSchedule(input.Action, input.RunAt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	s.record(r, "admin", "schedule.create", item.Action+" @ "+item.RunAt.Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, item)
}

// deleteSchedule отменяет выбранное расписание после проверки административного доступа.
func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	id := r.PathValue("id")
	ok, err := s.operations.deleteSchedule(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось удалить расписание"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "активное расписание не найдено"})
		return
	}
	s.record(r, "admin", "schedule.delete", id)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "расписание удалено"})
}
