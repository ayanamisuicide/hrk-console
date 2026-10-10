package main

import (
	"context"
	"sync"
	"time"

	"heroku-console/botproc"
)

// stateNotifier — Последнее наблюдение состояния бота. Первое наблюдение устанавливает базу, но не
// отправляет уведомление; изменения передаются диспетчеру уведомлений.
type stateNotifier struct {
	alerts      *alertDispatcher
	mu          sync.Mutex
	last        bool
	initialized bool
}

// observe запоминает состояние под мьютексом и отправляет уведомление только при изменении после первого
// наблюдения.
func (n *stateNotifier) observe(running bool) {
	if n == nil {
		return
	}
	n.mu.Lock()
	changed := n.initialized && n.last != running
	n.last, n.initialized = running, true
	n.mu.Unlock()
	if !changed {
		return
	}
	if running {
		n.alerts.notify(alertEvent{Kind: "bot.started", Severity: "ok", Title: "Бот запущен", Message: "Процесс Heroku работает."})
	} else {
		n.alerts.notify(alertEvent{Kind: "bot.stopped", Severity: "critical", Title: "Бот остановлен", Message: "Процесс Heroku не найден."})
	}
}

// newStateNotifier создаёт наблюдатель только при наличии хотя бы одного канала доставки.
func newStateNotifier(alerts *alertDispatcher) *stateNotifier {
	if !alerts.active() {
		return nil
	}
	return &stateNotifier{alerts: alerts}
}

// watchBotState проверяет состояние каждые 15 секунд до отмены контекста.
func watchBotState(ctx context.Context, notifier *stateNotifier, bot *botproc.Manager) {
	if notifier == nil {
		return
	}
	notifier.observe(bot.PID() != 0)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notifier.observe(bot.PID() != 0)
		}
	}
}
