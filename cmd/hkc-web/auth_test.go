package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInviteRegistrationAndLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	store, err := openAuthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := store.createInvite(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.register(invite, "alice", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if !store.authenticate("alice", "correct-horse-battery") {
		t.Fatal("правильный пароль не принят")
	}
	if store.authenticate("alice", "wrong-password") {
		t.Fatal("неправильный пароль принят")
	}
	if err := store.register(invite, "bob", "another-long-password"); err == nil {
		t.Fatal("одноразовый инвайт принят повторно")
	}

	reloaded, err := openAuthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.authenticate("alice", "correct-horse-battery") {
		t.Fatal("пользователь не сохранился на диске")
	}
}

func TestExpiredInvite(t *testing.T) {
	store, err := openAuthStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	invite, _, err := store.createInvite(-time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.register(invite, "alice", "correct-horse-battery"); err == nil {
		t.Fatal("истёкший инвайт принят")
	}
}
