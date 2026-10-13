package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"heroku-console/botproc"
	"heroku-console/internal/telegram"
)

// Главное меню: одно сообщение-дашборд в личном чате с администратором. Бот сам правит его по
// таймеру (состояние бота, нагрузка сервера, модули, версия), а кнопки переключают экраны в
// этом же сообщении, как маленькое приложение. Правка не звенит и не плодит сообщения. Записей
// журнала в меню нет: «Логи» и «Ошибки» открывают отдельные сообщения.

const (
	menuFile            = "telegram-menu.json"
	menuTickEvery       = 250 * time.Millisecond // шаг планировщика: секундный период держится ровно
	menuIdleAfter       = 2 * time.Minute        // дольше администратор не нажимает — обновляем редко
	menuIdleEvery       = time.Minute
	menuDefaultInterval = 10 // секунд
	menuBackoff         = 30 * time.Second
	menuDataTTL         = 700 * time.Millisecond // данные не старше секундного периода
	menuHeavyTTL        = 30 * time.Second
)

// menuIntervals — доступные периоды автообновления в секундах; 0 — выключено.
var menuIntervals = []int{1, 5, 10, 60, 0}

// menuFloors — ступени, на которые бот сам замедляет обновление, когда Telegram отвечает 429.
var menuFloors = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

var retryAfterRe = regexp.MustCompile(`retry after (\d+)`)

// menuEntry — меню одного чата. Хранится в файле: после перезапуска панели меню продолжает жить.
type menuEntry struct {
	Message  int64  `json:"message"`
	Screen   string `json:"screen"`
	Interval int    `json:"interval"`
	Pinned   bool   `json:"pinned"`
}

type menuStore struct {
	mu      sync.Mutex
	path    string
	entries map[int64]*menuEntry
	touched map[int64]time.Time
	nextAt  map[int64]time.Time
	// floor — вынужденная нижняя граница периода после лимита Telegram; только в памяти.
	floor map[int64]time.Duration

	// Кэш собранных данных: несколько чатов и кнопки не должны каждый раз читать журнал и процессы.
	dataMu  sync.Mutex
	dataAt  time.Time
	data    menuData
	heavyAt time.Time
	heavy   menuHeavy
}

func newMenuStore(path string) *menuStore {
	m := &menuStore{path: path, entries: map[int64]*menuEntry{}, touched: map[int64]time.Time{}, nextAt: map[int64]time.Time{}, floor: map[int64]time.Duration{}}
	if path == "" {
		return m
	}
	if data, err := os.ReadFile(path); err == nil {
		var saved map[int64]*menuEntry
		if json.Unmarshal(data, &saved) == nil {
			for chat, e := range saved {
				if e != nil && e.Message != 0 && chat > 0 {
					m.entries[chat] = e
				}
			}
		}
	}
	return m
}

func menuFilePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, menuFile)
}

func (m *menuStore) saveLocked() {
	if m.path == "" {
		return
	}
	data, err := json.Marshal(m.entries)
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, m.path)
	}
}

func (m *menuStore) get(chat int64) (menuEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[chat]
	if !ok {
		return menuEntry{}, false
	}
	return *e, true
}

func (m *menuStore) put(chat int64, e menuEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[chat] = &e
	delete(m.nextAt, chat)
	m.saveLocked()
}

// update меняет запись под замком и сохраняет; false — меню в этом чате нет.
func (m *menuStore) update(chat int64, change func(*menuEntry)) (menuEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[chat]
	if !ok {
		return menuEntry{}, false
	}
	change(e)
	m.saveLocked()
	return *e, true
}

func (m *menuStore) remove(chat int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, chat)
	delete(m.nextAt, chat)
	m.saveLocked()
}

func (m *menuStore) touch(chat int64, now time.Time) {
	m.mu.Lock()
	m.touched[chat] = now
	delete(m.nextAt, chat) // после нажатия следующее плановое обновление считаем заново
	m.mu.Unlock()
}

func (m *menuStore) all() map[int64]menuEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[int64]menuEntry, len(m.entries))
	for chat, e := range m.entries {
		result[chat] = *e
	}
	return result
}

