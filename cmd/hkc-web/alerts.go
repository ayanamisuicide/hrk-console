package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// alertSettings — Какие события отправлять и пороги ресурсов. Секреты каналов доставки
// (токен Telegram, адрес webhook) задаются только окружением и в браузер не попадают.
type alertSettings struct {
	BotState bool `json:"botState"`
	Watchdog bool `json:"watchdog"`
	Modules  bool `json:"modules"`
	// Пороги в процентах; 0 выключает проверку ресурса.
	CPUPercent     int `json:"cpuPercent"`
	MemoryPercent  int `json:"memoryPercent"`
	DiskPercent    int `json:"diskPercent"`
	SustainSeconds int `json:"sustainSeconds"`
}

func defaultAlertSettings() alertSettings {
	return alertSettings{BotState: true, Watchdog: true, Modules: true,
		CPUPercent: 90, MemoryPercent: 90, DiskPercent: 90, SustainSeconds: 300}
}

func validateAlertSettings(settings alertSettings) error {
	for _, item := range []struct {
		name  string
		value int
	}{{"CPU", settings.CPUPercent}, {"памяти", settings.MemoryPercent}, {"диска", settings.DiskPercent}} {
		if item.value != 0 && (item.value < 50 || item.value > 100) {
			return fmt.Errorf("порог %s должен быть 0 (выключен) или от 50 до 100%%", item.name)
		}
	}
	if settings.SustainSeconds < 30 || settings.SustainSeconds > 3600 {
		return errors.New("длительность превышения должна быть от 30 до 3600 секунд")
	}
	return nil
}

// allows решает по типу события, разрешено ли оно настройками. Пороги ресурсов проверяет их монитор.
func (settings alertSettings) allows(kind string) bool {
	switch {
	case strings.HasPrefix(kind, "bot."):
		return settings.BotState
	case strings.HasPrefix(kind, "watchdog."):
		return settings.Watchdog
	case strings.HasPrefix(kind, "module."):
		return settings.Modules
	}
	return true
}

// alertEvent — Одно уведомление. Kind — машинный тип (bot.stopped, host.disk, test…),
// Severity — critical, warning, ok или info.
type alertEvent struct {
	Kind     string    `json:"event"`
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	Host     string    `json:"host"`
	Time     time.Time `json:"time"`
}

type alertSink interface {
	name() string
	send(ctx context.Context, event alertEvent) error
}

const (
	alertRateLimit  = 20
	alertRateWindow = 10 * time.Minute
)

// alertDispatcher рассылает события во все настроенные каналы. Общий лимит частоты
// защищает чат от лавины при петле падений; ошибки доставки не влияют на управление ботом.
type alertDispatcher struct {
	sinks    []alertSink
	settings func() alertSettings
	// record получает каждое событие до фильтров и лимита: так лента графика полна
	// независимо от настроек доставки.
	record func(alertEvent)
	host   string
	mu     sync.Mutex
	sent   []time.Time
}

func (d *alertDispatcher) active() bool { return d != nil && len(d.sinks) > 0 }

func (d *alertDispatcher) channels() map[string]bool {
	result := map[string]bool{"telegram": false, "webhook": false}
	if d != nil {
		for _, sink := range d.sinks {
			result[sink.name()] = true
		}
	}
	return result
}

// allowRate отбрасывает события сверх лимита в скользящем окне.
func (d *alertDispatcher) allowRate(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := now.Add(-alertRateWindow)
	kept := d.sent[:0]
	for _, at := range d.sent {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	d.sent = kept
	if len(d.sent) >= alertRateLimit {
		return false
	}
	d.sent = append(d.sent, now)
	return true
}

func (d *alertDispatcher) prepare(event alertEvent) alertEvent {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	event.Time = event.Time.UTC()
	event.Host = d.host
	return event
}

// notify фильтрует событие настройками и лимитом и отправляет его в фоне.
func (d *alertDispatcher) notify(event alertEvent) {
	if d == nil {
		return
	}
	if d.record != nil {
		d.record(d.prepare(event))
	}
	if !d.active() {
		return
	}
	if d.settings != nil && !d.settings().allows(event.Kind) {
		return
	}
	if !d.allowRate(time.Now()) {
		log.Printf("уведомления: превышен лимит %d за %s, событие %s пропущено", alertRateLimit, alertRateWindow, event.Kind)
		return
	}
	event = d.prepare(event)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for channel, err := range d.deliver(ctx, event) {
			if err != nil {
				log.Printf("уведомления %s: %v", channel, err)
			}
		}
	}()
}

