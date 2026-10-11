package main

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"heroku-console/logfeed"
)

// Журнал в чате: «покажи логи», «что упало», «найди таймаут». Записи собираются так же, как на
// странице журнала (traceback внутри записи), повторы схлопываются, а секреты в тексте
// скрываются: сообщения Telegram хранятся у третьей стороны и пересылаются.

const (
	logViewTTL      = 30 * time.Minute
	logViewMax      = 60
	logsDefaultN    = 12
	logsMaxN        = 40
	errorsDefaultIn = 6 * time.Hour
	chatBudget      = 3400 // видимый предел текста в одном сообщении (лимит Telegram — 4096)
	fileMaxEntries  = 2000
)

// logView — выбранный просмотр журнала. К нему привязаны кнопки под сообщением: нажатие
// находит его по короткому коду, потому что в callback_data помещается всего 64 байта.
type logView struct {
	mode   string // logs | errors | search
	module string
	text   string
	level  string // "", WARNING, ERROR
	since  time.Duration
	from   int64 // абсолютные границы (из уведомления об инциденте), мс
	to     int64
	limit  int
	// Курсор «Старее»: время самой старой показанной записи и число показанных с этим временем.
	before  int64
	skip    int
	expires time.Time
	// Заполняются при показе: группы ошибок и модули для кнопок.
	groups  []errGroup
	modules []string
}

type errGroup struct {
	module  string
	message string
	count   int
	last    logfeed.Entry
}

/* ---------- Скрытие секретов ---------- */

var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{35}\b`), "‹токен бота›"},
	{regexp.MustCompile(`\bhkc\.[A-Za-z0-9_.-]{12,}`), "‹токен hkc›"},
	{regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`), "‹ключ›"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`), "‹токен GitHub›"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), "‹ключ Google›"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "‹JWT›"},
	{regexp.MustCompile(`(?i)(://[^/\s:@]+:)[^@\s/]+@`), "${1}‹пароль›@"},
	{regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{12,}`), "${1} ‹токен›"},
	{regexp.MustCompile(`(?i)\b((?:api[_-]?key|apikey|access[_-]?token|auth[_-]?token|token|secret|password|passwd|pwd|session|authorization)["']?\s*[:=]\s*["']?)[^\s"',;&)}\]]{4,}`), "${1}‹скрыто›"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`), "‹хеш›"},
}

// maskSecrets скрывает в тексте токены, ключи, пароли и подобное. Список не исчерпывающий:
// это защита от очевидных утечек, а не гарантия.
func maskSecrets(text string) string {
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllString(text, p.with)
	}
	return text
}

/* ---------- Просмотры ---------- */

