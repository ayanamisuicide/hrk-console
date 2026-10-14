package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"heroku-console/internal/telegram"
	"heroku-console/logfeed"
)

// Сводка: что было за период — остановки и падения бота с причинами, ошибки и самые частые из
// них, нагрузка сервера, модули и новое (вышла версия панели, обновился Heroku, появились или
// пропали модули). По запросу («сводка», «что было за ночь», «сводка за неделю») и каждое утро
// в личный чат администраторов. Утренняя сводка охватывает время с прошлой.

const (
	digestFile        = "telegram-digest.json"
	digestDefaultAt   = "08:00"
	digestCheckEvery  = 30 * time.Second
	digestLateLimit   = 6 * time.Hour // панель лежала утром дольше — сводку того дня не шлём
	digestDefaultSpan = 24 * time.Hour
	digestMaxSpan     = 30 * 24 * time.Hour // столько хранит архив журнала
	digestListLimit   = 5
)

var digestTimeRe = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)

// digestAt — время утренней сводки из настроек («ЧЧ:ММ»).
func (settings telegramSettings) digestAt() (hour, minute int) {
	at := settings.DigestAt
	if !digestTimeRe.MatchString(at) {
		at = digestDefaultAt
	}
	m := digestTimeRe.FindStringSubmatch(at)
	hour, _ = strconv.Atoi(m[1])
	minute, _ = strconv.Atoi(m[2])
	return hour, minute
}

// digestSlot — последний наступивший срок утренней сводки.
func digestSlot(settings telegramSettings, now time.Time) time.Time {
	hour, minute := settings.digestAt()
	slot := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if slot.After(now) {
		slot = slot.AddDate(0, 0, -1)
	}
	return slot
}

// digestState — что было при прошлой утренней сводке: от него считается «новое».
type digestState struct {
	LastSent     time.Time `json:"lastSent"`
	BotVersion   string    `json:"botVersion,omitempty"`
	PanelVersion string    `json:"panelVersion,omitempty"`
	Modules      []string  `json:"modules,omitempty"`
}

func (c *telegramControl) digestPath() string {
	if c.s.dataDir == "" {
		return ""
	}
	return filepath.Join(c.s.dataDir, digestFile)
}

func (c *telegramControl) loadDigestState() digestState {
	var state digestState
	if path := c.digestPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &state)
		}
	}
	return state
}