// effectiveInterval — с каким периодом реально обновлять: выбранный, пока человек рядом, и не
// чаще раза в минуту, когда он давно ничего не нажимал.
func (m *menuStore) effectiveInterval(chat int64, chosen int, now time.Time) time.Duration {
	if chosen <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.effectiveLocked(chat, chosen, now)
}

func (m *menuStore) effectiveLocked(chat int64, chosen int, now time.Time) time.Duration {
	if chosen <= 0 {
		return 0
	}
	d := time.Duration(chosen) * time.Second
	if touched := m.touched[chat]; touched.IsZero() || now.Sub(touched) > menuIdleAfter {
		d = max(d, menuIdleEvery)
	}
	// После лимита Telegram частота снижена, пока человек сам не выберет период заново.
	return max(d, m.floor[chat])
}

// resetFloor возвращает выбранную частоту: человек сам поменял период или открыл меню заново.
func (m *menuStore) resetFloor(chat int64) {
	m.mu.Lock()
	delete(m.floor, chat)
	m.mu.Unlock()
}

/* ---------- Данные ---------- */

// menuHeavy — то, что дорого собирать и редко меняется: чтение журнала.
type menuHeavy struct {
	errs, warns int
	haveJournal bool
	botVersion  string
}

type menuData struct {
	running bool
	pid     int
	uptime  string

	haveSystem    bool
	cpu, mem, dsk float64

	botCPU  float64
	botRSS  uint64
	rx, tx  float64
	haveNet bool

	modulesNote   string
	modulesTotal  int
	modulesReady  int
	modulesFailed []moduleEntry
	modulesLive   bool

	heavy      menuHeavy
	lastEvent  string
	watchdog   string
	updateTo   string
	muteUntil  time.Time
	muteAll    bool
	panelVer   string
	sampledAt  time.Time
	moduleList []moduleEntry
}

func (c *telegramControl) menuSnapshot(now time.Time) menuData {
	m := c.menu
	m.dataMu.Lock()
	defer m.dataMu.Unlock()
	if !m.dataAt.IsZero() && now.Sub(m.dataAt) < menuDataTTL && now.After(m.dataAt) {
		d := m.data
		d.muteUntil, d.muteAll = c.muteView()
		return d
	}
	d := menuData{panelVer: currentVersion().Version, sampledAt: now}
	d.pid = c.s.bot.PID()
	d.running = d.pid != 0
	if d.running {
		d.uptime = botproc.Uptime(d.pid)
	}
	c.s.systemMu.RLock()
	system := c.s.latestSystem
	c.s.systemMu.RUnlock()
	if system.Supported && !system.SampledAt.IsZero() {
		d.haveSystem = true
		d.cpu = system.CPUPercent
		d.mem = hostPercent(system.MemoryUsed, system.MemoryTotal)
		d.dsk = hostPercent(system.DiskUsed, system.DiskTotal)
	}
	if c.s.hostHistory != nil {
		if points := c.s.hostHistory.since(now.Add(-20 * time.Second)); len(points) > 0 {
			last := points[len(points)-1]
			d.botCPU, d.botRSS = last.BotCPU, last.BotRSS
			d.rx, d.tx = last.RxRate, last.TxRate
			d.haveNet = last.RxRate > 0 || last.TxRate > 0
		}
	}
	snap := readModulesSnapshot(c.s.bot.HerokuDir, d.pid, now)
	d.modulesLive = snap.Status == "live" || snap.Status == "stale"
	d.modulesNote = snap.Message
	d.moduleList = snap.Modules
	for _, module := range snap.Modules {
		d.modulesTotal++
		switch module.State {
		case "ready":
			d.modulesReady++
		case "error":
			d.modulesFailed = append(d.modulesFailed, module)
		}
	}
	if now.Sub(m.heavyAt) >= menuHeavyTTL || m.heavyAt.IsZero() {
		errs, warns, _ := c.recentProblems()
		m.heavy = menuHeavy{errs: errs, warns: warns, haveJournal: fileExists(c.s.bot.LogFile), botVersion: c.s.bot.Version()}
		m.heavyAt = now
	}
	d.heavy = m.heavy
	if events := c.s.timeline.since(now.Add(-24 * time.Hour)); len(events) > 0 {
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind != "test" && events[i].Title != "" {
				d.lastEvent = fmt.Sprintf("%s %s", events[i].Time.In(now.Location()).Format("15:04"), events[i].Title)
				break
			}
		}
	}
	if c.s.operations != nil {
		d.watchdog = "выключено"
		if c.s.operations.watchdogSettings().Enabled {
			d.watchdog = "включено"
			if c.s.watchdog.snapshot().State == "suspended" {
				d.watchdog = "приостановлено — бот падал раз за разом"
			}
		}
	}
	if c.s.updates != nil {
		if remote := c.s.updates.snapshot(); remote.Version != "" && compareVersions(remote.Version, d.panelVer) > 0 {
			d.updateTo = remote.Version
		}
	}
	m.data, m.dataAt = d, now
	d.muteUntil, d.muteAll = c.muteView()
	return d
}

