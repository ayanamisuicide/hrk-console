package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Обновление панели из Telegram: под уведомлением о новой версии (и по команде /update) появляется
// кнопка «Обновить». Установку выполняет та же отдельная root-служба, что и страница обновлений.
// Панель при этом перезапускается, поэтому ход установки запоминается в файле: новый процесс
// находит сообщение с кнопкой и сообщает в нём итог — «Обновлено» или причину неудачи.

const (
	telegramUpdateFile    = "telegram-update.json"
	telegramUpdatePoll    = 3 * time.Second
	telegramUpdateTimeout = 15 * time.Minute
	// Задание службы пишется чуть позже запроса, поэтому итог чуть старше запроса ещё считается нашим.
	telegramUpdateSlack = 5 * time.Second
)

// pendingUpdate — Установка, запущенная из Telegram и ещё не завершённая.
type pendingUpdate struct {
	Chat    int64     `json:"chat"`
	Message int64     `json:"message"`
	Version string    `json:"version"`
	Actor   string    `json:"actor"`
	At      time.Time `json:"at"`
}

func updateKeyboard(version string) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]string{{
		{"text": "⬆️ Обновить до " + version, "callback_data": "upd:" + version},
	}}}
}

// canAct — бот сейчас слушает команды, значит кнопка под сообщением сработает.
func (c *telegramControl) canAct() bool {
	c.mu.Lock()
	running := c.state == "running"
	c.mu.Unlock()
	return running && c.settings().ControlEnabled && len(c.admins) > 0
}

// attachUpdateButton подключает кнопку к уведомлениям Telegram. Без включённого управления
// кнопка бесполезна — нажатие никто бы не обработал, поэтому уведомление идёт без неё.
func (c *telegramControl) attachUpdateButton() {
	if c.s.alerts == nil {
		return
	}
	for _, sink := range c.s.alerts.sinks {
		if tg, ok := sink.(*telegramSink); ok {
			tg.markup = c.updateMarkup
		}
	}
}

func (c *telegramControl) updateMarkup(event alertEvent) map[string]any {
	if event.Kind != "update.available" || !releaseTag.MatchString(event.Version) || !c.canAct() {
		return nil
	}
	return updateKeyboard(event.Version)
}

// firstNote — главная строка заметок релиза для короткого сообщения.
func firstNote(notes []releaseNotes) string {
	if len(notes) > 0 && len(notes[0].Sections) > 0 && len(notes[0].Sections[0].Items) > 0 {
		return notes[0].Sections[0].Items[0]
	}
	return ""
}

// cmdUpdate — команда /update: свежая проверка и кнопка установки, если есть что ставить.
func (c *telegramControl) cmdUpdate(ctx context.Context, chat int64) {
	if c.s.updates == nil {
		c.send(ctx, chat, "Проверка обновлений на этом сервере не настроена.", nil)
		return
	}
	c.s.updates.check()
	// Проверка идёт в фоне (слишком частая просто не запускается); ждём её недолго, чтобы ответить свежими данными.
	for i := 0; i < 16 && c.s.updates.snapshot().Checking; i++ {
		time.Sleep(500 * time.Millisecond)
	}
	octx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	overview := c.s.buildUpdateOverview(octx)
	installed := html.EscapeString(overview.Installed.Version)
	switch {
	case overview.Running:
		c.send(ctx, chat, "⏳ <b>Обновление уже идёт</b>\nО результате сообщу здесь.", nil)
	case overview.Latest.Error != "" && overview.Latest.Version == "":
		c.send(ctx, chat, "⚠️ Не удалось проверить обновления: "+html.EscapeString(overview.Latest.Error), nil)
	case !overview.UpdateAvailable:
		c.send(ctx, chat, "✅ <b>Установлена последняя версия</b>\nhrk-console "+installed, nil)
	default:
		var text strings.Builder
		fmt.Fprintf(&text, "🔵 <b>Доступна hrk-console %s</b>\nУстановлена: %s", html.EscapeString(overview.Latest.Version), installed)
		if note := firstNote(overview.Releases); note != "" {
			text.WriteString("\n" + html.EscapeString(note))
		}
		if len(overview.Blockers) > 0 {
			blocker := overview.Blockers[0]
			text.WriteString("\n\n⚠️ " + html.EscapeString(blocker.Text+" "+blocker.Fix))
			c.send(ctx, chat, text.String(), nil)
			return
		}
		c.send(ctx, chat, text.String(), updateKeyboard(overview.Latest.Version))
	}
}

