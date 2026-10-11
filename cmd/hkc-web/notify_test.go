package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"heroku-console/botproc"
)

// recordedAlerts собирает события, которые диспетчер записывает в ленту до фильтров.
func recordedAlerts() (*alertDispatcher, func() []alertEvent) {
	var mu sync.Mutex
	var events []alertEvent
	dispatcher := &alertDispatcher{record: func(e alertEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}}
	return dispatcher, func() []alertEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]alertEvent(nil), events...)
	}
}

// Первое наблюдение — только база; сообщения приходят при переходах бота между состояниями.
func TestStateNotifierReportsOnlyChanges(t *testing.T) {
	dispatcher, events := recordedAlerts()
	notifier := newStateNotifier(dispatcher)

	notifier.observe(true)
	notifier.observe(true)
	if got := events(); len(got) != 0 {
		t.Fatalf("первое наблюдение и повтор не должны давать событий: %+v", got)
	}

	notifier.observe(false)
	notifier.observe(false)
	got := events()
	if len(got) != 1 || got[0].Kind != "bot.stopped" || got[0].Severity != "critical" {
		t.Fatalf("остановка: %+v", got)
	}

	notifier.observe(true)
	got = events()
	if len(got) != 2 || got[1].Kind != "bot.started" || got[1].Severity != "ok" {
		t.Fatalf("запуск: %+v", got)
	}
}

// Если бот остановлен с самого начала, это не «изменение»: тревоги при старте панели нет.
func TestStateNotifierBaselineStopped(t *testing.T) {
	dispatcher, events := recordedAlerts()
	notifier := newStateNotifier(dispatcher)
	notifier.observe(false)
	if got := events(); len(got) != 0 {
		t.Fatalf("бот остановлен до запуска панели: %+v", got)
	}
	notifier.observe(true)
	if got := events(); len(got) != 1 || got[0].Kind != "bot.started" {
		t.Fatalf("запуск после остановки: %+v", got)
	}
}

func TestStateNotifierNilSafe(t *testing.T) {
	if newStateNotifier(nil) != nil {
		t.Fatal("без диспетчера наблюдатель не нужен")
	}
	var notifier *stateNotifier
	notifier.observe(true) // не должно падать
	watchBotState(context.Background(), nil, botproc.New(t.TempDir()))
}

// watchBotState возвращается, когда контекст отменён, и сразу задаёт базу.
func TestWatchBotStateStopsOnCancel(t *testing.T) {
	dispatcher, events := recordedAlerts()
	notifier := newStateNotifier(dispatcher)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchBotState(ctx, notifier, botproc.New(t.TempDir()))
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for {
		notifier.mu.Lock()
		ready := notifier.initialized
		notifier.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("база состояния не задана")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchBotState не завершился после отмены")
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("лишние события: %+v", got)
	}
}