// deliver отправляет событие во все каналы параллельно и возвращает ошибку каждого канала.
func (d *alertDispatcher) deliver(ctx context.Context, event alertEvent) map[string]error {
	results := make(map[string]error, len(d.sinks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, sink := range d.sinks {
		wg.Add(1)
		go func(sink alertSink) {
			defer wg.Done()
			err := sink.send(ctx, event)
			mu.Lock()
			results[sink.name()] = err
			mu.Unlock()
		}(sink)
	}
	wg.Wait()
	return results
}

// webhookSink сохраняет прежний формат {"service","status","time"} для событий запуска
// и остановки и дополняет его полями event, severity, title, message и host.
type webhookSink struct {
	endpoint string
	client   *http.Client
}

func (sink *webhookSink) name() string { return "webhook" }

func (sink *webhookSink) send(ctx context.Context, event alertEvent) error {
	payload := map[string]any{"service": "Heroku bot", "event": event.Kind, "severity": event.Severity,
		"title": event.Title, "message": event.Message, "host": event.Host, "time": event.Time}
	switch event.Kind {
	case "bot.started":
		payload["status"] = "running"
	case "bot.stopped":
		payload["status"] = "stopped"
	}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sink.endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := sink.client.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

var (
	telegramTokenPattern = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{30,64}$`)
	telegramChatPattern  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

// telegramSink отправляет сообщения через Bot API. Токен бота входит в URL запроса,
// поэтому ошибки сети пересказываются без адреса.
type telegramSink struct {
	token   string
	chatID  string
	apiBase string
	client  *http.Client
}

func (sink *telegramSink) name() string { return "telegram" }

var severityMarks = map[string]string{"critical": "🔴", "warning": "🟠", "ok": "🟢", "info": "🔵"}

func telegramText(event alertEvent) string {
	mark := severityMarks[event.Severity]
	if mark == "" {
		mark = "🔵"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s <b>%s</b>", mark, html.EscapeString(event.Title))
	if event.Message != "" {
		text.WriteString("\n" + html.EscapeString(event.Message))
	}
	footer := "hrk-console"
	if event.Host != "" {
		footer = event.Host + " · " + footer
	}
	text.WriteString("\n<i>" + html.EscapeString(footer) + "</i>")
	return text.String()
}

func (sink *telegramSink) send(ctx context.Context, event alertEvent) error {
	data, _ := json.Marshal(map[string]any{"chat_id": sink.chatID, "text": telegramText(event),
		"parse_mode": "HTML", "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sink.apiBase+"/bot"+sink.token+"/sendMessage", bytes.NewReader(data))
	if err != nil {
		return errors.New("некорректный запрос к Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := sink.client.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	var answer struct {
		Description string `json:"description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&answer)
	if answer.Description != "" {
		return fmt.Errorf("Telegram HTTP %d: %s", res.StatusCode, strings.ReplaceAll(answer.Description, sink.token, "***"))
	}
	return fmt.Errorf("Telegram HTTP %d", res.StatusCode)
}

// redactURLError убирает адрес из сетевой ошибки: в нём может быть секрет канала.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return errors.New("превышено время ожидания ответа")
		}
		return fmt.Errorf("сетевая ошибка: %v", urlErr.Err)
	}
	return err
}

// newAlertDispatcher собирает каналы из окружения. Некорректные значения отключают
// только свой канал, не мешая запуску панели.
func newAlertDispatcher(getenv func(string) string, settings func() alertSettings) *alertDispatcher {
	dispatcher := &alertDispatcher{settings: settings}
	dispatcher.host, _ = os.Hostname()
	client := &http.Client{Timeout: 10 * time.Second}
	if raw := strings.TrimSpace(getenv("HKC_WEBHOOK_URL")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			log.Print("HKC_WEBHOOK_URL должен быть HTTPS URL без логина в адресе; webhook отключён")
		} else {
			dispatcher.sinks = append(dispatcher.sinks, &webhookSink{endpoint: raw, client: client})
		}
	}
	token := strings.TrimSpace(getenv("HKC_TELEGRAM_BOT_TOKEN"))
	if path := strings.TrimSpace(getenv("HKC_TELEGRAM_BOT_TOKEN_FILE")); token == "" && path != "" {
		if data, err := os.ReadFile(path); err == nil {
			token = strings.TrimSpace(string(data))
		} else {
			log.Print("не удалось прочитать HKC_TELEGRAM_BOT_TOKEN_FILE; Telegram отключён")
		}
	}
	chat := strings.TrimSpace(getenv("HKC_TELEGRAM_CHAT_ID"))
	switch {
	case token == "" && chat == "":
	case !telegramTokenPattern.MatchString(token):
		log.Print("HKC_TELEGRAM_BOT_TOKEN не похож на токен @BotFather; Telegram отключён")
	case !telegramChatPattern.MatchString(chat):
		log.Print("HKC_TELEGRAM_CHAT_ID должен быть числовым ID чата или @username канала; Telegram отключён")
	default:
		// Свой адрес нужен для локального Bot API-сервера и тестов.
		base := strings.TrimRight(strings.TrimSpace(getenv("HKC_TELEGRAM_API_URL")), "/")
		if base == "" {
			base = "https://api.telegram.org"
		}
		dispatcher.sinks = append(dispatcher.sinks, &telegramSink{token: token, chatID: chat, apiBase: base, client: client})
	}
	return dispatcher
}

