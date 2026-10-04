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

type maintenanceState struct {
	Enabled   bool      `json:"enabled"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type scheduledAction struct {
	ID          string     `json:"id"`
	Action      string     `json:"action"`
	RunAt       time.Time  `json:"runAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Result      string     `json:"result,omitempty"`
}

type operationData struct {
	Maintenance maintenanceState  `json:"maintenance"`
	Schedules   []scheduledAction `json:"schedules"`
}

type operationStore struct {
	mu   sync.Mutex
	path string
	data operationData
}

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
	// A process can stop after persisting the claim but before completing it.
	// Requeue such entries so a restart does not lose the requested action.
	for index := range store.data.Schedules {
		if store.data.Schedules[index].Status == "running" {
			store.data.Schedules[index].Status = "pending"
		}
	}
	return store, nil
}

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

func (s *operationStore) maintenanceState() maintenanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Maintenance
}

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

func validScheduledAction(action string) bool {
	return action == "start" || action == "stop" || action == "restart"
}

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

func (s *operationStore) listSchedules() []scheduledAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append([]scheduledAction(nil), s.data.Schedules...)
	sort.Slice(items, func(i, j int) bool { return items[i].RunAt.After(items[j].RunAt) })
	return items
}

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

func (s *server) getMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.operations.maintenanceState())
}

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

func (s *server) listSchedules(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": s.operations.listSchedules()})
}

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
