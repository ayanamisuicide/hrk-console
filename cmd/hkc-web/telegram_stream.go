package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"heroku-console/internal/telegram"
)

// Ответы бота появляются постепенно, как у нейросети: пока бот готовит ответ, Telegram
// показывает «Думаю…», затем текст набирается на глазах. Это метод sendMessageDraft: черновик
// существует только как предпросмотр и в чате не остаётся, поэтому в конце всегда отправляется
// обычное сообщение с полным текстом и кнопками. Работает только в личных чатах; в группах и
// при любой ошибке бот отвечает привычным сообщением, ничего не теряя.

const (
	streamMinVisible = 48  // короче — отправляем сразу: анимировать нечего
	streamMaxSteps   = 10  // не больше столько обновлений черновика на ответ
	streamMinSteps   = 4   // и не меньше, чтобы набор был заметен
	streamStepChars  = 42  // примерно столько видимых символов за шаг
	streamDefaultGap = 140 // миллисекунд между шагами
)

// canStream — черновики доступны: личный чат и Telegram их не отверг.
func (c *telegramControl) canStream(chat int64) bool {
	return chat > 0 && c.streamGap >= 0 && !c.draftOff.Load()
}

// draftCall отправляет один шаг черновика. Жёсткий отказ Telegram (метод не поддерживается
// этим сервером или чатом) отключает анимацию до перезапуска; временный — пропускает шаг.
func (c *telegramControl) draftCall(ctx context.Context, chat, draft int64, text string) bool {
	payload := map[string]any{"chat_id": chat, "draft_id": draft, "text": text}
	if text != "" {
		payload["parse_mode"] = "HTML"
	}
	err := c.api.Call(ctx, "sendMessageDraft", payload, nil)
	if err == nil {
		return true
	}
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == http.StatusBadRequest || apiErr.Code == http.StatusNotFound || apiErr.Code == http.StatusMethodNotAllowed) {
		if c.draftOff.CompareAndSwap(false, true) {
			log.Printf("telegram: анимация ответов отключена (%v); ответы идут обычными сообщениями", err)
		}
	}
	return false
}

// think показывает в чате «Думаю…» на время подготовки ответа. Возвращает идентификатор
// черновика для последующего набора текста или 0, если анимация недоступна.
func (c *telegramControl) think(ctx context.Context, chat int64) int64 {
	if !c.canStream(chat) {
		if chat > 0 {
			_ = c.api.Call(ctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "typing"}, nil)
		}
		return 0
	}
	id := telegram.NextDraftID()
	if !c.draftCall(ctx, chat, id, "") {
		return 0
	}
	return id
}

func (c *telegramControl) streamPause(ctx context.Context) bool {
	gap := c.streamGap
	if gap == 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(gap)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// streamTo набирает текст в черновике draft (0 — новый), а затем отправляет итоговое сообщение.
func (c *telegramControl) streamTo(ctx context.Context, chat, draft int64, text string, keyboard map[string]any) int64 {
	total := telegram.HTMLVisibleLen(text)
	if c.canStream(chat) && total >= streamMinVisible {
		if draft == 0 {
			draft = telegram.NextDraftID()
		}
		steps := min(streamMaxSteps, max(streamMinSteps, total/streamStepChars))
		for i := 1; i < steps; i++ {
			if !c.draftCall(ctx, chat, draft, telegram.HTMLPrefix(text, total*i/steps, true)) {
				break
			}
			if !c.streamPause(ctx) {
				break
			}
		}
	}
	return c.sendMessageID(ctx, chat, text, keyboard)
}

// reply отвечает в чат с набором текста.
func (c *telegramControl) reply(ctx context.Context, chat int64, text string, keyboard map[string]any) {
	c.streamTo(ctx, chat, 0, text, keyboard)
}

// answer показывает «Думаю…», пока work готовит ответ, и затем набирает его. Так долгие
// действия — проверка обновлений, чтение журнала — не выглядят зависанием.
func (c *telegramControl) answer(ctx context.Context, chat int64, work func() (string, map[string]any)) {
	draft := c.think(ctx, chat)
	text, keyboard := work()
	c.streamTo(ctx, chat, draft, text, keyboard)
}

// sendMessageID отправляет сообщение и возвращает его номер (0, если Telegram его не вернул).
func (c *telegramControl) sendMessageID(ctx context.Context, chat int64, text string, keyboard map[string]any) int64 {
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	// Ответ без кнопок оставил бы человека без выбора.
	if keyboard == nil {
		keyboard = c.nextKeyboard(chat, nextShort)
	}
	payload["reply_markup"] = keyboard
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.api.Call(ctx, "sendMessage", payload, &sent); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			log.Printf("telegram: ответ не отправлен: %v", err)
		}
	}
	return sent.MessageID
}

// describeSize — размер для подписи к файлу.
func describeSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d Б", n)
	}
	return fmt.Sprintf("%.0f КБ", float64(n)/1024)
}
