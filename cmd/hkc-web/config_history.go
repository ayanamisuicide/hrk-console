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

const configHistoryLimit = 50

var configHistoryName = regexp.MustCompile(`^config-\d{8}T\d{6}\.\d{9}Z-[a-zA-Z0-9_-]+\.json$`)

type configHistoryView struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
}

func (s *server) configHistoryDir() string {
	return filepath.Join(s.bot.HerokuDir, ".hkc-config-history")
}

func (s *server) snapshotConfigLocked() error {
	data, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > 1024*1024 {
		return errors.New("config.json слишком большой для истории")
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) != nil || parsed == nil {
		return errors.New("текущий config.json повреждён; история не изменена")
	}
	if err := os.MkdirAll(s.configHistoryDir(), 0o700); err != nil {
		return err
	}
	random, err := randomToken(6)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("config-%s-%s.json", time.Now().UTC().Format("20060102T150405.000000000Z"), random)
	path := filepath.Join(s.configHistoryDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	entries, err := s.configHistoryLocked()
	if err != nil {
		return err
	}
	if len(entries) > configHistoryLimit {
		for _, entry := range entries[configHistoryLimit:] {
			_ = os.Remove(filepath.Join(s.configHistoryDir(), entry.Name))
		}
	}
	return nil
}

func (s *server) configHistoryLocked() ([]configHistoryView, error) {
	entries, err := os.ReadDir(s.configHistoryDir())
	if errors.Is(err, os.ErrNotExist) {
		return []configHistoryView{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]configHistoryView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !configHistoryName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			result = append(result, configHistoryView{Name: entry.Name(), CreatedAt: info.ModTime().UTC(), Size: info.Size()})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *server) listConfigHistory(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	s.configMu.Lock()
	history, err := s.configHistoryLocked()
	s.configMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось прочитать историю конфигурации"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": history})
}

func (s *server) restoreConfigHistory(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	name := r.PathValue("name")
	if !configHistoryName.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректное имя версии"})
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	path := filepath.Join(s.configHistoryDir(), name)
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		err = errors.New("config history entry is not a regular file")
	}
	var data []byte
	if err == nil {
		data, err = os.ReadFile(path)
	}
	var values map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(data, &values)
	}
	if err != nil || values == nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "версия конфигурации повреждена или недоступна"})
		return
	}
	if err = s.snapshotConfigLocked(); err == nil {
		err = s.writeConfigLocked(values)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	s.record(r, "admin", "config.restore", name)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "конфигурация восстановлена; перезапустите бот для применения"})
}

func (s *server) diffConfigHistory(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	name := r.PathValue("name")
	if !configHistoryName.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректное имя версии"})
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	path := filepath.Join(s.configHistoryDir(), name)
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		err = errors.New("config history entry is not a regular file")
	}
	var historical map[string]json.RawMessage
	if err == nil {
		data, readErr := os.ReadFile(path)
		err = readErr
		if err == nil {
			err = json.Unmarshal(data, &historical)
		}
	}
	current, currentErr := s.readConfigLocked()
	if err != nil || currentErr != nil || historical == nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "версия конфигурации повреждена или недоступна"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "changes": configChanges(current, historical)})
}