func (c *telegramControl) muteView() (time.Time, bool) {
	if state, ok := c.mute.Active(); ok {
		return state.Until, state.All
	}
	return time.Time{}, false
}

/* ---------- Оформление ---------- */

// bar рисует полоску загрузки из восьми клеток.
func bar(percent float64) string {
	filled := int(percent/12.5 + 0.5)
	filled = min(max(filled, 0), 8)
	return strings.Repeat("▰", filled) + strings.Repeat("▱", 8-filled)
}

func ruPlural(n int, one, few, many string) string {
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		return one
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return few
	}
	return many
}

func intervalLabel(seconds int) string {
	switch {
	case seconds <= 0:
		return "выкл"
	case seconds < 60:
		return fmt.Sprintf("%d с", seconds)
	}
	return fmt.Sprintf("%d мин", seconds/60)
}

func (c *telegramControl) renderMenu(chat int64, e menuEntry, now time.Time) (string, map[string]any) {
	d := c.menuSnapshot(now)
	switch e.Screen {
	case "modules":
		return c.renderMenuModules(chat, e, d, now)
	case "more":
		return c.renderMenuMore(chat, e, d, now)
	}
	return c.renderMenuMain(chat, e, d, now)
}

func (c *telegramControl) menuFooter(chat int64, e menuEntry, now time.Time) string {
	every := "автообновление выключено"
	if eff := c.menu.effectiveInterval(chat, e.Interval, now); eff > 0 {
		every = "каждые " + intervalLabel(int(eff/time.Second))
		if eff == time.Second {
			every = "каждую секунду"
		}
		c.menu.mu.Lock()
		floor := c.menu.floor[chat]
		c.menu.mu.Unlock()
		switch {
		case floor > 0 && floor > time.Duration(e.Interval)*time.Second:
			every += " — частота снижена: лимит Telegram"
		case eff == menuIdleEvery && e.Interval > 0 && e.Interval < 60:
			every = "раз в минуту — вы давно не нажимали"
		}
	}
	return "<i>обновлено " + now.Format("15:04:05") + " · " + every + "</i>"
}

