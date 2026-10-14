package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"heroku-console/internal/telegram"
)

// Бот не оставляет человека без выбора. В личном чате под полем ввода всегда есть клавиатура
// быстрых действий, под каждым ответом — кнопки следующего шага, а на то, что бот выполнить не
// может (картинка, стикер, сообщение, пришедшее, пока панель была недоступна), он отвечает, а не
// молчит. Чужим и в группе без обращения по имени бот по-прежнему не отвечает: это защита, а не
// недосмотр.

const (
	chatKeyboardFile = "telegram-keyboard.json"
	// chatKeyboardVersion меняется вместе с раскладкой: тогда клавиатура отправляется заново.
	chatKeyboardVersion = 1
	// staleNoticeEvery — не чаще этого отвечаем на сообщения, пришедшие, пока панель была недоступна.
	staleNoticeEvery = 10 * time.Minute
)

// chatKeyboardRows — клавиатура под полем ввода. Кнопки отправляют обычные слова, поэтому их
// разбирает тот же разбор фраз, что и написанное вручную; перезапуск спрашивает подтверждение.
var chatKeyboardRows = [][]string{
	{"📋 Меню", "📊 Статус", "🔴 Ошибки"},
	{"📜 Логи", "🔁 Перезапуск", "🔕 Тишина"},
}

func chatKeyboard() map[string]any {
	rows := make([][]map[string]string, 0, len(chatKeyboardRows))
	for _, row := range chatKeyboardRows {
		buttons := make([]map[string]string, 0, len(row))
		for _, text := range row {
			buttons = append(buttons, map[string]string{"text": text})
		}
		rows = append(rows, buttons)
	}
	return map[string]any{"keyboard": rows, "is_persistent": true, "resize_keyboard": true,
		"input_field_placeholder": "Напишите словами или выберите кнопку"}
}

// chatKeyboardStore помнит, в какие чаты клавиатура уже отправлена. Хранится в файле: после
// перезапуска панели бот не присылает её снова.
type chatKeyboardStore struct {
	mu   sync.Mutex
	path string
	sent map[int64]int
}

func chatKeyboardFilePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, chatKeyboardFile)
}

func newChatKeyboardStore(path string) *chatKeyboardStore {
	k := &chatKeyboardStore{path: path, sent: map[int64]int{}}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &k.sent)
		}
	}
	return k
}

func (k *chatKeyboardStore) needs(chat int64) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sent[chat] != chatKeyboardVersion
}

func (k *chatKeyboardStore) mark(chat int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sent[chat] = chatKeyboardVersion
	if k.path == "" {
		return
	}
	data, err := json.Marshal(k.sent)
	if err != nil {
		return
	}
	tmp := k.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, k.path)
	}
}

// ensureChatKeyboard один раз присылает клавиатуру в личный чат. У сообщения может быть только
// одна клавиатура, а ответы бота несут кнопки следующего шага, поэтому это отдельное сообщение.
func (c *telegramControl) ensureChatKeyboard(ctx context.Context, chat int64) {
	if c.keyboard == nil || chat <= 0 || !c.keyboard.needs(chat) {
		return
	}
	payload := map[string]any{"chat_id": chat, "parse_mode": "HTML", "reply_markup": chatKeyboard(),
		"text": "⌨️ Внизу — быстрые кнопки: меню, статус, ошибки, логи, перезапуск и тишина. Можно и просто писать словами."}
	if err := c.api.Call(ctx, "sendMessage", payload, nil); err != nil {
		log.Printf("telegram: клавиатура не отправлена: %v", err)
		return
	}
	c.keyboard.mark(chat)
}

/* ---------- Кнопки следующего шага ---------- */

// nextSet — какие кнопки следующего шага показать.
type nextSet int

const (
	// nextGeneral — после справки, приветствия, непонятой фразы: всё основное.
	nextGeneral nextSet = iota
	// nextAfterAction — после запуска, остановки, отмены: проверить, что вышло.
	nextAfterAction
	// nextShort — после мелких ответов.
	nextShort
)

