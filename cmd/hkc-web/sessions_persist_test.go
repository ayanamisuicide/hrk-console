package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Сессия переживает перезапуск панели; на диске лежит не токен, а его SHA-256.
func TestSessionsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	first := newSessionStore()
	first.persistTo(path)
	token, _, err := first.create("alice")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("в файле сессий лежит токен, годный для входа")
	}

	second := newSessionStore() // «перезапуск»
	second.persistTo(path)
	if user, ok := second.get(token); !ok || user != "alice" {
		t.Fatalf("после перезапуска сессия потеряна: %q %v", user, ok)
	}
	if _, ok := second.get(token + "x"); ok {
		t.Fatal("чужой токен принят")
	}
}

// Выход, удаление пользователя и очистка тоже сохраняются: отозванная сессия не воскресает.
func TestRevokedSessionsStayRevoked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := newSessionStore()
	store.persistTo(path)
	out, _, _ := store.create("alice")
	removed, _, _ := store.create("bob")
	cleared, _, _ := store.create("carol")

	store.delete(out)
	store.deleteUser("bob")
	reloaded := newSessionStore()
	reloaded.persistTo(path)
	for name, token := range map[string]string{"выход": out, "удаление пользователя": removed} {
		if _, ok := reloaded.get(token); ok {
			t.Errorf("после перезапуска ожила сессия (%s)", name)
		}
	}
	if _, ok := reloaded.get(cleared); !ok {
		t.Fatal("посторонняя сессия потеряна")
	}

	reloaded.clear()
	again := newSessionStore()
	again.persistTo(path)
	if _, ok := again.get(cleared); ok {
		t.Fatal("после очистки сессия ожила")
	}
}

// Истёкшие сессии не загружаются, а мусорный файл не мешает запуску.
func TestSessionsIgnoreExpiredAndCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := newSessionStore()
	store.persistTo(path)
	token, _, _ := store.create("alice")
	store.mu.Lock()
	for key, entry := range store.sessions {
		entry.ExpiresAt = time.Now().Add(-time.Minute)
		store.sessions[key] = entry
	}
	store.saveLocked()
	store.mu.Unlock()
	reloaded := newSessionStore()
	reloaded.persistTo(path)
	if _, ok := reloaded.get(token); ok {
		t.Fatal("истёкшая сессия загружена")
	}

	if err := os.WriteFile(path, []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := newSessionStore()
	broken.persistTo(path) // не должно падать
	if _, _, err := broken.create("bob"); err != nil {
		t.Fatalf("после повреждённого файла сессии не создаются: %v", err)
	}
}