func (c *telegramControl) renderMenuMain(chat int64, e menuEntry, d menuData, now time.Time) (string, map[string]any) {
	var t strings.Builder
	if d.running {
		t.WriteString("🟢 <b>Heroku · главное меню</b>\n")
		fmt.Fprintf(&t, "<b>Бот работает</b> · %s · PID %d", html.EscapeString(d.uptime), d.pid)
	} else {
		t.WriteString("🔴 <b>Heroku · главное меню</b>\n<b>Бот остановлен</b>")
	}
	versions := []string{}
	if d.heavy.botVersion != "" {
		versions = append(versions, "бот "+html.EscapeString(d.heavy.botVersion))
	}
	panel := "панель " + html.EscapeString(d.panelVer)
	if d.updateTo != "" {
		panel += " ⬆️ доступна " + html.EscapeString(d.updateTo)
	} else {
		panel += " ✅"
	}
	versions = append(versions, panel)
	t.WriteString("\nВерсии: " + strings.Join(versions, " · "))

	if d.haveSystem {
		fmt.Fprintf(&t, "\n\n<pre>CPU    %s %3.0f%%\nПамять %s %3.0f%%\nДиск   %s %3.0f%%</pre>", bar(d.cpu), d.cpu, bar(d.mem), d.mem, bar(d.dsk), d.dsk)
	} else {
		t.WriteString("\n")
	}
	if d.running && (d.botCPU > 0 || d.botRSS > 0) {
		fmt.Fprintf(&t, "Бот: CPU %.1f%% · %s\n", d.botCPU, formatBytesShort(float64(d.botRSS)))
	}
	if d.haveNet {
		fmt.Fprintf(&t, "Сеть: ↓ %s/с · ↑ %s/с\n", formatBytesShort(d.rx), formatBytesShort(d.tx))
	}

	t.WriteString("\n" + c.modulesLine(d))
	if d.heavy.haveJournal {
		if d.heavy.errs+d.heavy.warns == 0 {
			t.WriteString("\n📈 За час: ошибок нет ✅")
		} else {
			fmt.Fprintf(&t, "\n📈 За час: %d %s · %d %s", d.heavy.errs, ruPlural(d.heavy.errs, "ошибка", "ошибки", "ошибок"),
				d.heavy.warns, ruPlural(d.heavy.warns, "предупреждение", "предупреждения", "предупреждений"))
		}
	}
	if d.lastEvent != "" {
		t.WriteString("\n🕘 Последнее событие: " + html.EscapeString(d.lastEvent))
	}
	if d.watchdog != "" {
		t.WriteString("\n🛟 Автовосстановление: " + html.EscapeString(d.watchdog))
	}
	if !d.muteUntil.IsZero() {
		note := "🔕 Уведомления выключены до " + d.muteUntil.In(now.Location()).Format("15:04")
		if !d.muteAll {
			note += " (критичные приходят)"
		}
		t.WriteString("\n" + note)
	} else {
		t.WriteString("\n🔔 Уведомления включены")
	}
	t.WriteString("\n\n" + c.menuFooter(chat, e, now))

	button := func(text, data string) map[string]any { return map[string]any{"text": text, "callback_data": data} }
	control := []map[string]any{button("▶ Запустить", "mn:run")}
	if d.running {
		control = []map[string]any{button("🔁 Перезапустить", "st:rs"), button("⏹ Остановить", "st:sp")}
	}
	mute := button("🔕 Тишина на час", "mn:mu1")
	if !d.muteUntil.IsZero() {
		mute = button("🔔 Включить", "mn:mu0")
	}
	rows := [][]map[string]any{
		{button("🧩 Модули", "mn:mod"), button("📜 Логи", "st:l"), button("🔴 Ошибки", "st:e")},
		control,
		{mute, button("🔄", "mn:r"), button("⏱ "+intervalLabel(e.Interval), "mn:cy")},
		{button("⚙️ Ещё", "mn:more")},
	}
	if url := c.webAppURL(); url != "" {
		rows[3] = append(rows[3], map[string]any{"text": "Открыть панель", "web_app": map[string]string{"url": url}})
	}
	return t.String(), map[string]any{"inline_keyboard": rows}
}

func (c *telegramControl) modulesLine(d menuData) string {
	if !d.running {
		return "🧩 Модули: бот остановлен"
	}
	if !d.modulesLive {
		return "🧩 Модули: данных пока нет"
	}
	line := fmt.Sprintf("🧩 Модули: %d из %d работают", d.modulesReady, d.modulesTotal)
	if n := len(d.modulesFailed); n > 0 {
		names := make([]string, 0, 3)
		for i, module := range d.modulesFailed {
			if i < 3 {
				names = append(names, module.Name)
			}
		}
		line += " · с ошибкой: " + html.EscapeString(strings.Join(names, ", "))
		if n > 3 {
			line += fmt.Sprintf(" и ещё %d", n-3)
		}
	}
	return line
}

