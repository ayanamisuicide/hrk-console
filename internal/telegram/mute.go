package telegram

import (
	"encoding/json"
	"os"
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

// MuteState — до какого времени молчать.
type MuteState struct {
	Until time.Time `json:"until"`
	// All глушит и критичные события.
	All bool `json:"all,omitempty"`
}

// MuteStore — тишина уведомлений с сохранением в файл.
type MuteStore struct {
	mu    sync.Mutex
	path  string
	state MuteState
	now   func() time.Time
}

// NewMuteStore читает сохранённое состояние; повреждённый или чужой файл игнорируется.
func NewMuteStore(path string, now func() time.Time) *MuteStore {
	m := &MuteStore{path: path, now: now}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &m.state)
		}
	}
	return m
}

// Active возвращает действующую тишину; истёкшая считается снятой.
func (m *MuteStore) Active() (MuteState, bool) {
	if m == nil {
		return MuteState{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Until.IsZero() || !m.now().Before(m.state.Until) {
		return MuteState{}, false
	}
	return m.state, true
}

// Set включает тишину до until; all глушит и критичные события.
func (m *MuteStore) Set(until time.Time, all bool) error {
	m.mu.Lock()
	m.state = MuteState{Until: until.UTC(), All: all}
	state := m.state
	m.mu.Unlock()
	return m.save(state)
}

// Clear снимает тишину.
func (m *MuteStore) Clear() error {
	m.mu.Lock()
	m.state = MuteState{}
	m.mu.Unlock()
	return m.save(MuteState{})
}

func (m *MuteStore) save(state MuteState) error {
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

// Silences отвечает, нужно ли промолчать о событии такой важности сейчас.
func (m *MuteStore) Silences(severity string) bool {
	state, ok := m.Active()
	if !ok {
		return false
	}
	return state.All || severity != "critical"
}

var (
	durationUnits = regexp.MustCompile(`(\d+)\s*(сут\p{L}*|дн\p{L}*|день|д|час\p{L}*|ч|h|мин\p{L}*|м|m|нед\p{L}*)`)
	untilMorning  = regexp.MustCompile(`до\s+утра`)
	untilTomorrow = regexp.MustCompile(`до\s+завтра|на\s+сутки|на\s+день|на\s+целый\s+день`)
)

// ParseMuteDuration достаёт длительность из фразы: «на час», «2 часа», «на 30 минут», «полчаса»,
// «до утра». Второе значение — нашлась ли длительность; без неё вызывающий берёт час.
func ParseMuteDuration(text string, now time.Time) (time.Duration, bool) {
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

// FormatMuteDuration пишет длительность по-русски: «1 ч», «30 мин», «1 д 2 ч».
func FormatMuteDuration(d time.Duration) string {
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