// editMessage правит сообщение с кнопкой; если его нельзя изменить, пишет новое.
func (c *telegramControl) editMessage(ctx context.Context, chat, message int64, text string, keyboard map[string]any) {
	if message != 0 {
		payload := map[string]any{"chat_id": chat, "message_id": message, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
		if keyboard != nil {
			payload["reply_markup"] = keyboard
		}
		if err := c.api.call(ctx, "editMessageText", payload, nil); err == nil {
			return
		}
	}
	c.send(ctx, chat, text, keyboard)
}

// handleUpdateCallback — нажатие «Обновить до vX».
func (c *telegramControl) handleUpdateCallback(ctx context.Context, callback *tgCallback, version string, answer func(string)) {
	if !releaseTag.MatchString(version) {
		answer("Некорректная версия")
		return
	}
	chat, message := callback.From.ID, int64(0)
	if callback.Message != nil {
		chat, message = callback.Message.Chat.ID, callback.Message.MessageID
	}
	if c.s.updates == nil {
		answer("Обновление не настроено")
		return
	}
	installed := c.installedVersion()
	if compareVersions(version, installed) <= 0 {
		answer("Уже установлена")
		c.editMessage(ctx, chat, message, "✅ <b>"+html.EscapeString(version)+" уже установлена</b>", nil)
		return
	}
	answer("Запускаю…")
	octx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, status := c.s.launchUpdate(octx, updateRequest{Action: "install", Version: version})
	c.auditUpdate(callback.From, version, result.Message, status)
	if status != http.StatusAccepted {
		text := "⚠️ <b>Не удалось начать обновление</b>\n" + html.EscapeString(capitalize(result.Message))
		// При конфликте с идущей установкой кнопка не нужна: ход виден в самом сообщении.
		if strings.Contains(result.Message, "уже выполняется") {
			c.editMessage(ctx, chat, message, text, nil)
			return
		}
		c.editMessage(ctx, chat, message, text, updateKeyboard(version))
		return
	}
	pending := pendingUpdate{Chat: chat, Message: message, Version: version, Actor: callback.From.actor(), At: c.now().UTC()}
	if err := c.savePending(pending); err != nil {
		log.Printf("telegram: не удалось запомнить обновление: %v", err)
	}
	c.editMessage(ctx, chat, message, "⏳ <b>Обновляю до "+html.EscapeString(version)+"…</b>\nПанель перезапустится — это займёт около минуты. Итог напишу здесь.", nil)
	go c.watchUpdate(pending)
}

func (c *telegramControl) auditUpdate(user tgUser, version, detail string, status int) {
	if c.s.audit == nil {
		return
	}
	if status == http.StatusAccepted {
		detail = version
	}
	event := auditEvent{Time: c.now().UTC(), Actor: user.actor(), Action: "update.install", Detail: detail, IP: "telegram"}
	if err := c.s.audit.add(event); err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
	}
}

func (c *telegramControl) installedVersion() string {
	if c.installed != nil {
		return c.installed()
	}
	return currentVersion().Version
}

func (c *telegramControl) pendingPath() string { return filepath.Join(c.s.dataDir, telegramUpdateFile) }

func (c *telegramControl) savePending(p pendingUpdate) error {
	data, _ := json.Marshal(p)
	if err := os.MkdirAll(filepath.Dir(c.pendingPath()), 0o700); err != nil {
		return err
	}
	return writePrivateAtomic(c.pendingPath(), data)
}

// resumePendingUpdate после перезапуска панели находит начатую установку и дописывает итог.
func (c *telegramControl) resumePendingUpdate() {
	data, err := os.ReadFile(c.pendingPath())
	if err != nil {
		return
	}
	var p pendingUpdate
	if json.Unmarshal(data, &p) != nil || !releaseTag.MatchString(p.Version) || c.now().Sub(p.At) > 2*telegramUpdateTimeout {
		_ = os.Remove(c.pendingPath())
		return
	}
	c.watchUpdate(p)
}

// telegramUpdateOutcome решает по версии и состоянию службы, чем закончилась установка:
// waiting — ещё идёт, ok — готово, failed — служба сообщила о неудаче, timeout — слишком долго.
func telegramUpdateOutcome(p pendingUpdate, installed string, job map[string]any, now time.Time) (state, text string) {
	phase, _ := job["phase"].(string)
	message, _ := job["message"].(string)
	updated, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(job["updatedAt"]))
	finished := phase == "complete" || phase == "failed" || phase == "rolled_back" || phase == "cancelled"
	ours := !updated.IsZero() && updated.After(p.At.Add(-telegramUpdateSlack))
	switch {
	case compareVersions(installed, p.Version) >= 0 && !jobRunning(job):
		return "ok", "✅ <b>Обновлено до " + html.EscapeString(p.Version) + "</b>\nПанель перезапущена и работает."
	case finished && ours && phase != "complete":
		text = "⚠️ <b>Не удалось обновить до " + html.EscapeString(p.Version) + "</b>"
		if message != "" {
			text += "\n" + html.EscapeString(message)
		}
		return "failed", text + "\nУстановлена версия " + html.EscapeString(installed) + ". Подробности — в разделе «Обновления» админки."
	case now.Sub(p.At) > telegramUpdateTimeout:
		return "timeout", "⏳ <b>Обновление идёт дольше обычного</b>\nПроверьте раздел «Обновления» в админке."
	}
	return "waiting", ""
}

// watchUpdate следит за установкой и пишет итог в сообщение с кнопкой. Параллельно для одной
// установки работает один наблюдатель.
func (c *telegramControl) watchUpdate(p pendingUpdate) {
	c.mu.Lock()
	if c.watching {
		c.mu.Unlock()
		return
	}
	c.watching = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.watching = false
		c.mu.Unlock()
	}()
	poll := c.updatePoll
	if poll <= 0 {
		poll = telegramUpdatePoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		state, text := telegramUpdateOutcome(p, c.installedVersion(), readUpdateJob(), c.now())
		if state != "waiting" {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			c.editMessage(ctx, p.Chat, p.Message, text, nil)
			cancel()
			_ = os.Remove(c.pendingPath())
			return
		}
		<-ticker.C
	}
}
