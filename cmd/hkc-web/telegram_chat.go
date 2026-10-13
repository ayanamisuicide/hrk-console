package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"strconv"
	"strings"
	"time"

	"heroku-console/botproc"
	"heroku-console/internal/telegram"
	"heroku-console/logfeed"
)

// Разговорная часть бота: что отвечать на фразы, живой статус с кнопками, тишина, подтверждение
// словом «да». Слэш-команды приходят сюда же, поэтому поведение у них одно.

const muteMax = 7 * 24 * time.Hour

// handleIntent выполняет распознанное намерение. Опасные действия идут через подтверждение.
func (c *telegramControl) handleIntent(ctx context.Context, message *telegram.Message, it telegram.Intent) {
	chat := message.Chat.ID
	user := *message.From
	switch it.Kind {
	case telegram.IntentYes, telegram.IntentNo, telegram.IntentCancel:
		c.resolveByWord(ctx, chat, user, it.Kind)
	case telegram.IntentStatus:
		c.cmdStatus(ctx, chat)
	case telegram.IntentRun:
		c.answer(ctx, chat, func() (string, map[string]any) { return c.execute(user, "start"), nil })
	case telegram.IntentRestart:
		c.askConfirm(ctx, chat, user, telegramCommand{Name: "restart", Description: "Перезапустить бота", Action: "restart", Confirm: true})
	case telegram.IntentStop:
		c.askConfirm(ctx, chat, user, telegramCommand{Name: "stop", Description: "Остановить бота", Action: "stop", Confirm: true})
	case telegram.IntentUpdate:
		c.cmdUpdate(ctx, chat)
	case telegram.IntentLogs, telegram.IntentErrors, telegram.IntentSearch:
		c.showLog(ctx, chat, it)
	case telegram.IntentMute:
		c.cmdMute(ctx, chat, user, it)
	case telegram.IntentUnmute:
		c.cmdUnmute(ctx, chat, user)
	case telegram.IntentMenu:
		c.openMenu(ctx, chat)
	case telegram.IntentHelp:
		c.reply(ctx, chat, c.helpText(), nil)
	case telegram.IntentApp:
		if keyboard := c.appKeyboard(); keyboard != nil {
			c.reply(ctx, chat, "Мини-приложение панели:", keyboard)
		} else {
			c.reply(ctx, chat, "Мини-приложение не настроено: нужен публичный HTTPS-адрес панели в HKC_TELEGRAM_WEBAPP_URL. Всё остальное работает и без него — напишите «помощь».", nil)
		}
	case telegram.IntentHello:
		c.reply(ctx, chat, "Привет! Я на связи 👋 Напишите «меню» — открою главное меню с живым статусом. Все возможности — «помощь».", nil)
	case telegram.IntentThanks:
		c.reply(ctx, chat, "Пожалуйста! 🙂", nil)
	default:
		c.fallbackSearch(ctx, chat, it)
	}
}

// fallbackSearch — фраза не похожа на команду: ищем её в журнале, как обещано в справке.
func (c *telegramControl) fallbackSearch(ctx context.Context, chat int64, it telegram.Intent) {
	query := strings.TrimSpace(it.Text)
	if query == "" || len(query) > 120 {
		c.reply(ctx, chat, "Не понял 🤔 Напишите «помощь», чтобы увидеть, что я умею.", nil)
		return
	}
	c.answer(ctx, chat, func() (string, map[string]any) {
		v := &logView{mode: "search", text: query, limit: 15}
		nonce := c.registerView(v)
		text, keyboard := c.renderLogView(nonce, v)
		if v.before == 0 && keyboard != nil && strings.Contains(text, "Ничего не нашёл") {
			return "Не понял, что вы хотите, и в журнале «" + html.EscapeString(clip(query, 40)) + "» не встречается. Напишите «помощь» — покажу, что умею.", nil
		}
		return "<i>Не похоже на команду — поискал в журнале.</i>\n" + text, keyboard
	})
}

/* ---------- Подтверждение словом ---------- */

