package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var backupNamePattern = regexp.MustCompile(`^backup-\d{8}T\d{6}Z-[a-f0-9]{8}\.json$`)

type backupView struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
}

func (s *authStore) backupDir() string { return filepath.Join(filepath.Dir(s.path), "backups") }

func (s *authStore) backupLocked() (backupView, error) {
	if err := os.MkdirAll(s.backupDir(), 0o700); err != nil {
		return backupView{}, err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return backupView{}, err
	}
	random, err := randomToken(6)
	if err != nil {
		return backupView{}, err
	}
	// Hexadecimal keeps the backup filename simple and validates well at restore.
	name := fmt.Sprintf("backup-%s-%x.json", time.Now().UTC().Format("20060102T150405Z"), []byte(random)[:4])
	path := filepath.Join(s.backupDir(), name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backupView{}, err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return backupView{}, err
	}
	if err = f.Close(); err != nil {
		return backupView{}, err
	}
	return backupView{Name: name, CreatedAt: time.Now().UTC(), Size: int64(len(data))}, nil
}

func (s *authStore) backup() (backupView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backupLocked()
}

func (s *authStore) backups() ([]backupView, error) {
	entries, err := os.ReadDir(s.backupDir())
	if os.IsNotExist(err) {
		return []backupView{}, nil
	}
	if err != nil {
		return nil, err
	}
	views := make([]backupView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !backupNamePattern.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		views = append(views, backupView{Name: entry.Name(), CreatedAt: info.ModTime().UTC(), Size: info.Size()})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt.After(views[j].CreatedAt) })
	return views, nil
}

func (s *authStore) restore(name string) error {
	if !backupNamePattern.MatchString(name) {
		return errors.New("некорректное имя копии")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.backupDir(), name))
	if err != nil {
		return err
	}
	var restored authData
	if err := json.Unmarshal(data, &restored); err != nil {
		return err
	}
	if restored.Users == nil || restored.Invites == nil {
		return errors.New("повреждённая резервная копия")
	}
	if _, err := s.backupLocked(); err != nil {
		return fmt.Errorf("не удалось сохранить текущую базу: %w", err)
	}
	previous := s.data
	s.data = restored
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return err
	}
	return nil
}

func (s *server) listBackups(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	backups, err := s.auth.backups()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось прочитать копии"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

func (s *server) createBackup(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	backup, err := s.auth.backup()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось создать копию"})
		return
	}
	s.record(r, "admin", "backup.create", backup.Name)
	writeJSON(w, http.StatusCreated, backup)
}

func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	name := r.PathValue("name")
	if err := s.auth.restore(name); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	s.sessions.clear()
	s.record(r, "admin", "backup.restore", name)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "база доступа восстановлена; пользовательские сессии завершены"})
}
