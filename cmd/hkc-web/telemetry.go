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

	"heroku-console/botproc"
	"heroku-console/logfeed"
)

type auditEvent struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

type auditStore struct {
	mu   sync.Mutex
	path string
}

func newAuditStore(path string) *auditStore { return &auditStore{path: path} }

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
	defer f.Close()
	return json.NewEncoder(f).Encode(event)
}

func (a *auditStore) recent(limit int) ([]auditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.Open(a.path)
	if os.IsNotExist(err) {
		return []auditEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result := make([]auditEvent, 0, limit)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
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
		return nil, err
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

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

type metricPoint struct {
	Time time.Time `json:"time"`
	RSS  uint64    `json:"rssBytes"`
}

type metricStore struct {
	mu     sync.Mutex
	points []metricPoint
}

func newMetricStore() *metricStore { return &metricStore{} }

func (m *metricStore) append(point metricPoint) []metricPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.points) == 0 || point.Time.Sub(m.points[len(m.points)-1].Time) >= 5*time.Second {
		m.points = append(m.points, point)
		if len(m.points) > 120 {
			m.points = m.points[len(m.points)-120:]
		}
	}
	return append([]metricPoint{}, m.points...)
}

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

func (s *server) insights(w http.ResponseWriter, _ *http.Request) {
	pid := botproc.PID()
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
	rss := processRSS(pid)
	points := []metricPoint{}
	if s.metrics != nil {
		points = s.metrics.append(metricPoint{Time: time.Now().UTC(), RSS: rss})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": pid != 0, "pid": pid, "rssBytes": rss, "points": points,
		"logCounts": counts, "sampledLines": len(lines), "logReady": fileExists(s.bot.LogFile),
	})
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

type diagnosticCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func (s *server) diagnostics(w http.ResponseWriter, _ *http.Request) {
	checks := []diagnosticCheck{
		{Name: "Каталог Heroku", OK: fileExists(s.bot.HerokuDir), Detail: s.bot.HerokuDir},
		{Name: "Виртуальное окружение", OK: s.bot.VirtualEnv() != "", Detail: "Поддерживаются .venv и venv"},
		{Name: "Файл журнала", OK: fileExists(s.bot.LogFile), Detail: "Появится после первого запуска бота"},
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func (s *server) publicStatus(w http.ResponseWriter, _ *http.Request) {
	if os.Getenv("HKC_PUBLIC_STATUS") != "1" {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "публичный статус выключен"})
		return
	}
	pid := botproc.PID()
	writeJSON(w, http.StatusOK, map[string]any{"service": "Heroku bot", "running": pid != 0, "checkedAt": time.Now().UTC()})
}
