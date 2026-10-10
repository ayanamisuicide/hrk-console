package main

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestSecureRequestOnlyTrustsConfiguredLocalProxy проверяет, что HTTPS-заголовку доверяют только явно
// настроенного локального прокси.
func TestSecureRequestOnlyTrustsConfiguredLocalProxy(t *testing.T) {
	request := httptest.NewRequest("GET", "http://console.example/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.RemoteAddr = "127.0.0.1:1234"
	if secureRequest(request) {
		t.Fatal("forwarded protocol trusted without proxy opt-in")
	}
	t.Setenv("HKC_TRUST_PROXY", "1")
	if !secureRequest(request) {
		t.Fatal("configured local proxy was not trusted")
	}
	request.RemoteAddr = "192.0.2.5:1234"
	if secureRequest(request) {
		t.Fatal("non-local forwarded protocol was trusted")
	}
}

// TestInviteRegistrationAndLogin проверяет регистрацию, расходование инвайта и пароль.
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

// TestExpiredInvite проверяет отказ регистрации по истёкшему приглашению.
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