// resolveByWord отвечает на «да» и «нет» последнему вопросу этого человека. Так подтверждать
// можно и текстом, а не только кнопкой; старые вопросы не отвечаются (у кнопок тот же срок).
func (c *telegramControl) resolveByWord(ctx context.Context, chat int64, user telegram.User, kind telegram.IntentKind) {
	now := c.now()
	c.mu.Lock()
	var key string
	var latest telegramPending
	for k, p := range c.pending {
		if p.userID == user.ID && now.Before(p.expires) && (key == "" || p.expires.After(latest.expires)) {
			key, latest = k, p
		}
	}
	if key != "" {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	switch {
	case key == "" && kind == telegram.IntentYes:
		c.reply(ctx, chat, "Подтверждать нечего: вопросов в ожидании нет.", nil)
	case key == "":
		c.reply(ctx, chat, "Хорошо, ничего не делаю.", nil)
	case kind == telegram.IntentYes:
		c.answer(ctx, chat, func() (string, map[string]any) { return c.execute(user, latest.action), nil })
	default:
		c.reply(ctx, chat, "Хорошо, отменил.", nil)
	}
}

/* ---------- Статус ---------- */

// cmdStatus — «статус»: сводка с кнопками действий; кнопка «Обновить» правит это же сообщение.
func (c *telegramControl) cmdStatus(ctx context.Context, chat int64) {
	c.answer(ctx, chat, func() (string, map[string]any) { return c.statusView() })
}

func (c *telegramControl) statusView() (string, map[string]any) {
	return c.statusText(), c.statusKeyboard()
}

func (c *telegramControl) statusKeyboard() map[string]any {
	button := func(text, data string) map[string]any { return map[string]any{"text": text, "callback_data": data} }
	control := []map[string]any{button("▶ Запустить", "st:run")}
	if c.s.bot.PID() != 0 {
		control = []map[string]any{button("🔁 Перезапустить", "st:rs"), button("⏹ Остановить", "st:sp")}
	}
	rows := [][]map[string]any{
		{button("🔄 Обновить", "st:r"), button("📜 Логи", "st:l"), button("🔴 Ошибки", "st:e")},
		control,
	}
	if url := c.webAppURL(); url != "" {
		rows = append(rows, []map[string]any{{"text": "Открыть панель", "web_app": map[string]string{"url": url}}})
	}
	return map[string]any{"inline_keyboard": rows}
}

// recentProblems — ошибки и предупреждения за час и самые шумные модули по журналу.
func (c *telegramControl) recentProblems() (errs, warns int, noisy []string) {
	if !fileExists(c.s.bot.LogFile) && len(logfeed.Files(c.s.bot.LogFile)) == 0 {
		return 0, 0, nil
	}
	now := c.now()
	from, to := now.Add(-time.Hour).UnixMilli(), now.UnixMilli()+1
	all := logfeed.Collect(c.s.journal(), from, to, to-from, logfeed.Query{Hide: c.s.logRules.hider()})
	for _, b := range all.Buckets {
		errs += b.Err
		warns += b.Warn
	}
	if errs+warns == 0 {
		return 0, 0, nil
	}
	problem := logfeed.Collect(c.s.journal(), from, to, to-from, logfeed.Query{Levels: []string{"WARNING", "ERROR"}, Hide: c.s.logRules.hider()})
	for _, name := range topModuleNames(problem.Modules, 3) {
		noisy = append(noisy, fmt.Sprintf("%s ×%d", shortModule(name), problem.Modules[name]))
	}
	return errs, warns, noisy
}

func topModuleNames(counts map[string]int, n int) []string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	// Простая сортировка вставками: модулей единицы-десятки.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && (counts[names[j]] > counts[names[j-1]] || counts[names[j]] == counts[names[j-1]] && names[j] < names[j-1]); j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	if len(names) > n {
		names = names[:n]
	}
	return names
}

