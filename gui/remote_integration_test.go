package main

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"heroku-console/logfeed"
	"heroku-console/state"
)

// Проверяет именно связывание в app.go (startRemote переставляет точки
// подмены, loadRemoteHistory/followRemoteLog кормят a.visible) против
// настоящей машины — то, что не покрыто тестами пакета remotebot самого
// по себе (там проверяется протокол, здесь — что App его правильно
// использует). Не часть обычного `go test ./...` по той же причине, что и
// remotebot.TestRemoteIntegration: в CI нет доступа к чужой домашней сети.
//
//	HKC_TEST_REMOTE_HOST=192.168.31.128 HKC_TEST_REMOTE_USER=ayanami \
//	HKC_TEST_REMOTE_KEY=C:\Users\...\id_ed25519_hkc HKC_TEST_REMOTE_DIR=Heroku \
//	go test ./gui/... -run TestAppStartRemoteIntegration -v
func TestAppStartRemoteIntegration(t *testing.T) {
	host := os.Getenv("HKC_TEST_REMOTE_HOST")
	if host == "" {
		t.Skip("HKC_TEST_REMOTE_HOST не задан — пропускаю (это не автоматический CI-тест)")
	}

	a := &App{}
	a.ctx = context.Background()
	a.parser = logfeed.NewParser()
	a.ui = state.State{Remote: state.Remote{
		Host:    host,
		User:    os.Getenv("HKC_TEST_REMOTE_USER"),
		KeyPath: os.Getenv("HKC_TEST_REMOTE_KEY"),
		Dir:     os.Getenv("HKC_TEST_REMOTE_DIR"),
	}}

	var mu sync.Mutex
	var notices []string
	a.emit = func(event string, data ...interface{}) {
		if event != "notice" || len(data) == 0 {
			return
		}
		s, _ := data[0].(string)
		mu.Lock()
		notices = append(notices, s)
		mu.Unlock()
	}

	a.startRemote()

	mu.Lock()
	gotNotices := append([]string(nil), notices...)
	mu.Unlock()
	if a.remote == nil {
		t.Fatalf("startRemote не подключился, notice: %v", gotNotices)
	}
	t.Cleanup(func() { a.remote.Close() })

	pid := a.pid()
	t.Logf("a.pid() = %d", pid)
	if pid == 0 {
		t.Skip("бот на удалённой машине не запущен — дальше проверять нечего")
	}

	if !a.aliveAt(pid) {
		t.Errorf("a.aliveAt(%d) = false для только что найденного pid", pid)
	}
	if a.aliveAt(pid + 999999) {
		t.Error("a.aliveAt на заведомо несуществующем pid вернул true")
	}

	if got := a.uptime(pid); got == "—" || got == "" {
		t.Errorf("a.uptime(%d) = %q, ожидалось что-то содержательное для живого процесса", pid, got)
	}
	t.Logf("a.uptime(%d) = %q", pid, a.uptime(pid))
	t.Logf("a.botVersion() = %q", a.botVersion())

	// loadRemoteHistory + followRemoteLog кормят a.ring/a.visible через тот
	// же rebuildLocked/feedLine, что и локальный режим — проверяем, что
	// после них в буфере реально что-то есть, а не что парсер сам по себе
	// работает (это уже проверено в logfeed).
	a.loadRemoteHistory()
	a.mu.Lock()
	ringLen, visibleLen := len(a.ring), len(a.visible)
	a.mu.Unlock()
	t.Logf("после loadRemoteHistory: ring=%d visible=%d", ringLen, visibleLen)
	if ringLen == 0 {
		t.Error("loadRemoteHistory не наполнил a.ring на непустом удалённом логе")
	}

	go a.followRemoteLog()
	time.Sleep(500 * time.Millisecond) // дать followRemoteLog поднять a.remoteFollower
	if a.remoteFollower == nil {
		t.Error("followRemoteLog не установил a.remoteFollower")
	} else if ok, reason, _ := a.remoteFollower.Alive(); !ok {
		t.Errorf("remoteFollower.Alive() = false сразу после старта, reason=%q", reason)
	}
}
