package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type apiTokenRecord struct {
	Hash      string     `json:"hash"`
	Label     string     `json:"label"`
	Scope     string     `json:"scope"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

type apiTokenView struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Scope     string     `json:"scope"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

func (s *authStore) createAPIToken(label, scope string) (apiTokenView, string, error) {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 48 {
		return apiTokenView{}, "", errors.New("название токена: 1–48 символов")
	}
	if scope != "read" && scope != "control" {
		return apiTokenView{}, "", errors.New("неизвестное право токена")
	}
	id, err := randomToken(9)
	if err != nil {
		return apiTokenView{}, "", err
	}
	secret, err := randomToken(32)
	if err != nil {
		return apiTokenView{}, "", err
	}
	raw := "hkc." + id + "." + secret
	digest := sha256.Sum256([]byte(raw))
	now := time.Now().UTC()
	record := apiTokenRecord{Hash: hex.EncodeToString(digest[:]), Label: label, Scope: scope, CreatedAt: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Tokens == nil {
		s.data.Tokens = make(map[string]apiTokenRecord)
	}
	if len(s.data.Tokens) >= 32 {
		return apiTokenView{}, "", errors.New("достигнут лимит 32 токенов")
	}
	s.data.Tokens[id] = record
	if err := s.saveLocked(); err != nil {
		delete(s.data.Tokens, id)
		return apiTokenView{}, "", err
	}
	return apiTokenView{ID: id, Label: label, Scope: scope, CreatedAt: now}, raw, nil
}

func (s *authStore) listAPITokens() []apiTokenView {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]apiTokenView, 0, len(s.data.Tokens))
	for id, token := range s.data.Tokens {
		views = append(views, apiTokenView{ID: id, Label: token.Label, Scope: token.Scope, CreatedAt: token.CreatedAt, LastUsed: token.LastUsed})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt.After(views[j].CreatedAt) })
	return views
}

func (s *authStore) useAPIToken(raw, required string) (string, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "hkc" {
		return "", false
	}
	digest := sha256.Sum256([]byte(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.data.Tokens[parts[1]]
	if !ok {
		return "", false
	}
	stored, err := hex.DecodeString(record.Hash)
	if err != nil || subtle.ConstantTimeCompare(digest[:], stored) != 1 {
		return "", false
	}
	if required == "control" && record.Scope != "control" {
		return "", false
	}
	if record.LastUsed == nil || time.Since(*record.LastUsed) > time.Minute {
		now := time.Now().UTC()
		record.LastUsed = &now
		s.data.Tokens[parts[1]] = record
		_ = s.saveLocked()
	}
	return record.Label, true
}

func (s *authStore) revokeAPIToken(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.data.Tokens[id]
	if !ok {
		return false, nil
	}
	delete(s.data.Tokens, id)
	if err := s.saveLocked(); err != nil {
		s.data.Tokens[id] = record
		return false, err
	}
	return true, nil
}

type apiActorKey struct{}

func (s *server) apiAuthorize(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "требуется API-токен"})
			return
		}
		raw := strings.TrimPrefix(header, "Bearer ")
		label, ok := s.auth.useAPIToken(raw, scope)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "API-токен недействителен или не имеет нужного права"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), apiActorKey{}, "api:"+label)))
	}
}

func (s *server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": s.auth.listAPITokens()})
}

func (s *server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	var input struct {
		Label string `json:"label"`
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	view, raw, err := s.auth.createAPIToken(input.Label, input.Scope)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	s.record(r, "admin", "token.create", view.Label+" ("+view.Scope+")")
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": view})
}

func (s *server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	ok, err := s.auth.revokeAPIToken(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось отозвать токен"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "токен не найден"})
		return
	}
	s.record(r, "admin", "token.revoke", r.PathValue("id"))
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "API-токен отозван"})
}