func (c *telegramControl) saveDigestState(state digestState) {
	path := c.digestPath()
	if path == "" {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := writePrivateAtomic(path, data); err != nil {
		log.Printf("telegram: состояние сводки не сохранено: %v", err)
	}
}

// runDigest раз в полминуты проверяет, не пора ли прислать утреннюю сводку.
func (c *telegramControl) runDigest(ctx context.Context) {
	ticker := time.NewTicker(digestCheckEvery)
	defer ticker.Stop()
	for {
		c.digestTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// digestTick присылает сводку, если срок наступил, а сегодняшняя ещё не ушла. Нужен включённый
// режим управления: сводка приходит в личный чат с ботом, а её кнопки должны работать.
func (c *telegramControl) digestTick(ctx context.Context) {
	settings := c.settings()
	if c.api == nil || settings.DigestOff || !settings.ControlEnabled || len(c.admins) == 0 {
		return
	}
	now := c.now()
	slot := digestSlot(settings, now)
	state := c.loadDigestState()
	if !state.LastSent.Before(slot) || now.Sub(slot) > digestLateLimit {
		return
	}
	from := state.LastSent
	if from.IsZero() || now.Sub(from) > digestMaxSpan {
		from = now.Add(-digestDefaultSpan)
	}
	text, keyboard, next := c.buildDigest(from, now, true, state)
	for _, id := range c.adminIDs() {
		if c.sendMessageID(ctx, id, text, keyboard) == 0 {
			log.Printf("telegram: утренняя сводка не доставлена администратору %d (он ещё не писал боту?)", id)
		}
	}
	next.LastSent = now
	c.saveDigestState(next)
}

func (c *telegramControl) adminIDs() []int64 {
	ids := make([]int64, 0, len(c.admins))
	for id := range c.admins {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// cmdDigest — сводка по запросу; заодно меняет настройки утренней: «присылай сводку в 8:30»,
// «не присылай сводку».
func (c *telegramControl) cmdDigest(ctx context.Context, chat int64, user telegram.User, it telegram.Intent) {
	text := telegram.Normalize(strings.Join(it.Words, " "))
	off := telegram.HasStem(it.Words, "выключ", "отключ", "отмен", "убер", "хватит") || strings.Contains(text, "не присыл") ||
		strings.Contains(text, "не надо") || strings.Contains(text, "не шли") || strings.Contains(text, "не отправл")
	on := telegram.HasStem(it.Words, "присыл", "присла", "шли", "отправл", "включ", "кажд", "утр") && !off
	at, hasTime := digestTimeFrom(text)
	if off || (on && (hasTime || telegram.HasStem(it.Words, "присыл", "присла", "шли", "отправл", "включ"))) {
		c.configureDigest(ctx, chat, user, !off, at, hasTime)
		return
	}
	span := it.Since
	if span <= 0 {
		span = digestDefaultSpan
		if telegram.HasStem(it.Words, "ноч") {
			span = 12 * time.Hour
		}
	}
	c.sendDigest(ctx, chat, span)
}

// sendDigest присылает сводку за span до текущего момента.
func (c *telegramControl) sendDigest(ctx context.Context, chat int64, span time.Duration) {
	span = min(max(span, time.Hour), digestMaxSpan)
	c.answer(ctx, chat, func() (string, map[string]any) {
		now := c.now()
		text, keyboard, _ := c.buildDigest(now.Add(-span), now, false, c.loadDigestState())
		return text, keyboard
	})
}

var digestClockRe = regexp.MustCompile(`(?:^|\s)(?:в|к|на)\s*(\d{1,2})(?:[:.](\d{2}))?(?:\s|$)`)

// digestTimeFrom достаёт время из фразы: «в 8», «в 8:30», «к 9.15».
func digestTimeFrom(text string) (string, bool) {
	m := digestClockRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	if hour > 23 || minute > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), true
}

// configureDigest включает или выключает утреннюю сводку из чата и пишет это в журнал действий.
func (c *telegramControl) configureDigest(ctx context.Context, chat int64, user telegram.User, enabled bool, at string, hasTime bool) {
	if c.s.operations == nil {
		c.reply(ctx, chat, "Настройки на этом сервере не сохраняются.", c.nextKeyboard(chat, nextShort))
		return
	}
	settings := c.s.operations.telegramSettings()
	settings.DigestOff = !enabled
	if hasTime {
		settings.DigestAt = at
	}
	if err := c.s.operations.setTelegram(settings); err != nil {
		c.reply(ctx, chat, "⚠️ Не удалось сохранить: "+html.EscapeString(err.Error()), c.nextKeyboard(chat, nextShort))
		return
	}
	hour, minute := settings.digestAt()
	when := fmt.Sprintf("%02d:%02d", hour, minute)
	c.auditChat(user, "telegram.settings", fmt.Sprintf("digest=%t at=%s", enabled, when))
	text := "📰 <b>Утренняя сводка выключена</b>\nПо запросу она по-прежнему работает: напишите «сводка»."
	if enabled {
		text = "📰 <b>Утренняя сводка — каждый день в " + when + "</b>\nПо времени сервера, в этот чат. Выключить: «не присылай сводку»."
	}
	c.reply(ctx, chat, text, c.nextKeyboard(chat, nextShort))
}

/* ---------- Сборка сводки ---------- */

// digestStop — остановка бота за период по журналу панели.
type digestStop struct {
	at     time.Time
	crash  bool
	reason string
	down   time.Duration // 0 — бот ещё не запущен или запуск не найден
}

// buildDigest собирает текст и кнопки сводки за [from, to]. morning — утренняя: заголовок
// «Доброе утро» и сравнение с прошлой сводкой; next — состояние для следующей утренней.
func (c *telegramControl) buildDigest(from, to time.Time, morning bool, state digestState) (string, map[string]any, digestState) {
	loc := to.Location()
	d := c.menuSnapshot(to)
	var t strings.Builder
	if morning {
		day := "сегодня"
		if from.In(loc).YearDay() != to.In(loc).YearDay() {
			day = "вчера"
		}
		fmt.Fprintf(&t, "☀️ <b>Доброе утро! Сводка с %s %s</b>", from.In(loc).Format("15:04"), day)
	} else {
		fmt.Fprintf(&t, "📰 <b>Сводка за %s</b>", spanLabel(to.Sub(from)))
	}

	stops := c.digestStops(from, to)
	restarts, recovers := c.digestActions(from, to)
	bot := c.s.botJournal()
	hide := c.s.logRules.hider()
	fromMs, toMs := from.UnixMilli(), to.UnixMilli()+1
	errs, warns := countProblems(bot, fromMs, toMs, hide)
	span := toMs - fromMs
	prevErrs, _ := countProblems(bot, fromMs-span, fromMs, hide)

	calm := len(stops) == 0 && len(restarts) == 0 && recovers == 0 && errs == 0 && d.running
	if calm {
		period := "Ночь прошла"
		if !morning {
			period = "За это время всё прошло"
		}
		t.WriteString("\n🌙 " + period + " спокойно: бот работал без перерывов, ошибок нет.")
		if warns > 0 {
			fmt.Fprintf(&t, " Предупреждений %d.", warns)
		}
	} else {
		c.writeDigestBot(&t, d, stops, restarts, recovers, loc)
		if errs+warns > 0 {
			fmt.Fprintf(&t, "\n\nОшибок <b>%d</b>%s · предупреждений %d", errs, compareNote(errs, prevErrs), warns)
			for _, p := range logfeed.Problems(bot, fromMs, toMs, logfeed.Query{Hide: hide, Mask: maskSecrets}, 3) {
				fmt.Fprintf(&t, "\n  ×%d %s", p.Count, html.EscapeString(clip(p.Last.Message, 70)))
				if p.Last.Module != "" {
					t.WriteString(" · " + html.EscapeString(shortModule(p.Last.Module)))
				}
			}
		} else {
			t.WriteString("\n\nОшибок нет ✅")
		}
	}

	if line := c.digestServer(from, to, loc); line != "" {
		t.WriteString("\n" + line)
	}
	next := digestState{BotVersion: d.heavy.botVersion, PanelVersion: d.panelVer, Modules: state.Modules}
	if d.modulesLive {
		names := make([]string, 0, len(d.moduleList))
		for _, module := range d.moduleList {
			names = append(names, module.Name)
		}
		slices.Sort(names)
		next.Modules = names
		line := fmt.Sprintf("Модули: %d работают", d.modulesReady)
		if len(d.modulesFailed) > 0 {
			failed := make([]string, 0, len(d.modulesFailed))
			for _, module := range d.modulesFailed {
				failed = append(failed, module.Name)
			}
			line += fmt.Sprintf(", %d с ошибкой — %s", len(failed), strings.Join(failed, ", "))
		}
		t.WriteString("\n" + html.EscapeString(line))
	}

	news := c.digestNews(d, state, next)
	if len(news) > 0 {
		t.WriteString("\n\n🆕 " + strings.Join(news, "\n🆕 "))
	}
	if d.muteUntil.After(to) {
		t.WriteString("\n🔕 Уведомления выключены до " + d.muteUntil.In(loc).Format("15:04"))
	}

	var rows [][]map[string]string
	if d.updateTo != "" && c.s.updates != nil {
		rows = append(rows, []map[string]string{btn("⬆️ Обновить до "+d.updateTo, "upd:"+d.updateTo)})
	}
	rows = append(rows, []map[string]string{btn("🔴 Ошибки", "nx:e"), btn("📜 Логи", "nx:l"), btn("📋 Меню", "nx:m")})
	return t.String(), keyboardRows(rows...), next
}

// writeDigestBot — строки о боте: остановки с причинами, ручные перезапуски, автовосстановление.
func (c *telegramControl) writeDigestBot(t *strings.Builder, d menuData, stops []digestStop, restarts []string, recovers int, loc *time.Location) {
	crashes := 0
	for _, stop := range stops {
		if stop.crash {
			crashes++
		}
	}
	switch {
	case len(stops) == 0 && d.running:
		t.WriteString("\n🟢 Бот работал без перерывов")
		if d.uptime != "" {
			t.WriteString(" · сейчас " + html.EscapeString(d.uptime))
		}
	case crashes > 0:
		fmt.Fprintf(t, "\n🔴 Бот падал %d %s", crashes, plural(crashes, "раз", "раза", "раз"))
		if manual := len(stops) - crashes; manual > 0 {
			fmt.Fprintf(t, ", остановок вручную %d", manual)
		}
	case len(stops) > 0:
		fmt.Fprintf(t, "\n⏹ Остановок вручную: %d", len(stops))
	}
	for i, stop := range stops {
		if i == digestListLimit {
			fmt.Fprintf(t, "\n  … и ещё %d", len(stops)-i)
			break
		}
		fmt.Fprintf(t, "\n  %s %s", stop.at.In(loc).Format("15:04"), html.EscapeString(clip(stop.reason, 80)))
		if stop.down > 0 {
			t.WriteString(" · простоял " + humanDuration(stop.down))
		}
	}
	if !d.running {
		t.WriteString("\n⚠️ Сейчас бот не работает")
	}
	if len(restarts) > 0 {
		t.WriteString("\n🔁 Перезапуски вручную: " + html.EscapeString(strings.Join(restarts, ", ")))
	}
	if recovers > 0 {
		fmt.Fprintf(t, "\n🛟 Автовосстановление перезапускало бота: %d %s", recovers, plural(recovers, "раз", "раза", "раз"))
	}
}

// digestStops читает из журнала панели остановки бота и находит, когда его запустили снова.
func (c *telegramControl) digestStops(from, to time.Time) []digestStop {
	if c.s.panelLog == nil {
		return nil
	}
	journal := logfeed.Journal{Sources: []logfeed.Source{{Path: c.s.panelLog.path, Archive: c.s.logArchiveDir()}}}
	entries, _ := logfeed.Search(journal, logfeed.Query{Module: modulePanel, From: from.UnixMilli(), To: to.UnixMilli() + 1, Limit: 1000})
	var stops []digestStop
	for _, e := range entries {
		at := time.UnixMilli(e.TS)
		title, reason, _ := strings.Cut(e.Message, " — ")
		switch title {
		case "Бот упал", "Бот остановлен":
			if reason == "" {
				reason = title
			}
			stops = append(stops, digestStop{at: at, crash: title == "Бот упал", reason: reason})
		case "Бот запущен":
			for i := len(stops) - 1; i >= 0; i-- {
				if stops[i].down == 0 {
					stops[i].down = at.Sub(stops[i].at)
					break
				}
			}
		}
	}
	return stops
}

// digestActions — ручные перезапуски и запуски бота («@anya в 23:10») и число перезапусков
// автовосстановлением по журналу действий. Остановки видны в digestStops.
func (c *telegramControl) digestActions(from, to time.Time) (restarts []string, recovers int) {
	if c.s.audit == nil {
		return nil, 0
	}
	events, err := c.s.audit.recent(auditRetention)
	if err != nil {
		return nil, 0
	}
	for i := len(events) - 1; i >= 0; i-- { // от старых к новым
		event := events[i]
		if event.Time.Before(from) || event.Time.After(to) {
			continue
		}
		switch event.Action {
		case "bot.restart":
			restarts = append(restarts, strings.TrimSuffix(describeActor(event), " из панели")+" в "+event.Time.In(to.Location()).Format("15:04"))
		case "bot.recover":
			recovers++
		}
	}
	if len(restarts) > digestListLimit {
		restarts = append(restarts[:digestListLimit], fmt.Sprintf("и ещё %d", len(restarts)-digestListLimit))
	}
	return restarts, recovers
}

// digestServer — пик CPU, наибольшая занятость памяти и диск по истории нагрузки (она хранится сутки).
func (c *telegramControl) digestServer(from, to time.Time, loc *time.Location) string {
	if c.s.hostHistory == nil {
		return ""
	}
	points := c.s.hostHistory.since(from)
	var peak hostPoint
	maxMemory, disk := 0.0, 0.0
	for _, p := range points {
		if p.At.After(to) {
			break
		}
		cpu := max(p.CPU, p.CPUMax)
		if cpu > max(peak.CPU, peak.CPUMax) {
			peak = p
		}
		maxMemory = max(maxMemory, p.Memory)
		disk = p.Disk
	}
	if len(points) == 0 {
		return ""
	}
	line := fmt.Sprintf("Сервер: пик CPU %.0f%% в %s · память до %.0f%% · диск %.0f%%",
		max(peak.CPU, peak.CPUMax), peak.At.In(loc).Format("15:04"), maxMemory, disk)
	if to.Sub(from) > 25*time.Hour {
		line += " <i>(нагрузка — за последние сутки)</i>"
	}
	return line
}

// digestNews — что изменилось с прошлой утренней сводки.
func (c *telegramControl) digestNews(d menuData, state, next digestState) []string {
	var news []string
	if d.updateTo != "" {
		news = append(news, "Вышла hrk-console "+html.EscapeString(d.updateTo))
	}
	if state.PanelVersion != "" && next.PanelVersion != "" && state.PanelVersion != next.PanelVersion {
		news = append(news, "Панель обновлена: "+html.EscapeString(state.PanelVersion+" → "+next.PanelVersion))
	}
	if state.BotVersion != "" && next.BotVersion != "" && state.BotVersion != next.BotVersion {
		news = append(news, "Heroku обновился: "+html.EscapeString(state.BotVersion+" → "+next.BotVersion))
	}
	if len(state.Modules) > 0 && d.modulesLive {
		added, removed := diffNames(state.Modules, next.Modules)
		if len(added) > 0 {
			news = append(news, "Новые модули: "+html.EscapeString(strings.Join(clipList(added, 6), ", ")))
		}
		if len(removed) > 0 {
			news = append(news, "Пропали модули: "+html.EscapeString(strings.Join(clipList(removed, 6), ", ")))
		}
	}
	return news
}

func diffNames(before, after []string) (added, removed []string) {
	for _, name := range after {
		if !slices.Contains(before, name) {
			added = append(added, name)
		}
	}
	for _, name := range before {
		if !slices.Contains(after, name) {
			removed = append(removed, name)
		}
	}
	return added, removed
}

func clipList(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	return append(append([]string{}, names[:n]...), fmt.Sprintf("ещё %d", len(names)-n))
}

// countProblems — ошибки и предупреждения бота за окно без скрытых правилами записей.
func countProblems(j logfeed.Journal, from, to int64, hide func(logfeed.Entry) bool) (errs, warns int) {
	if to <= from {
		return 0, 0
	}
	for _, b := range logfeed.Collect(j, from, to, to-from, logfeed.Query{Hide: hide}).Buckets {
		errs += b.Err
		warns += b.Warn
	}
	return errs, warns
}

// compareNote — сравнение с прошлым таким же периодом: « (до этого 3)».
func compareNote(now, before int) string {
	if now == before {
		return ""
	}
	return fmt.Sprintf(" (до этого %d)", before)
}

// plural выбирает форму слова для числа: 1 раз, 2 раза, 5 раз.
func plural(n int, one, few, many string) string {
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		return one
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return few
	}
	return many
}

// spanLabel — «сутки», «неделю», «12 ч» для заголовка сводки.
func spanLabel(d time.Duration) string {
	d = d.Round(time.Hour)
	days := int(d / (24 * time.Hour))
	switch {
	case d == 24*time.Hour:
		return "сутки"
	case d == 7*24*time.Hour:
		return "неделю"
	case days >= 2 && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d %s", days, plural(days, "день", "дня", "дней"))
	}
	return fmt.Sprintf("%d ч", int(d/time.Hour))
}

// botJournal — только журнал бота: в сводке ошибки самой панели (падение бота и т. п.) уже
// посчитаны как остановки и не должны удваивать число ошибок.
func (s *server) botJournal() logfeed.Journal {
	return logfeed.Journal{Sources: []logfeed.Source{{Path: s.bot.LogFile, Archive: s.logArchiveDir()}}}
}

// digestView — состояние утренней сводки для админки.
func (c *telegramControl) digestView() map[string]any {
	settings := c.settings()
	hour, minute := settings.digestAt()
	view := map[string]any{"enabled": !settings.DigestOff, "at": fmt.Sprintf("%02d:%02d", hour, minute)}
	if state := c.loadDigestState(); !state.LastSent.IsZero() {
		view["lastSent"] = state.LastSent.UTC()
	}
	return view
}
