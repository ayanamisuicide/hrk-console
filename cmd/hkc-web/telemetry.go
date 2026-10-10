package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"heroku-console/logfeed"
)

// auditEvent — Кто, когда и с какого адреса выполнил действие; Detail не должен содержать секреты.
type auditEvent struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

// auditStore — JSONL-журнал действий; мьютекс защищает дописывание и сокращение истории.
type auditStore struct {
	mu   sync.Mutex
	path string
}

const auditRetention = 1000

// newAuditStore создаёт файловое хранилище административного аудита.
func newAuditStore(path string) *auditStore { return &auditStore{path: path} }

// add добавляет событие в JSONL под блокировкой и при необходимости оставляет последние записи.
func (a *auditStore) add(event auditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(event); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	events, count, err := a.readRecent(auditRetention)
	if err != nil || count <= auditRetention {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".audit-*.jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	for i := len(events) - 1; i >= 0; i-- {
		if err := encoder.Encode(events[i]); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.path)
}

// recent возвращает последние события аудита под блокировкой.
func (a *auditStore) recent(limit int) ([]auditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	events, _, err := a.readRecent(limit)
	return events, err
}

// readRecent читает ограниченный хвост валидных событий и считает строки для очистки истории. Вызывается
// под блокировкой auditStore.
func (a *auditStore) readRecent(limit int) ([]auditEvent, int, error) {
	f, err := os.Open(a.path)
	if os.IsNotExist(err) {
		return []auditEvent{}, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	result := make([]auditEvent, 0, limit)
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		count++
		var event auditEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if len(result) == limit {
			copy(result, result[1:])
			result[len(result)-1] = event
		} else {
			result = append(result, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, count, nil
}

// record добавляет автора, действие и адрес запроса в аудит; ошибка записи выводится в журнал сервера.
func (s *server) record(r *http.Request, actor, action, detail string) {
	if s.audit == nil {
		return
	}
	host := r.RemoteAddr
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	if err := s.audit.add(auditEvent{Time: time.Now().UTC(), Actor: actor, Action: action, Detail: detail, IP: host}); err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
	}
}

// adminAudit выдаёт администратору последние 100 событий.
func (s *server) adminAudit(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	if s.audit == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []auditEvent{}})
		return
	}
	events, err := s.audit.recent(100)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось прочитать журнал действий"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// metricPoint — Короткий замер резидентной памяти бота; PID используется внутри и не передаётся в JSON.
type metricPoint struct {
	Time time.Time `json:"time"`
	RSS  uint64    `json:"rssBytes"`
	PID  int       `json:"-"`
}

// metricStore — Не более двух минут секундных замеров одного PID под блокировкой.
type metricStore struct {
	mu     sync.Mutex
	points []metricPoint
	pid    int
}

// newMetricStore создаёт короткую историю памяти процесса для совместимого API.
func newMetricStore() *metricStore { return &metricStore{} }

// append накапливает не более 120 секундных замеров; смена PID очищает старую историю процесса.
func (m *metricStore) append(point metricPoint) []metricPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	if point.PID != m.pid {
		m.points = nil
		m.pid = point.PID
	}
	if len(m.points) == 0 || point.Time.Sub(m.points[len(m.points)-1].Time) >= time.Second {
		m.points = append(m.points, point)
		if len(m.points) > 120 {
			m.points = m.points[len(m.points)-120:]
		}
	}
	return append([]metricPoint{}, m.points...)
}

// processRSS читает резидентную память процесса Linux из VmRSS; отсутствие данных даёт ноль.
func processRSS(pid int) uint64 {
	if pid == 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				n, _ := strconv.ParseUint(parts[1], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
}

// sampleMetrics получает PID и память бота и добавляет замер в историю, если она подключена.
func (s *server) sampleMetrics() (int, uint64, []metricPoint) {
	pid := s.bot.PID()
	rss := processRSS(pid)
	points := []metricPoint{}
	if s.metrics != nil {
		points = s.metrics.append(metricPoint{Time: time.Now().UTC(), RSS: rss, PID: pid})
	}
	return pid, rss, points
}

// liveMetrics выдаёт совместимый API короткой истории памяти бота.
func (s *server) liveMetrics(w http.ResponseWriter, _ *http.Request) {
	pid, rss, points := s.sampleMetrics()
	writeJSON(w, http.StatusOK, map[string]any{
		"running": pid != 0, "pid": pid, "rssBytes": rss, "points": points,
	})
}

// insights собирает совместимую сводку процесса и количества уровней журнала.
func (s *server) insights(w http.ResponseWriter, _ *http.Request) {
	pid, rss, points := s.sampleMetrics()
	lines := logfeed.TailLines(s.bot.LogFile, 1000)
	counts := map[string]int{"info": 0, "warning": 0, "error": 0}
	for _, line := range lines {
		switch {
		case strings.Contains(line, "[ERROR]") || strings.Contains(line, "[CRITICAL]"):
			counts["error"]++
		case strings.Contains(line, "[WARNING]"):
			counts["warning"]++
		case strings.Contains(line, "[INFO]"):
			counts["info"]++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": pid != 0, "pid": pid, "rssBytes": rss, "points": points,
		"logCounts": counts, "sampledLines": len(lines), "logReady": fileExists(s.bot.LogFile),
	})
}

// fileExists проверяет доступность пути через os.Stat.
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// diagnosticCheck — Название, результат и пояснение фиксированной проверки установки.
type diagnosticCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// diagnostics возвращает простые проверки каталога, окружения Python и журнала.
func (s *server) diagnostics(w http.ResponseWriter, _ *http.Request) {
	logExists := fileExists(s.bot.LogFile)
	logDetail := "Появится после первого запуска бота"
	if logExists {
		logDetail = s.bot.LogFile
	}
	checks := []diagnosticCheck{
		{Name: "Каталог Heroku", OK: fileExists(s.bot.HerokuDir), Detail: s.bot.HerokuDir},
		{Name: "Виртуальное окружение", OK: s.bot.VirtualEnv() != "", Detail: "Поддерживаются .venv и venv"},
		{Name: "Файл журнала", OK: logExists, Detail: logDetail},
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

// publicStatus выдаёт минимальный статус без авторизации только при HKC_PUBLIC_STATUS=1.
func (s *server) publicStatus(w http.ResponseWriter, _ *http.Request) {
	if os.Getenv("HKC_PUBLIC_STATUS") != "1" {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "публичный статус выключен"})
		return
	}
	pid := s.bot.PID()
	writeJSON(w, http.StatusOK, map[string]any{"service": "Heroku bot", "running": pid != 0, "checkedAt": time.Now().UTC()})
}