// nextKeyboard — кнопки nx:<что> под ответом. В группе меню не предлагаем: оно только в личном чате.
func (c *telegramControl) nextKeyboard(chat int64, set nextSet) map[string]any {
	status, logs, errs := btn("📊 Статус", "nx:s"), btn("📜 Логи", "nx:l"), btn("🔴 Ошибки", "nx:e")
	menu, help := btn("📋 Меню", "nx:m"), btn("❓ Помощь", "nx:h")
	var rows [][]map[string]string
	switch set {
	case nextAfterAction:
		rows = [][]map[string]string{{status, logs, menu}}
	case nextShort:
		rows = [][]map[string]string{{menu, status}}
	default:
		rows = [][]map[string]string{{status, logs, errs}, {menu, help}}
	}
	if chat <= 0 {
		for i, row := range rows {
			kept := row[:0:0]
			for _, b := range row {
				if b["callback_data"] != "nx:m" {
					kept = append(kept, b)
				}
			}
			rows[i] = kept
		}
	}
	return keyboardRows(rows...)
}

// withNext добавляет кнопки следующего шага к уже готовой клавиатуре ответа.
func (c *telegramControl) withNext(chat int64, keyboard map[string]any, set nextSet) map[string]any {
	next := c.nextKeyboard(chat, set)
	if keyboard == nil {
		return next
	}
	rows, ok := keyboard["inline_keyboard"].([][]map[string]string)
	if !ok {
		return keyboard
	}
	extra := next["inline_keyboard"].([][]map[string]string)
	merged := map[string]any{}
	for key, value := range keyboard {
		merged[key] = value
	}
	merged["inline_keyboard"] = append(append([][]map[string]string{}, rows...), extra...)
	return merged
}

// handleNextCallback — кнопки следующего шага: каждая отвечает новым сообщением, а сообщение
// с кнопками остаётся, как было.
func (c *telegramControl) handleNextCallback(ctx context.Context, callback *telegram.Callback, act string, answer func(string)) {
	if callback.Message == nil {
		answer("Сообщение недоступно")
		return
	}
	chat := callback.Message.Chat.ID
	switch act {
	case "s":
		answer("Статус")
		c.cmdStatus(ctx, chat)
	case "l":
		answer("Журнал")
		c.showLog(ctx, chat, telegram.Intent{Kind: telegram.IntentLogs})
	case "e":
		answer("Ошибки")
		c.showLog(ctx, chat, telegram.Intent{Kind: telegram.IntentErrors, Level: "ERROR"})
	case "m":
		answer("Меню")
		c.openMenu(ctx, chat)
	case "h":
		answer("Помощь")
		c.reply(ctx, chat, c.helpText(), c.nextKeyboard(chat, nextShort))
	default:
		answer("Кнопка устарела")
	}
}

/* ---------- Вместо молчания ---------- */

// replyUnsupported — пришло то, что бот не разбирает (картинка, стикер, голосовое).
func (c *telegramControl) replyUnsupported(ctx context.Context, chat int64) {
	c.reply(ctx, chat, "Я понимаю текст и кнопки. Напишите словами, что сделать, или выберите ниже.", c.nextKeyboard(chat, nextGeneral))
}

// noticeStale отвечает на сообщение, пришедшее, пока панель была недоступна или управление
// выключено. Выполнять его нельзя — человек мог уже сделать то же другим путём, и «перезапусти»
// сработало бы дважды, — но и молчать нельзя. Отвечаем раз на всю пачку таких сообщений.
func (c *telegramControl) noticeStale(ctx context.Context, chat int64) {
	now := c.now()
	c.mu.Lock()
	last, seen := c.staleNotice[chat]
	if seen && now.Sub(last) < staleNoticeEvery {
		c.mu.Unlock()
		return
	}
	c.staleNotice[chat] = now
	c.mu.Unlock()
	c.send(ctx, chat, "Пока я был недоступен, сюда пришли сообщения. Я их не выполняю, чтобы не сделать лишнего — например, не перезапустить бота второй раз. Что сделать сейчас?",
		c.nextKeyboard(chat, nextGeneral))
}
