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
	// explain объясняет остановку: кто остановил бота или почему он упал (nil — без объяснения).
	explain func(now time.Time) stopReport
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
		event := alertEvent{Kind: "bot.stopped", Severity: "critical", Title: "Бот остановлен", Message: "Процесс Heroku не найден."}
		if n.explain != nil {
			report := n.explain(time.Now())
			event.Title, event.Severity, event.Message = report.Title, report.Severity, report.Message
		}
		n.alerts.notify(event)
	}
}

// newStateNotifier создаёт наблюдатель; события попадают в ленту, даже если каналов доставки нет.
func newStateNotifier(alerts *alertDispatcher) *stateNotifier {
	if alerts == nil {
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