// thresholdState — Состояние одного ресурса: с какого момента он выше порога и отправлено ли уведомление.
type thresholdState struct {
	aboveSince time.Time
	firing     bool
}

// alertHysteresis — запас в процентах ниже порога, после которого превышение считается завершённым.
// Без него значение около порога порождало бы пары «тревога/норма».
const alertHysteresis = 5

// hostAlertMonitor отслеживает длительное превышение порогов CPU, памяти и диска.
type hostAlertMonitor struct {
	mu     sync.Mutex
	states map[string]*thresholdState
}

var hostAlertLabels = map[string]string{"cpu": "CPU", "memory": "Память", "disk": "Диск"}

// observe принимает секундный замер и возвращает события начала и окончания превышения.
func (m *hostAlertMonitor) observe(now time.Time, settings alertSettings, values map[string]float64) []alertEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states == nil {
		m.states = map[string]*thresholdState{}
	}
	limits := map[string]int{"cpu": settings.CPUPercent, "memory": settings.MemoryPercent, "disk": settings.DiskPercent}
	sustain := time.Duration(settings.SustainSeconds) * time.Second
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var events []alertEvent
	for _, key := range keys {
		value, limit := values[key], limits[key]
		state := m.states[key]
		if state == nil {
			state = &thresholdState{}
			m.states[key] = state
		}
		label := hostAlertLabels[key]
		if limit == 0 {
			// Выключенный порог завершает активное превышение без отдельного уведомления.
			*state = thresholdState{}
			continue
		}
		switch {
		case value >= float64(limit):
			if state.aboveSince.IsZero() {
				state.aboveSince = now
			}
			if !state.firing && now.Sub(state.aboveSince) >= sustain {
				state.firing = true
				events = append(events, alertEvent{Kind: "host." + key, Severity: "critical", Time: now,
					Title:   fmt.Sprintf("%s выше %d%%", label, limit),
					Message: fmt.Sprintf("Сейчас %.0f%%, превышение длится %s.", value, formatAlertDuration(now.Sub(state.aboveSince)))})
			}
		case value < float64(limit-alertHysteresis):
			if state.firing {
				events = append(events, alertEvent{Kind: "host." + key, Severity: "ok", Time: now,
					Title:   fmt.Sprintf("%s в норме", label),
					Message: fmt.Sprintf("Сейчас %.0f%%, превышение длилось %s.", value, formatAlertDuration(now.Sub(state.aboveSince)))})
			}
			*state = thresholdState{}
		}
	}
	return events
}

