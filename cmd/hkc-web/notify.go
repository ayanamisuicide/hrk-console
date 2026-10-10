package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"heroku-console/botproc"
)

// stateNotifier — Последнее наблюдение и HTTPS-клиент. Первое наблюдение устанавливает базу, но не
// отправляет уведомление.
type stateNotifier struct {
	endpoint    string
	client      *http.Client
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
	if changed {
		go n.send(context.Background(), running)
	}
}

// newStateNotifier проверяет HTTPS-адрес без учётных данных и создаёт клиент с коротким таймаутом.
func newStateNotifier(raw string) *stateNotifier {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		log.Print("HKC_WEBHOOK_URL должен быть HTTPS URL без логина в адресе; уведомления отключены")
		return nil
	}
	return &stateNotifier{endpoint: raw, client: &http.Client{Timeout: 5 * time.Second}}
}

// send отправляет JSON-уведомление о состоянии; ошибки уведомления не останавливают управление ботом.
func (n *stateNotifier) send(ctx context.Context, running bool) {
	if n == nil {
		return
	}
	status := "stopped"
	if running {
		status = "running"
	}
	payload, _ := json.Marshal(map[string]any{"service": "Heroku bot", "status": status, "time": time.Now().UTC()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := n.client.Do(req)
	if err != nil {
		log.Printf("webhook: %v", err)
		return
	}
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		log.Printf("webhook: HTTP %d", res.StatusCode)
	}
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

// configuredWebhook создаёт необязательный отправитель из HKC_WEBHOOK_URL.
func configuredWebhook() *stateNotifier { return newStateNotifier(os.Getenv("HKC_WEBHOOK_URL")) }
