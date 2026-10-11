package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Тишина: «помолчи на час» глушит уведомления в Telegram до указанного времени. По умолчанию
// критичные события (бот упал) всё равно приходят: тишина нужна, чтобы не мешали повторы,
// а не чтобы пропустить аварию. Фраза «совсем» глушит и их. Состояние хранится в файле, поэтому
// перезапуск панели — например, при обновлении — тишину не снимает.

const muteFile = "telegram-mute.json"

func muteFilePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, muteFile)
}

type muteState struct {
	Until time.Time `json:"until"`
	// All глушит и критичные события.
	All bool `json:"all,omitempty"`
}

type muteStore struct {
	mu    sync.Mutex
	path  string
	state muteState
	now   func() time.Time
}

// newMuteStore читает сохранённое состояние; повреждённый или чужой файл игнорируется.
func newMuteStore(path string, now func() time.Time) *muteStore {
	m := &muteStore{path: path, now: now}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &m.state)
		}
	}
	return m
}

// active возвращает действующую тишину; истёкшая считается снятой.
func (m *muteStore) active() (muteState, bool) {
	if m == nil {
		return muteState{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Until.IsZero() || !m.now().Before(m.state.Until) {
		return muteState{}, false
	}
	return m.state, true
}

func (m *muteStore) set(until time.Time, all bool) error {
	m.mu.Lock()
	m.state = muteState{Until: until.UTC(), All: all}
	state := m.state
	m.mu.Unlock()
	return m.save(state)
}

func (m *muteStore) clear() error {
	m.mu.Lock()
	m.state = muteState{}
	m.mu.Unlock()
	return m.save(muteState{})
}

func (m *muteStore) save(state muteState) error {
	if m.path == "" {
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// silences отвечает, нужно ли промолчать об этом событии сейчас.
func (m *muteStore) silences(event alertEvent) bool {
	state, ok := m.active()
	if !ok {
		return false
	}
	return state.All || event.Severity != "critical"
}

var (
	durationUnits = regexp.MustCompile(`(\d+)\s*(сут\p{L}*|дн\p{L}*|день|д|час\p{L}*|ч|h|мин\p{L}*|м|m|нед\p{L}*)`)
	untilMorning  = regexp.MustCompile(`до\s+утра`)
	untilTomorrow = regexp.MustCompile(`до\s+завтра|на\s+сутки|на\s+день|на\s+целый\s+день`)
)

// parseMuteDuration достаёт длительность из фразы: «на час», «2 часа», «на 30 минут», «полчаса»,
// «до утра». Второе значение — нашлась ли длительность; без неё вызывающий берёт час.
func parseMuteDuration(text string, now time.Time) (time.Duration, bool) {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	if untilMorning.MatchString(text) {
		next := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
		if !next.After(now.Add(30 * time.Minute)) {
			next = next.AddDate(0, 0, 1)
		}
		return next.Sub(now), true
	}
	if untilTomorrow.MatchString(text) {
		return 24 * time.Hour, true
	}
	if strings.Contains(text, "полчаса") || strings.Contains(text, "пол часа") {
		return 30 * time.Minute, true
	}
	if m := durationUnits.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return 0, false
		}
		unit := m[2]
		var d time.Duration
		switch {
		case strings.HasPrefix(unit, "нед"):
			d = time.Duration(n) * 7 * 24 * time.Hour
		case strings.HasPrefix(unit, "сут"), strings.HasPrefix(unit, "дн"), unit == "день", unit == "д":
			d = time.Duration(n) * 24 * time.Hour
		case strings.HasPrefix(unit, "час"), unit == "ч", unit == "h":
			d = time.Duration(n) * time.Hour
		default:
			d = time.Duration(n) * time.Minute
		}
		return d, true
	}
	if regexp.MustCompile(`(^|\s)(на\s+)?час(\s|$)`).MatchString(text) {
		return time.Hour, true
	}
	return 0, false
}

// formatMuteDuration пишет длительность по-русски: «1 ч», «30 мин», «1 д 2 ч».
func formatMuteDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d%(24*time.Hour)) / int(time.Hour)
	minutes := int(d%time.Hour) / int(time.Minute)
	var parts []string
	if days > 0 {
		parts = append(parts, strconv.Itoa(days)+" д")
	}
	if hours > 0 {
		parts = append(parts, strconv.Itoa(hours)+" ч")
	}
	if minutes > 0 && days == 0 {
		parts = append(parts, strconv.Itoa(minutes)+" мин")
	}
	if len(parts) == 0 {
		return "меньше минуты"
	}
	return strings.Join(parts, " ")
}