// statusText — сводка: процесс, ресурсы сервера, ошибки за час, автовосстановление и тишина.
func (c *telegramControl) statusText() string {
	var text strings.Builder
	pid := c.s.bot.PID()
	if pid != 0 {
		fmt.Fprintf(&text, "🟢 <b>Бот работает</b>\nPID %d · %s", pid, html.EscapeString(botproc.Uptime(pid)))
	} else {
		text.WriteString("🔴 <b>Бот остановлен</b>")
	}
	c.s.systemMu.RLock()
	system := c.s.latestSystem
	c.s.systemMu.RUnlock()
	if system.Supported && !system.SampledAt.IsZero() {
		fmt.Fprintf(&text, "\n\nCPU %.0f%% · память %.0f%% · диск %.0f%%", system.CPUPercent,
			hostPercent(system.MemoryUsed, system.MemoryTotal), hostPercent(system.DiskUsed, system.DiskTotal))
	}
	if errs, warns, noisy := c.recentProblems(); errs+warns > 0 {
		fmt.Fprintf(&text, "\nЗа час: %d ошибок · %d предупреждений", errs, warns)
		if len(noisy) > 0 {
			text.WriteString("\nШумят: " + html.EscapeString(strings.Join(noisy, ", ")))
		}
	} else if fileExists(c.s.bot.LogFile) {
		text.WriteString("\nЗа час ошибок нет ✅")
	}
	if c.s.operations != nil {
		state := "выключено"
		if c.s.operations.watchdogSettings().Enabled {
			state = "включено"
			if c.s.watchdog.snapshot().State == "suspended" {
				state = "приостановлено — бот падал раз за разом"
			}
		}
		text.WriteString("\nАвтовосстановление: " + state)
	}
	if mute, ok := c.mute.Active(); ok {
		note := "🔕 Уведомления выключены до " + mute.Until.In(c.now().Location()).Format("15:04")
		if !mute.All {
			note += " (критичные приходят)"
		}
		text.WriteString("\n" + note)
	}
	footer := "hrk-console " + currentVersion().Version
	if host, err := os.Hostname(); err == nil {
		footer = host + " · " + footer
	}
	text.WriteString("\n<i>" + html.EscapeString(footer) + " · " + c.now().Format("15:04:05") + "</i>")
	return text.String()
}

// handleStatusCallback — кнопки под статусом: st:<действие>.
func (c *telegramControl) handleStatusCallback(ctx context.Context, callback *telegram.Callback, act string, answer func(string)) {
	if callback.Message == nil {
		answer("Сообщение недоступно")
		return
	}
	chat, message := callback.Message.Chat.ID, callback.Message.MessageID
	switch act {
	case "r":
		answer("Обновляю…")
		text, keyboard := c.statusView()
		c.editMessage(ctx, chat, message, text, keyboard)
	case "l":
		answer("Журнал")
		c.showLog(ctx, chat, telegram.Intent{Kind: telegram.IntentLogs})
	case "e":
		answer("Ошибки")
		c.showLog(ctx, chat, telegram.Intent{Kind: telegram.IntentErrors, Level: "ERROR"})
	case "run":
		answer("Запускаю…")
		result := c.execute(callback.From, "start")
		text, keyboard := c.statusView()
		c.editMessage(ctx, chat, message, text+"\n\n"+result, keyboard)
	case "rs":
		answer("")
		c.askConfirm(ctx, chat, callback.From, telegramCommand{Name: "restart", Description: "Перезапустить бота", Action: "restart", Confirm: true})
	case "sp":
		answer("")
		c.askConfirm(ctx, chat, callback.From, telegramCommand{Name: "stop", Description: "Остановить бота", Action: "stop", Confirm: true})
	default:
		answer("Кнопка устарела")
	}
}

/* ---------- Тишина ---------- */

func (c *telegramControl) cmdMute(ctx context.Context, chat int64, user telegram.User, it telegram.Intent) {
	d := it.Mute
	if !it.MuteSet || d <= 0 {
		d = time.Hour
	}
	if d > muteMax {
		d = muteMax
	}
	until := c.now().Add(d)
	if err := c.mute.Set(until, it.MuteAll); err != nil {
		c.reply(ctx, chat, "⚠️ Не удалось сохранить тишину: "+html.EscapeString(err.Error()), nil)
		return
	}
	c.auditChat(user, "telegram.mute", fmt.Sprintf("до %s all=%t", until.UTC().Format(time.RFC3339), it.MuteAll))
	text := fmt.Sprintf("🔕 <b>Тишина на %s</b> — до %s", telegram.FormatMuteDuration(d), until.In(c.now().Location()).Format("15:04"))
	if it.MuteAll {
		text += "\nМолчу обо всём, даже о падении бота."
	} else {
		text += "\nКритичные события (бот упал) всё равно придут."
	}
	c.reply(ctx, chat, text, keyboardRows([]map[string]string{btn("🔔 Включить", "mu:off")}))
}