func formatAlertDuration(duration time.Duration) string {
	duration = duration.Round(time.Second)
	if duration < time.Minute {
		return fmt.Sprintf("%d с", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%d мин", int(duration.Minutes()))
	}
	return fmt.Sprintf("%d ч %d мин", int(duration.Hours()), int(duration.Minutes())%60)
}

// moduleAlertMonitor сообщает о модулях, перешедших в ошибку в текущем запуске бота.
// Первый снимок после старта панели только запоминается, чтобы перезапуск панели не повторял старые ошибки.
type moduleAlertMonitor struct {
	mu          sync.Mutex
	initialized bool
	session     string
	reported    map[string]bool
}

func (m *moduleAlertMonitor) observe(snapshot modulesSnapshot) []alertEvent {
	if snapshot.Status != "live" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := fmt.Sprintf("%d/%s", snapshot.PID, snapshot.Session)
	baseline := !m.initialized
	if session != m.session {
		m.session = session
		m.reported = map[string]bool{}
	}
	m.initialized = true
	var fresh []moduleEntry
	for _, module := range snapshot.Modules {
		if module.State != "error" {
			// Модуль, вышедший из ошибки, сообщит о себе снова при следующем сбое.
			delete(m.reported, module.ID)
			continue
		}
		if !m.reported[module.ID] {
			m.reported[module.ID] = true
			fresh = append(fresh, module)
		}
	}
	if baseline || len(fresh) == 0 {
		return nil
	}
	names := make([]string, 0, len(fresh))
	for index, module := range fresh {
		if index == 5 {
			names = append(names, fmt.Sprintf("и ещё %d", len(fresh)-5))
			break
		}
		name := module.Name
		if name == "" {
			name = module.ID
		}
		names = append(names, name)
	}
	title := "Ошибка модуля " + names[0]
	if len(fresh) > 1 {
		title = fmt.Sprintf("Ошибки модулей: %d", len(fresh))
	}
	message := strings.Join(names, ", ")
	if detail := firstLine(fresh[0].Error); detail != "" {
		message += "\n" + detail
	}
	return []alertEvent{{Kind: "module.error", Severity: "warning", Title: title, Message: message}}
}

func firstLine(text string) string {
	text, _, _ = strings.Cut(strings.TrimSpace(text), "\n")
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:200]) + "…"
	}
	return text
}

// runModuleAlerts раз в пять секунд проверяет снимок модулей работающего бота.
func (s *server) runModuleAlerts(ctx context.Context) {
	if s.alerts == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			pid := s.bot.PID()
			if pid == 0 {
				continue
			}
			for _, event := range s.moduleAlerts.observe(readModulesSnapshot(s.bot.HerokuDir, pid, now)) {
				s.alerts.notify(event)
			}
		}
	}
}

// observeHostAlerts вызывается сборщиком истории после каждого замера.
func (s *server) observeHostAlerts(now time.Time, status systemStatus) {
	if s.alerts == nil || s.operations == nil || !status.Supported {
		return
	}
	values := map[string]float64{"cpu": status.CPUPercent,
		"memory": hostPercent(status.MemoryUsed, status.MemoryTotal),
		"disk":   hostPercent(status.DiskUsed, status.DiskTotal)}
	for _, event := range s.hostAlerts.observe(now, s.operations.alertSettings(), values) {
		s.alerts.notify(event)
	}
}

type alertsResponse struct {
	Channels map[string]bool `json:"channels"`
	Settings alertSettings   `json:"settings"`
}

func (s *server) getAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, alertsResponse{Channels: s.alerts.channels(), Settings: s.operations.alertSettings()})
}

func (s *server) setAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	settings := s.operations.alertSettings()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректные настройки уведомлений"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "ожидался один объект настроек"})
		return
	}
	if err := s.operations.setAlerts(settings); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
		return
	}
	s.record(r, "admin", "alerts.update", fmt.Sprintf("bot=%t watchdog=%t modules=%t cpu=%d memory=%d disk=%d sustain=%d",
		settings.BotState, settings.Watchdog, settings.Modules, settings.CPUPercent, settings.MemoryPercent, settings.DiskPercent, settings.SustainSeconds))
	s.getAlerts(w, r)
}

// testAlert отправляет пробное сообщение синхронно и сообщает результат каждого канала.
// Лимит частоты на него не распространяется, но вызов доступен только администратору.
func (s *server) testAlert(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	if !s.alerts.active() {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "каналы уведомлений не настроены: задайте HKC_TELEGRAM_BOT_TOKEN и HKC_TELEGRAM_CHAT_ID или HKC_WEBHOOK_URL"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	event := s.alerts.prepare(alertEvent{Kind: "test", Severity: "info", Title: "Проверка уведомлений",
		Message: "Если вы видите это сообщение, канал настроен правильно."})
	type channelResult struct {
		Channel string `json:"channel"`
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
	}
	results := []channelResult{}
	allOK := true
	for channel, err := range s.alerts.deliver(ctx, event) {
		item := channelResult{Channel: channel, OK: err == nil}
		if err != nil {
			item.Error = err.Error()
			allOK = false
		}
		results = append(results, item)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Channel < results[j].Channel })
	message := "Пробное уведомление доставлено."
	if !allOK {
		message = "Не все каналы приняли пробное уведомление."
	}
	s.record(r, "admin", "alerts.test", message)
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "message": message, "results": results})
}