func (c *telegramControl) renderMenuModules(chat int64, e menuEntry, d menuData, now time.Time) (string, map[string]any) {
	var t strings.Builder
	t.WriteString("🧩 <b>Модули бота</b>\n")
	switch {
	case !d.running:
		t.WriteString("Бот остановлен — состояние модулей недоступно.")
	case !d.modulesLive:
		t.WriteString(html.EscapeString(d.modulesNote))
	default:
		loading := d.modulesTotal - d.modulesReady - len(d.modulesFailed)
		fmt.Fprintf(&t, "Всего %d · работают %d", d.modulesTotal, d.modulesReady)
		if loading > 0 {
			fmt.Fprintf(&t, " · загружаются %d", loading)
		}
		if len(d.modulesFailed) == 0 {
			t.WriteString("\n\n✅ Все загруженные модули в порядке.")
		} else {
			fmt.Fprintf(&t, " · <b>с ошибкой %d</b>\n", len(d.modulesFailed))
			for i, module := range d.modulesFailed {
				if i >= 8 {
					fmt.Fprintf(&t, "\n… и ещё %d", len(d.modulesFailed)-8)
					break
				}
				reason := strings.TrimSpace(lastNonEmptyLine(module.Error))
				fmt.Fprintf(&t, "\n🔴 <b>%s</b>", html.EscapeString(module.Name))
				if reason != "" {
					t.WriteString("\n<i>" + html.EscapeString(clip(maskSecrets(reason), 120)) + "</i>")
				}
			}
		}
	}
	t.WriteString("\n\n" + c.menuFooter(chat, e, now))
	button := func(text, data string) map[string]any { return map[string]any{"text": text, "callback_data": data} }
	return t.String(), map[string]any{"inline_keyboard": [][]map[string]any{{button("⬅ Назад", "mn:m"), button("🔄", "mn:r")}}}
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

func (c *telegramControl) renderMenuMore(chat int64, e menuEntry, d menuData, now time.Time) (string, map[string]any) {
	var t strings.Builder
	t.WriteString("⚙️ <b>Настройки меню</b>\n")
	fmt.Fprintf(&t, "\nАвтообновление: <b>%s</b>", intervalLabel(e.Interval))
	if e.Interval > 0 && e.Interval < 60 {
		t.WriteString(" (когда вы долго не нажимаете — раз в минуту)")
	}
	pin := "нет"
	if e.Pinned {
		pin = "да"
	}
	fmt.Fprintf(&t, "\nЗакреплено: <b>%s</b>", pin)
	if d.updateTo != "" {
		t.WriteString("\n⬆️ Доступна новая версия панели: " + html.EscapeString(d.updateTo))
	}
	t.WriteString("\n\n" + c.menuFooter(chat, e, now))
	button := func(text, data string) map[string]any { return map[string]any{"text": text, "callback_data": data} }
	var intervals []map[string]any
	for _, seconds := range menuIntervals {
		label := intervalLabel(seconds)
		if seconds == 0 {
			label = "⏸ выкл"
		}
		if seconds == e.Interval {
			label = "✓ " + label
		}
		intervals = append(intervals, button(label, fmt.Sprintf("mn:iv%d", seconds)))
	}
	pinButton := button("📌 Закрепить", "mn:pin")
	if e.Pinned {
		pinButton = button("📍 Открепить", "mn:unpin")
	}
	rows := [][]map[string]any{
		intervals,
		{pinButton, button("⬆ Проверить обновление", "mn:upd")},
		{button("🔕 Тишина до утра", "mn:mum"), button("🔔 Включить уведомления", "mn:mu0")},
		{button("⬅ Назад", "mn:m")},
	}
	return t.String(), map[string]any{"inline_keyboard": rows}
}

/* ---------- Открытие и обновление ---------- */

// openMenu показывает меню внизу чата: прежнее сообщение удаляется, чтобы меню всегда было
// под рукой, а не терялось выше по переписке.
func (c *telegramControl) openMenu(ctx context.Context, chat int64) {
	if chat <= 0 {
		c.reply(ctx, chat, "Меню работает только в личном чате с ботом: в группе его правка мешала бы всем. Напишите мне в личные сообщения.", nil)
		return
	}
	now := c.now()
	old, hadOld := c.menu.get(chat)
	entry := menuEntry{Screen: "main", Interval: menuDefaultInterval}
	if hadOld {
		entry.Interval, entry.Pinned = old.Interval, old.Pinned
		_ = c.api.Call(ctx, "deleteMessage", map[string]any{"chat_id": chat, "message_id": old.Message}, nil)
	}
	c.menu.resetFloor(chat)
	c.menu.touch(chat, now)
	var id int64
	draft := c.think(ctx, chat)
	text, keyboard := c.renderMenu(chat, entry, now)
	id = c.streamTo(ctx, chat, draft, text, keyboard)
	if id == 0 {
		// Telegram не вернул номер сообщения (или не принял его): править нечего.
		if hadOld {
			c.menu.remove(chat)
		}
		return
	}
	entry.Message = id
	c.menu.put(chat, entry)
	if entry.Pinned {
		_ = c.api.Call(ctx, "pinChatMessage", map[string]any{"chat_id": chat, "message_id": id, "disable_notification": true}, nil)
	}
}

// editMenu перерисовывает сообщение меню и разбирает ответ Telegram.
func (c *telegramControl) editMenu(ctx context.Context, chat int64, e menuEntry, now time.Time) {
	text, keyboard := c.renderMenu(chat, e, now)
	payload := map[string]any{"chat_id": chat, "message_id": e.Message, "text": text, "parse_mode": "HTML",
		"disable_web_page_preview": true, "reply_markup": keyboard}
	c.menuEditResult(chat, c.api.Call(ctx, "editMessageText", payload, nil), now)
}

// menuEditResult решает, что делать после правки: «не изменилось» — не ошибка; лимит — подождать;
// сообщение удалено или чат закрыт — забыть меню, иначе бот вечно стучался бы впустую.
func (c *telegramControl) menuEditResult(chat int64, err error, now time.Time) {
	if err == nil || isNotModified(err) {
		return
	}
	var apiErr *telegram.APIError
	if !errors.As(err, &apiErr) {
		return // сеть: следующий тик повторит
	}
	switch apiErr.Code {
	case http.StatusTooManyRequests:
		// Лимит: ждём, сколько просит Telegram, и дальше обновляем реже на одну ступень.
		wait := menuBackoff
		if m := retryAfterRe.FindStringSubmatch(apiErr.Description); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > 0 {
				wait = time.Duration(n+1) * time.Second
			}
		}
		c.menu.mu.Lock()
		if e, ok := c.menu.entries[chat]; ok {
			current := c.menu.effectiveLocked(chat, e.Interval, now)
			for _, step := range menuFloors {
				if step > current {
					c.menu.floor[chat] = step
					break
				}
			}
			if current >= menuFloors[len(menuFloors)-1] {
				c.menu.floor[chat] = current
			}
		}
		c.menu.nextAt[chat] = now.Add(wait)
		c.menu.mu.Unlock()
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
		c.menu.remove(chat)
	}
}

