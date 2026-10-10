package main

import (
	"encoding/json"
	"fmt"
	"heroku-console/logfeed"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// logs возвращает хвост журнала с ограничением числа строк из параметра limit.
func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 5000 {
			limit = parsed
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": logfeed.TailLines(s.bot.LogFile, limit)})
}

// events отправляет новые завершённые строки через SSE и поддерживает соединение служебными сообщениями.
// Отмена запроса останавливает читатель файла.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "потоковая передача недоступна", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	follower, err := logfeed.Follow(s.bot.LogFile, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Закрытие вкладки отменяет запрос; defer освобождает файл и горутину.
	defer follower.Stop()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, open := <-follower.Lines:
			if !open {
				return
			}
			payload, _ := json.Marshal(line)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-ticker.C:
			// Комментарий SSE поддерживает соединение, но не добавляет строку в журнал.
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// tailText соединяет последние строки файла в текст для диагностики.
func tailText(path string, maxLines int) string {
	return strings.Join(logfeed.TailLines(path, maxLines), "\n")
}