func (c *telegramControl) registerView(v *logView) string {
	nonce, err := randomToken(6)
	if err != nil {
		nonce = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	now := c.now()
	v.expires = now.Add(logViewTTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, other := range c.views {
		if now.After(other.expires) {
			delete(c.views, key)
		}
	}
	for len(c.views) >= logViewMax {
		var oldest string
		var oldestAt time.Time
		for key, other := range c.views {
			if oldest == "" || other.expires.Before(oldestAt) {
				oldest, oldestAt = key, other.expires
			}
		}
		delete(c.views, oldest)
	}
	c.views[nonce] = v
	return nonce
}

func (c *telegramControl) viewByNonce(nonce string) (*logView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.views[nonce]
	if !ok || c.now().After(v.expires) {
		delete(c.views, nonce)
		return nil, false
	}
	v.expires = c.now().Add(logViewTTL)
	return v, true
}

// knownModules — имена модулей из недавнего журнала, самые шумные первыми. По ним слова фразы
// («логи updates») превращаются в фильтр по модулю.
func (c *telegramControl) knownModules() []string {
	now := c.now()
	stats := logfeed.Collect(c.s.bot.LogFile, now.Add(-6*time.Hour).UnixMilli(), now.UnixMilli()+1, int64(7*time.Hour/time.Millisecond), logfeed.Query{})
	names := make([]string, 0, len(stats.Modules))
	for name := range stats.Modules {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if stats.Modules[names[i]] != stats.Modules[names[j]] {
			return stats.Modules[names[i]] > stats.Modules[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

func (v *logView) query(now time.Time) logfeed.Query {
	q := logfeed.Query{Module: v.module, Text: v.text, Limit: v.limit, BeforeTS: v.before, BeforeSkip: v.skip}
	switch v.level {
	case "ERROR":
		q.Levels = []string{"ERROR"}
	case "WARNING":
		q.Levels = []string{"WARNING"}
	}
	if v.from > 0 {
		q.From = v.from
	} else if v.since > 0 {
		q.From = now.Add(-v.since).UnixMilli()
	}
	if v.to > 0 {
		q.To = v.to
	}
	return q
}

/* ---------- Оформление ---------- */

var digitsRe = regexp.MustCompile(`\d+`)

func clip(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	return string(r[:n-1]) + "…"
}

func levelTag(level string) string {
	switch level {
	case "WARNING":
		return "WARN "
	case "CRITICAL":
		return "CRIT "
	case "ERROR":
		return "ERROR"
	case "INFO":
		return "INFO "
	case "DEBUG":
		return "DEBUG"
	}
	return "     "
}

func hms(e logfeed.Entry) string {
	if len(e.Time) >= 19 {
		return e.Time[11:19]
	}
	return "--:--:--"
}

func shortModule(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	return clip(name, 14)
}

func describeSince(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d д", int(d/(24*time.Hour)))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d ч", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d мин", int(d/time.Minute))
	}
}

func (v *logView) filterNote() string {
	var parts []string
	if v.module != "" {
		parts = append(parts, "модуль <code>"+html.EscapeString(v.module)+"</code>")
	}
	if v.level == "WARNING" {
		parts = append(parts, "предупреждения")
	}
	if v.since > 0 && v.from == 0 {
		parts = append(parts, "за "+describeSince(v.since))
	}
	if v.from > 0 {
		parts = append(parts, fmt.Sprintf("%s–%s", time.UnixMilli(v.from).Format("15:04"), time.UnixMilli(v.to).Format("15:04")))
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// logLines оформляет записи строками для <pre>: время, уровень, модуль, сообщение. Подряд
// идущие одинаковые записи собираются в одну с «×N». Лишнее обрезается с начала, чтобы влезть
// в сообщение; возвращается число реально показанных записей — от него считается курсор.
func logLines(entries []logfeed.Entry, budget int) (lines []string, shown []logfeed.Entry) {
	type row struct {
		text    string
		entries []logfeed.Entry
	}
	var rows []row
	for _, e := range entries {
		key := e.Level + "\x00" + e.Module + "\x00" + e.Message
		if n := len(rows); n > 0 && len(rows[n-1].entries[0].Trace) == 0 && len(e.Trace) == 0 {
			first := rows[n-1].entries[0]
			if first.Level+"\x00"+first.Module+"\x00"+first.Message == key {
				rows[n-1].entries = append(rows[n-1].entries, e)
				continue
			}
		}
		rows = append(rows, row{entries: []logfeed.Entry{e}})
	}
	for i := range rows {
		first := rows[i].entries[0]
		line := fmt.Sprintf("%s %s %-14s %s", hms(first), levelTag(first.Level), shortModule(first.Module), clip(maskSecrets(first.Message), 110))
		if n := len(rows[i].entries); n > 1 {
			line += fmt.Sprintf(" ×%d", n)
		}
		if t := len(first.Trace); t > 0 {
			line += fmt.Sprintf(" [+%d стр.]", t)
		}
		rows[i].text = line
	}
	used := 0
	start := len(rows)
	for i := len(rows) - 1; i >= 0; i-- {
		cost := htmlVisibleLen(html.EscapeString(rows[i].text)) + 1
		if used+cost > budget && start < len(rows) {
			break
		}
		used += cost
		start = i
	}
	for _, r := range rows[start:] {
		lines = append(lines, r.text)
		shown = append(shown, r.entries...)
	}
	return lines, shown
}

// cursorOf — курсор «Старее» по реально показанным записям.
func cursorOf(shown []logfeed.Entry) (before int64, skip int) {
	if len(shown) == 0 {
		return 0, 0
	}
	before = shown[0].TS
	for _, e := range shown {
		if e.TS == before {
			skip++
		}
	}
	return before, skip
}

// topModules — модули показанных записей по убыванию числа записей.
func topModules(entries []logfeed.Entry, n int) []string {
	counts := map[string]int{}
	for _, e := range entries {
		if e.Module != "" {
			counts[e.Module]++
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > n {
		names = names[:n]
	}
	return names
}

func btn(text, data string) map[string]string {
	return map[string]string{"text": text, "callback_data": data}
}

func keyboardRows(rows ...[]map[string]string) map[string]any {
	var kept [][]map[string]string
	for _, r := range rows {
		if len(r) > 0 {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": kept}
}

// renderLogView достаёт данные и собирает текст с кнопками для текущего состояния просмотра.
func (c *telegramControl) renderLogView(nonce string, v *logView) (string, map[string]any) {
	now := c.now()
	switch v.mode {
	case "errors":
		return c.renderErrors(nonce, v, now)
	default:
		return c.renderEntries(nonce, v, now)
	}
}

func (c *telegramControl) renderEntries(nonce string, v *logView, now time.Time) (string, map[string]any) {
	q := v.query(now)
	entries, more := logfeed.Search(c.s.bot.LogFile, q)
	title := "📜 <b>Журнал</b>"
	if v.mode == "search" {
		title = "🔎 <b>«" + html.EscapeString(clip(v.text, 40)) + "»</b>"
	}
	if len(entries) == 0 {
		note := "Записей нет."
		if v.mode == "search" {
			note = "Ничего не нашёл. Поиск идёт по сообщению, модулю и traceback во всём журнале, включая ротации."
		} else if v.before > 0 {
			note = "Это самое начало журнала."
		}
		return title + v.filterNote() + "\n" + note, keyboardRows([]map[string]string{btn("🔄 Свежее", "lg:"+nonce+":fresh")})
	}
	lines, shown := logLines(entries, chatBudget)
	v.before, v.skip = cursorOf(shown)
	// Курсор «Старее» — по самой старой показанной записи: обрезанные по размеру вернутся при нажатии.
	hasOlder := more || len(shown) < len(entries)
	v.modules = topModules(shown, 3)
	var text strings.Builder
	text.WriteString(title + v.filterNote())
	if v.mode == "search" {
		fmt.Fprintf(&text, " · %d", len(shown))
		if hasOlder {
			text.WriteString("+")
		}
	}
	text.WriteString("\n<pre>" + html.EscapeString(strings.Join(lines, "\n")) + "</pre>")

	var nav, tools, mods []map[string]string
	if hasOlder {
		nav = append(nav, btn("⬅ Старее", "lg:"+nonce+":older"))
	}
	nav = append(nav, btn("🔄 Свежее", "lg:"+nonce+":fresh"))
	if v.mode != "search" {
		tools = append(tools, btn("🔴 Ошибки", "lg:"+nonce+":errs"))
	}
	tools = append(tools, btn("📄 Файлом", "lg:"+nonce+":file"))
	if v.module == "" {
		for i, name := range v.modules {
			if i < 2 {
				mods = append(mods, btn("🔎 "+shortModule(name), "lg:"+nonce+":mod"+strconv.Itoa(i)))
			}
		}
	}
	return text.String(), keyboardRows(nav, tools, mods)
}

func (c *telegramControl) renderErrors(nonce string, v *logView, now time.Time) (string, map[string]any) {
	since := v.since
	if since <= 0 && v.from == 0 {
		since = errorsDefaultIn
		v.since = since
	}
	q := v.query(now)
	q.Levels = []string{"ERROR"}
	q.Limit = 400
	q.BeforeTS, q.BeforeSkip = 0, 0
	entries, _ := logfeed.Search(c.s.bot.LogFile, q)
	window := "за " + describeSince(since)
	if v.from > 0 {
		window = fmt.Sprintf("%s–%s", time.UnixMilli(v.from).Format("15:04"), time.UnixMilli(v.to).Format("15:04"))
	}
	if len(entries) == 0 {
		return "✅ <b>Ошибок нет</b> · " + window + strings.TrimPrefix(v.filterNote(), " · "+window) +
			"\nМожно посмотреть всё подряд.", keyboardRows([]map[string]string{btn("📜 Все записи", "lg:"+nonce+":logs"), btn("🔄 Заново", "lg:"+nonce+":fresh")})
	}
	groups := map[string]*errGroup{}
	for _, e := range entries {
		key := e.Module + "\x00" + digitsRe.ReplaceAllString(e.Message, "#")
		g := groups[key]
		if g == nil {
			g = &errGroup{module: e.Module, message: e.Message}
			groups[key] = g
		}
		g.count++
		g.last = e // записи идут по возрастанию времени: последняя перезапишет
	}
	list := make([]errGroup, 0, len(groups))
	for _, g := range groups {
		list = append(list, *g)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].last.TS > list[j].last.TS })
	if len(list) > 6 {
		list = list[:6]
	}
	v.groups = list

	var text strings.Builder
	fmt.Fprintf(&text, "🔴 <b>Ошибки</b> · %s · %d", window, len(entries))
	if len(groups) > 1 {
		fmt.Fprintf(&text, " (%d разных)", len(groups))
	}
	if v.module != "" {
		text.WriteString(" · <code>" + html.EscapeString(v.module) + "</code>")
	}
	text.WriteString("\n")
	used := text.Len()
	var tb []map[string]string
	for i, g := range list {
		line := fmt.Sprintf("\n<b>%d.</b> ", i+1)
		if g.count > 1 {
			line += fmt.Sprintf("<b>×%d</b> ", g.count)
		}
		line += "<code>" + html.EscapeString(shortModule(g.module)) + "</code> " + html.EscapeString(clip(maskSecrets(g.message), 120)) +
			" <i>" + hms(g.last) + "</i>"
		if used+len(line) > chatBudget*2 {
			break
		}
		text.WriteString(line)
		used += len(line)
		tb = append(tb, btn("🧵 "+strconv.Itoa(i+1), "lg:"+nonce+":tb"+strconv.Itoa(i)))
	}
	var tools, mods []map[string]string
	tools = append(tools, btn("📜 Все записи", "lg:"+nonce+":logs"), btn("📄 Файлом", "lg:"+nonce+":file"))
	v.modules = nil
	if v.module == "" {
		seen := map[string]bool{}
		for _, g := range list {
			if g.module != "" && !seen[g.module] && len(v.modules) < 2 {
				seen[g.module] = true
				mods = append(mods, btn("🔎 "+shortModule(g.module), "lg:"+nonce+":mod"+strconv.Itoa(len(v.modules))))
				v.modules = append(v.modules, g.module)
			}
		}
	}
	return text.String(), keyboardRows(tb, tools, mods)
}

/* ---------- Команды и кнопки ---------- */

// showLog — ответ на «логи», «ошибки» и «найди …»: новый просмотр с кнопками.
func (c *telegramControl) showLog(ctx context.Context, chat int64, it intent) {
	c.answer(ctx, chat, func() (string, map[string]any) {
		v := &logView{mode: "logs", module: it.module, level: it.level, since: it.since, limit: it.limit}
		switch it.kind {
		case intentErrors:
			v.mode, v.level = "errors", "ERROR"
		case intentSearch:
			v.mode, v.text = "search", it.text
		}
		if v.module == "" {
			v.module = moduleFrom(it.words, c.knownModules())
		}
		if v.limit <= 0 || v.limit > logsMaxN {
			v.limit = logsDefaultN
			if it.limit > logsMaxN {
				v.limit = logsMaxN
			}
		}
		if v.mode == "search" && it.limit <= 0 {
			v.limit = 15
		}
		nonce := c.registerView(v)
		return c.renderLogView(nonce, v)
	})
}

// showIncidentLog — кнопка «Что случилось» под уведомлением: журнал в окне вокруг инцидента.
func (c *telegramControl) showIncidentLog(ctx context.Context, chat int64, from, to time.Time) {
	c.answer(ctx, chat, func() (string, map[string]any) {
		v := &logView{mode: "logs", limit: 25, from: from.Add(-2 * time.Minute).UnixMilli(), to: to.Add(2 * time.Minute).UnixMilli()}
		return c.renderLogView(c.registerView(v), v)
	})
}

func (c *telegramControl) logFile(v *logView) (string, []byte) {
	q := v.query(c.now())
	q.Limit, q.BeforeTS, q.BeforeSkip = fileMaxEntries, 0, 0
	if v.mode == "errors" {
		q.Levels = []string{"ERROR"}
	}
	entries, more := logfeed.Search(c.s.bot.LogFile, q)
	var out strings.Builder
	fmt.Fprintf(&out, "# hrk-console · журнал %s · записей: %d%s\n# Секреты в тексте скрыты автоматически, но список шаблонов неполный — проверьте перед пересылкой.\n\n",
		c.now().Format("2006-01-02 15:04:05"), len(entries), map[bool]string{true: " (самые новые; есть более старые)", false: ""}[more])
	for _, e := range entries {
		out.WriteString(maskSecrets(rawEntry(e)) + "\n")
	}
	name := "heroku-log-" + c.now().Format("2006-01-02-1504") + ".txt"
	return name, []byte(out.String())
}

func rawEntry(e logfeed.Entry) string {
	head := e.Message
	if e.Time != "" {
		head = fmt.Sprintf("%s [%s] %s: %s", e.Time, e.Level, e.Module, e.Message)
	}
	return strings.Join(append([]string{head}, e.Trace...), "\n")
}

// handleLogCallback обрабатывает кнопки под просмотром журнала: lg:<код>:<действие>.
func (c *telegramControl) handleLogCallback(ctx context.Context, callback *tgCallback, data string, answer func(string)) {
	nonce, act, _ := strings.Cut(data, ":")
	v, ok := c.viewByNonce(nonce)
	if !ok || callback.Message == nil {
		answer("Кнопка устарела")
		return
	}
	chat, message := callback.Message.Chat.ID, callback.Message.MessageID
	edit := func() {
		text, keyboard := c.renderLogView(nonce, v)
		c.editMessage(ctx, chat, message, text, keyboard)
	}
	switch {
	case act == "older":
		answer("Старее…")
		edit()
	case act == "fresh":
		v.before, v.skip = 0, 0
		answer("Обновляю…")
		edit()
	case act == "errs":
		v.mode, v.level, v.before, v.skip = "errors", "ERROR", 0, 0
		answer("Только ошибки")
		edit()
	case act == "logs":
		v.mode, v.level, v.before, v.skip = "logs", "", 0, 0
		if v.limit <= 0 {
			v.limit = logsDefaultN
		}
		answer("Все записи")
		edit()
	case strings.HasPrefix(act, "mod"):
		index, _ := strconv.Atoi(strings.TrimPrefix(act, "mod"))
		if index < 0 || index >= len(v.modules) {
			answer("Кнопка устарела")
			return
		}
		v.module, v.before, v.skip = v.modules[index], 0, 0
		answer("Модуль " + shortModule(v.module))
		edit()
	case act == "file":
		answer("Готовлю файл…")
		name, content := c.logFile(v)
		caption := fmt.Sprintf("Журнал · %s", describeSize(len(content)))
		if err := c.api.upload(ctx, "sendDocument", map[string]string{"chat_id": strconv.FormatInt(chat, 10), "caption": caption}, "document", name, content); err != nil {
			c.send(ctx, chat, "⚠️ Не удалось отправить файл: "+html.EscapeString(err.Error()), nil)
		}
	case strings.HasPrefix(act, "tb"):
		index, _ := strconv.Atoi(strings.TrimPrefix(act, "tb"))
		if index < 0 || index >= len(v.groups) {
			answer("Кнопка устарела")
			return
		}
		answer("Traceback")
		g := v.groups[index]
		raw := maskSecrets(rawEntry(g.last))
		if utf8.RuneCountInString(raw) > 3500 {
			raw = string([]rune(raw)[:3500]) + "\n…"
		}
		c.reply(ctx, chat, "🧵 <b>"+html.EscapeString(shortModule(g.module))+"</b>"+countNote(g.count)+"\n<pre>"+html.EscapeString(raw)+"</pre>", nil)
	default:
		answer("Кнопка устарела")
	}
}

func countNote(n int) string {
	if n > 1 {
		return fmt.Sprintf(" · последняя из %d", n)
	}
	return ""
}