// menuTick — один проход по всем меню: обновляет те, чей срок подошёл. Вызывается циклом
// runMenus; отдельной функцией, чтобы тесты управляли временем.
func (c *telegramControl) menuTick(ctx context.Context, now time.Time) {
	if !c.canAct() {
		return // без слушающего бота кнопки мертвы — обновлять незачем
	}
	for chat, e := range c.menu.all() {
		interval := c.menu.effectiveInterval(chat, e.Interval, now)
		if interval == 0 {
			continue
		}
		c.menu.mu.Lock()
		due, scheduled := c.menu.nextAt[chat]
		if !scheduled {
			// Первый проход после открытия или нажатия: только назначаем срок, правка была только что.
			c.menu.nextAt[chat] = now.Add(interval)
			c.menu.mu.Unlock()
			continue
		}
		if now.Before(due) {
			c.menu.mu.Unlock()
			continue
		}
		// Срок считается от прошлого срока, а не от момента правки: так секундный период не
		// накапливает задержку сети и шага планировщика.
		next := due.Add(interval)
		if !next.After(now) {
			next = now.Add(interval)
		}
		c.menu.nextAt[chat] = next
		c.menu.mu.Unlock()
		// Зависший запрос не должен останавливать обновление остальных меню.
		editCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c.editMenu(editCtx, chat, e, now)
		cancel()
	}
}

// runMenus обновляет меню, пока работает панель.
func (c *telegramControl) runMenus(ctx context.Context) {
	ticker := time.NewTicker(menuTickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.menuTick(ctx, c.now())
		}
	}
}

/* ---------- Кнопки ---------- */