func (c *telegramControl) cmdUnmute(ctx context.Context, chat int64, user telegram.User) {
	_, was := c.mute.Active()
	if err := c.mute.Clear(); err != nil {
		c.reply(ctx, chat, "⚠️ Не удалось снять тишину: "+html.EscapeString(err.Error()), nil)
		return
	}
	if was {
		c.auditChat(user, "telegram.unmute", "")
		c.reply(ctx, chat, "🔔 <b>Уведомления снова включены</b>", nil)
		return
	}
	c.reply(ctx, chat, "Уведомления и так включены 🔔", nil)
}

func (c *telegramControl) handleMuteCallback(ctx context.Context, callback *telegram.Callback, act string, answer func(string)) {
	if act != "off" {
		answer("Кнопка устарела")
		return
	}
	if err := c.mute.Clear(); err != nil {
		answer("Не удалось")
		return
	}
	c.auditChat(callback.From, "telegram.unmute", "")
	answer("Включено")
	if callback.Message != nil {
		c.editMessage(ctx, callback.Message.Chat.ID, callback.Message.MessageID, "🔔 <b>Уведомления снова включены</b>", nil)
	}
}

// auditChat пишет в журнал действий то, что сделано из чата.
func (c *telegramControl) auditChat(user telegram.User, action, detail string) {
	if c.s.audit == nil {
		return
	}
	event := auditEvent{Time: c.now().UTC(), Actor: user.Actor(), Action: action, Detail: detail, IP: "telegram"}
	if err := c.s.audit.add(event); err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
	}
}

// handleIncidentCallback — «Что случилось» под уведомлением: rg:<начало>:<конец> в секундах.
func (c *telegramControl) handleIncidentCallback(ctx context.Context, callback *telegram.Callback, data string, answer func(string)) {
	a, b, ok := strings.Cut(data, ":")
	from, errA := strconv.ParseInt(a, 10, 64)
	to, errB := strconv.ParseInt(b, 10, 64)
	if !ok || errA != nil || errB != nil || callback.Message == nil || from <= 0 || to < from {
		answer("Кнопка устарела")
		return
	}
	answer("Журнал за этот период")
	c.showIncidentLog(ctx, callback.Message.Chat.ID, time.Unix(from, 0), time.Unix(to, 0))
}

/* ---------- Кнопки под уведомлениями об инцидентах ---------- */

// incidentMarkup — кнопки под уведомлением. Без включённого управления нажатие никто бы не
// обработал, поэтому сообщение идёт без кнопок.
func (c *telegramControl) incidentMarkup(key string, phase incidentPhase, from, to time.Time) map[string]any {
	if !c.canAct() {
		return nil
	}
	if phase == phaseResolve {
		if from.IsZero() || to.IsZero() {
			return nil
		}
		return keyboardRows([]map[string]string{btn("📜 Что случилось", fmt.Sprintf("rg:%d:%d", from.Unix(), to.Unix()))})
	}
	switch {
	case key == "bot":
		return keyboardRows([]map[string]string{btn("▶ Запустить", "ic:run"), btn("📜 Логи", "st:l")})
	case strings.HasPrefix(key, "module:"):
		return keyboardRows([]map[string]string{btn("🔴 Ошибки", "st:e"), btn("🔕 Тишина на час", "ic:mute")})
	default:
		return keyboardRows([]map[string]string{btn("📜 Логи", "st:l")})
	}
}

// handleIncidentAction — кнопки ic:<действие>. Они не правят само сообщение об инциденте: его
// обновит следующее событие (например, «Бот запущен»).
func (c *telegramControl) handleIncidentAction(ctx context.Context, callback *telegram.Callback, act string, answer func(string)) {
	switch act {
	case "run":
		answer("Запускаю…")
		c.answerAfter(ctx, callback, c.execute(callback.From, "start"))
	case "mute":
		if err := c.mute.Set(c.now().Add(time.Hour), false); err != nil {
			answer("Не удалось")
			return
		}
		c.auditChat(callback.From, "telegram.mute", "кнопка под уведомлением, 1 ч")
		answer("Тишина на час. Критичное всё равно придёт.")
	default:
		answer("Кнопка устарела")
	}
}

// answerAfter сообщает итог действия отдельным сообщением в том же чате.
func (c *telegramControl) answerAfter(ctx context.Context, callback *telegram.Callback, text string) {
	if callback.Message != nil {
		c.send(ctx, callback.Message.Chat.ID, text, nil)
	}
}
