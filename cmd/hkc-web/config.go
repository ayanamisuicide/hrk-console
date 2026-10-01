package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var herokuConfigKeys = []string{"api_id", "api_hash", "redis_uri", "db_uri", "app_name"}
var apiHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
var appNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validHerokuKey(key string) bool {
	for _, allowed := range herokuConfigKeys {
		if key == allowed {
			return true
		}
	}
	return false
}

func (s *server) configPath() string { return filepath.Join(s.bot.HerokuDir, "config.json") }

func (s *server) readConfigLocked() (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, errors.New("config.json слишком большой")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("повреждённый config.json: %w", err)
	}
	if values == nil {
		return nil, errors.New("config.json должен содержать объект")
	}
	return values, nil
}

func (s *server) writeConfigLocked(values map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(values, "", "    ")
	if err != nil {
		return err
	}
	path := s.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".hkc-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func encodeConfigValue(key, value string) (json.RawMessage, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 {
		return nil, errors.New("значение пустое или слишком длинное")
	}
	switch key {
	case "api_id":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 {
			return nil, errors.New("API ID должен быть положительным числом")
		}
		return json.Marshal(n)
	case "api_hash":
		if !apiHashPattern.MatchString(value) {
			return nil, errors.New("API hash должен содержать 32 шестнадцатеричных символа")
		}
	case "app_name":
		if !appNamePattern.MatchString(value) {
			return nil, errors.New("имя: 1–64 символа, буквы, цифры, _ или -")
		}
	case "redis_uri", "db_uri":
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || (u.Host == "" && !strings.HasPrefix(value, "sqlite:")) {
			return nil, errors.New("некорректный адрес базы данных")
		}
	default:
		return nil, errors.New("неизвестный параметр")
	}
	return json.Marshal(value)
}

func (s *server) adminConfig(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	s.configMu.Lock()
	values, err := s.readConfigLocked()
	s.configMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	configured := make(map[string]bool, len(herokuConfigKeys))
	for _, key := range herokuConfigKeys {
		_, configured[key] = values[key]
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": configured, "path": s.configPath()})
}

func (s *server) updateConfig(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	var input map[string]string
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&input); err != nil || len(input) == 0 {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректные параметры"})
		return
	}
	encoded := make(map[string]json.RawMessage, len(input))
	keys := make([]string, 0, len(input))
	for key, value := range input {
		if !validHerokuKey(key) {
			writeJSON(w, http.StatusBadRequest, actionResponse{Message: "неизвестный параметр: " + key})
			return
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		item, err := encodeConfigValue(key, value)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, actionResponse{Message: key + ": " + err.Error()})
			return
		}
		encoded[key] = item
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "укажите хотя бы одно новое значение"})
		return
	}
	s.configMu.Lock()
	values, err := s.readConfigLocked()
	if err == nil {
		for key, value := range encoded {
			values[key] = value
		}
		err = s.writeConfigLocked(values)
	}
	s.configMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	sort.Strings(keys)
	s.record(r, "admin", "config.update", strings.Join(keys, ", "))
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "настройки сохранены; для применения перезапустите бота"})
}

func (s *server) deleteConfigKey(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	key := r.PathValue("key")
	if !validHerokuKey(key) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "неизвестный параметр"})
		return
	}
	s.configMu.Lock()
	values, err := s.readConfigLocked()
	if err == nil {
		delete(values, key)
		err = s.writeConfigLocked(values)
	}
	s.configMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	s.record(r, "admin", "config.delete", key)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "параметр удалён; для применения перезапустите бота"})
}