var tagRe = regexp.MustCompile(`<[^>]+>`)

// plainText убирает разметку: всплывающий ответ на нажатие принимает только простой текст.
func plainText(text string) string {
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(text, "")))
}

// handleMenuCallback — кнопки меню: mn:<действие>.
func (c *telegramControl) handleMenuCallback(ctx context.Context, callback *telegram.Callback, act string, answer func(string)) {
	if callback.Message == nil {
		answer("Сообщение недоступно")
		return
	}
	chat := callback.Message.Chat.ID
	entry, ok := c.menu.get(chat)
	if !ok || entry.Message != callback.Message.MessageID {
		answer("Это старое меню — напишите «меню», открою новое")
		return
	}
	c.menu.touch(chat, c.now())
	reply := ""
	switch {
	case act == "m", act == "mod", act == "more":
		screen := map[string]string{"m": "main", "mod": "modules", "more": "more"}[act]
		entry, _ = c.menu.update(chat, func(e *menuEntry) { e.Screen = screen })
	case act == "r":
		reply = "Обновлено"
	case act == "cy":
		next := menuIntervals[0]
		for i, seconds := range menuIntervals {
			if seconds == entry.Interval {
				next = menuIntervals[(i+1)%len(menuIntervals)]
				break
			}
		}
		c.menu.resetFloor(chat)
		entry, _ = c.menu.update(chat, func(e *menuEntry) { e.Interval = next })
		reply = "Автообновление: " + intervalLabel(next)
	case strings.HasPrefix(act, "iv"):
		seconds := -1
		fmt.Sscanf(strings.TrimPrefix(act, "iv"), "%d", &seconds)
		valid := false
		for _, allowed := range menuIntervals {
			valid = valid || allowed == seconds
		}
		if !valid {
			answer("Кнопка устарела")
			return
		}
		c.menu.resetFloor(chat)
		entry, _ = c.menu.update(chat, func(e *menuEntry) { e.Interval = seconds })
		reply = "Автообновление: " + intervalLabel(seconds)
	case act == "run":
		reply = clip(plainText(c.execute(callback.From, "start")), 190)
		c.invalidateMenuData()
	case act == "mu1", act == "mum":
		d := time.Hour
		if act == "mum" {
			d, _ = telegram.ParseMuteDuration("до утра", c.now())
		}
		if err := c.mute.Set(c.now().Add(d), false); err != nil {
			answer("Не удалось сохранить")
			return
		}
		c.auditChat(callback.From, "telegram.mute", "из меню, "+telegram.FormatMuteDuration(d))
		reply = "Тишина на " + telegram.FormatMuteDuration(d) + ". Критичное придёт."
	case act == "mu0":
		if err := c.mute.Clear(); err != nil {
			answer("Не удалось")
			return
		}
		c.auditChat(callback.From, "telegram.unmute", "из меню")
		reply = "Уведомления включены"
	case act == "pin":
		if err := c.api.Call(ctx, "pinChatMessage", map[string]any{"chat_id": chat, "message_id": entry.Message, "disable_notification": true}, nil); err != nil {
			answer("Не удалось закрепить: " + clip(err.Error(), 120))
			return
		}
		entry, _ = c.menu.update(chat, func(e *menuEntry) { e.Pinned = true })
		reply = "Закреплено"
	case act == "unpin":
		_ = c.api.Call(ctx, "unpinChatMessage", map[string]any{"chat_id": chat, "message_id": entry.Message}, nil)
		entry, _ = c.menu.update(chat, func(e *menuEntry) { e.Pinned = false })
		reply = "Откреплено"
	case act == "upd":
		answer("Проверяю…")
		c.cmdUpdate(ctx, chat)
		return
	default:
		answer("Кнопка устарела")
		return
	}
	answer(reply)
	c.editMenu(ctx, chat, entry, c.now())
}

// invalidateMenuData сбрасывает кэш: после действия меню должно показать новое состояние сразу.
func (c *telegramControl) invalidateMenuData() {
	c.menu.dataMu.Lock()
	c.menu.dataAt = time.Time{}
	c.menu.heavyAt = time.Time{}
	c.menu.dataMu.Unlock()
}
