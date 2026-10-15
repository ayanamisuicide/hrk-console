package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"heroku-console/logfeed"
)

// Правила журнала относятся к группе одинаковых записей (подпись logfeed.ProblemKey: модуль,
// сообщение без чисел, тип исключения). Правило может скрыть группу из ленты, счётчиков и
// Telegram (известный безобидный шум) и/или уведомить, когда группа повторилась N раз за M минут.

type logRuleNotify struct {
	Count   int `json:"count"`
	Minutes int `json:"minutes"`
}

type logRule struct {
	ID        string         `json:"id"`
	Key       string         `json:"key"`
	Module    string         `json:"module"`
	Label     string         `json:"label"` // пример сообщения группы для показа
	Level     string         `json:"level"`
	Hide      bool           `json:"hide"`
	Notify    *logRuleNotify `json:"notify,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	// LastAlert — когда по правилу последний раз ушло уведомление: повтор не раньше окна правила.
	LastAlert time.Time `json:"lastAlert,omitzero"`
}

type logRuleStore struct {
	mu    sync.RWMutex
	path  string
	rules []logRule
}

const maxLogRules = 200

func openLogRules(path string) *logRuleStore {
	store := &logRuleStore{path: path}
	if data, err := os.ReadFile(path); err == nil {
		var rules []logRule
		if json.Unmarshal(data, &rules) == nil {
			store.rules = rules
		} else {
			log.Printf("правила журнала: %s повреждён, начинаем с пустого списка", path)
		}
	}
	return store
}

func (st *logRuleStore) list() []logRule {
	if st == nil {
		return []logRule{}
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	return append([]logRule{}, st.rules...)
}

// saveLocked пишет правила атомарно. Вызывается под st.mu.
func (st *logRuleStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st.rules, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

// upsert создаёт или обновляет правило группы. Правило без скрытия и уведомления удаляется.
func (st *logRuleStore) upsert(rule logRule) (logRule, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	at := -1
	for i, existing := range st.rules {
		if existing.Key == rule.Key {
			at = i
			break
		}
	}
	if at >= 0 {
		existing := st.rules[at]
		existing.Hide, existing.Notify = rule.Hide, rule.Notify
		if existing.Notify == nil {
			existing.LastAlert = time.Time{}
		}
		rule = existing
	} else {
		if len(st.rules) >= maxLogRules {
			return logRule{}, fmt.Errorf("правил не больше %d", maxLogRules)
		}
		random, err := randomToken(6)
		if err != nil {
			return logRule{}, err
		}
		rule.ID = random
		rule.CreatedAt = time.Now().UTC()
	}
	if !rule.Hide && rule.Notify == nil {
		if at >= 0 {
			st.rules = append(st.rules[:at], st.rules[at+1:]...)
		}
		return rule, st.saveLocked()
	}
	if at >= 0 {
		st.rules[at] = rule
	} else {
		st.rules = append(st.rules, rule)
	}
	return rule, st.saveLocked()
}

func (st *logRuleStore) byKey(key string) (logRule, bool) {
	if st == nil || key == "" {
		return logRule{}, false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, rule := range st.rules {
		if rule.Key == key {
			return rule, true
		}
	}
	return logRule{}, false
}

func (st *logRuleStore) remove(id string) (logRule, bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, rule := range st.rules {
		if rule.ID == id {
			st.rules = append(st.rules[:i], st.rules[i+1:]...)
			return rule, true, st.saveLocked()
		}
	}
	return logRule{}, false, nil
}

// hider возвращает проверку «запись скрыта правилом» или nil, если скрывающих правил нет.
// Подпись считается только для модулей, у которых есть такие правила: обычная запись
// не платит за регулярное выражение.
func (st *logRuleStore) hider() func(logfeed.Entry) bool {
	if st == nil {
		return nil
	}
	st.mu.RLock()
	keys := map[string]bool{}
	modules := map[string]bool{}
	for _, rule := range st.rules {
		if rule.Hide {
			keys[rule.Key] = true
			modules[rule.Module] = true
		}
	}
	st.mu.RUnlock()
	if len(keys) == 0 {
		return nil
	}
	return func(e logfeed.Entry) bool {
		return modules[e.Module] && keys[logfeed.ProblemKey(e)]
	}
}

// hideFor решает, скрывать ли записи по правилам для этого запроса: hidden=1 показывает всё.
func (s *server) hideFor(r *http.Request) func(logfeed.Entry) bool {
	if r != nil && r.URL.Query().Get("hidden") == "1" {
		return nil
	}
	return s.logRules.hider()
}

// --- HTTP ---

func (s *server) listLogRules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.logRules.list()})
}

type logRuleInput struct {
	Key    string         `json:"key"`
	TS     int64          `json:"ts"`
	ID     string         `json:"id"`
	Hide   bool           `json:"hide"`
	Notify *logRuleNotify `json:"notify"`
}

// saveLogRule создаёт или меняет правило группы. Группа задаётся подписью из «Частых ошибок» или
// записью (ts и id): подпись тогда считает сервер, браузер её не повторяет.
func (s *server) saveLogRule(w http.ResponseWriter, r *http.Request) {
	var input logRuleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	if input.Notify != nil {
		if input.Notify.Count < 1 || input.Notify.Count > 1000 || input.Notify.Minutes < 1 || input.Notify.Minutes > 1440 {
			writeJSON(w, http.StatusBadRequest, actionResponse{Message: "уведомление: от 1 до 1000 раз за 1–1440 минут"})
			return
		}
	}
	// У существующего правила группа уже известна; новую находит по записи или подписи.
	rule, known := s.logRules.byKey(input.Key)
	if !known {
		sample, err := s.ruleSample(input)
		if err != nil {
			writeJSON(w, http.StatusNotFound, actionResponse{Message: err.Error()})
			return
		}
		rule = logRule{Key: logfeed.ProblemKey(sample), Module: sample.Module, Level: sample.Level,
			Label: truncateRunes(maskSecrets(sample.Message), 200), Actor: s.requestActor(r)}
	}
	rule.Hide, rule.Notify = input.Hide, input.Notify
	saved, err := s.logRules.upsert(rule)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	var parts []string
	if input.Hide {
		parts = append(parts, "скрыть")
	}
	if input.Notify != nil {
		parts = append(parts, fmt.Sprintf("уведомлять при %d за %d мин", input.Notify.Count, input.Notify.Minutes))
	}
	if len(parts) == 0 {
		parts = append(parts, "снято")
	}
	s.record(r, s.requestActor(r), "log.rule", fmt.Sprintf("%s — %s", rule.Label, strings.Join(parts, ", ")))
	writeJSON(w, http.StatusOK, map[string]any{"rule": saved, "rules": s.logRules.list()})
}

// ruleSample находит запись, по которой строится правило: по подписи — самую свежую запись группы
// за неделю, по ts и id — саму запись.
func (s *server) ruleSample(input logRuleInput) (logfeed.Entry, error) {
	if input.Key != "" {
		module, _, _ := strings.Cut(input.Key, "\x00")
		found := false
		var sample logfeed.Entry
		from := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
		for _, p := range logfeed.Problems(s.journal(), from, 0, logfeed.Query{Module: module, Levels: []string{"DEBUG", "INFO", "WARNING", "ERROR", "OTHER"}}, 0) {
			if p.Key == input.Key {
				sample, found = p.Last, true
				break
			}
		}
		if !found {
			return logfeed.Entry{}, errors.New("записи этой группы не найдены")
		}
		return sample, nil
	}
	entries, index, ok := logfeed.AroundN(s.journal(), input.TS, input.ID, 0, 0)
	if !ok {
		return logfeed.Entry{}, errors.New("запись не найдена: возможно, журнал ротирован")
	}
	return entries[index], nil
}

func (s *server) deleteLogRule(w http.ResponseWriter, r *http.Request) {
	rule, ok, err := s.logRules.remove(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "правило не найдено"})
		return
	}
	s.record(r, s.requestActor(r), "log.rule", rule.Label+" — правило удалено")
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.logRules.list()})
}

// requestActor — имя вошедшего пользователя для аудита.
func (s *server) requestActor(r *http.Request) string {
	if p, ok := requestPrincipal(r); ok {
		return p.name
	}
	return "user"
}

// --- Уведомления ---

// runLogRuleAlerts раз в 30 секунд считает записи групп с уведомлением за окно правила и
// отправляет уведомление, когда порог достигнут. Повтор — не раньше, чем через окно правила.
func (s *server) runLogRuleAlerts(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkLogRuleAlerts(time.Now())
		}
	}
}

func (s *server) checkLogRuleAlerts(now time.Time) {
	if s.logRules == nil {
		return
	}
	rules := s.logRules.list()
	window := 0
	modules := map[string]bool{}
	byKey := map[string][]int{}
	for i, rule := range rules {
		if rule.Notify == nil {
			continue
		}
		if rule.Notify.Minutes > window {
			window = rule.Notify.Minutes
		}
		modules[rule.Module] = true
		byKey[rule.Key] = append(byKey[rule.Key], i)
	}
	if window == 0 {
		return
	}
	from := now.Add(-time.Duration(window) * time.Minute).UnixMilli()
	counts := make([]int, len(rules))
	s.journal().Backwards(func(e logfeed.Entry) bool {
		if e.TS > 0 && e.TS < from {
			return false
		}
		if !modules[e.Module] || e.TS == 0 {
			return true
		}
		for _, i := range byKey[logfeed.ProblemKey(e)] {
			if e.TS >= now.Add(-time.Duration(rules[i].Notify.Minutes)*time.Minute).UnixMilli() {
				counts[i]++
			}
		}
		return true
	})
	for i, rule := range rules {
		if rule.Notify == nil || counts[i] < rule.Notify.Count {
			continue
		}
		if !rule.LastAlert.IsZero() && now.Sub(rule.LastAlert) < time.Duration(rule.Notify.Minutes)*time.Minute {
			continue
		}
		s.logRules.markAlerted(rule.ID, now)
		severity := "warning"
		if rule.Level == "ERROR" || rule.Level == "CRITICAL" {
			severity = "critical"
		}
		module := rule.Module
		if module == "" {
			module = "без модуля"
		}
		s.alerts.notify(alertEvent{Kind: "log.rule", Severity: severity,
			Title:   "Повторяется: " + truncateRunes(rule.Label, 120),
			Message: fmt.Sprintf("%d раз за %d мин · %s. Правило журнала можно изменить в «Частых ошибках».", counts[i], rule.Notify.Minutes, module)})
	}
}

func (st *logRuleStore) markAlerted(id string, at time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.rules {
		if st.rules[i].ID == id {
			st.rules[i].LastAlert = at.UTC()
		}
	}
	if err := st.saveLocked(); err != nil {
		log.Printf("правила журнала: %v", err)
	}
}
