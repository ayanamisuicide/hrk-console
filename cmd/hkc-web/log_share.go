package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"heroku-console/logfeed"
)

// Запись журнала отправляется в Telegram из её карточки: из мини-приложения — себе в личный чат
// с ботом, из панели — в чат уведомлений. Текст берётся на сервере по ts и id записи, а не из
// запроса, и проходит ту же маскировку секретов, что и ответы бота.

const shareTextLimit = 3500

// shareChat — куда отправить запись этому пользователю; 0 — некуда.
func (s *server) shareChat(p principal) int64 {
	c := s.telegram
	if c == nil || c.api == nil || p.role != "operator" {
		return 0
	}
	if p.telegram != nil {
		return p.telegram.ID
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.config.ChatID), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func (s *server) shareLogEntry(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TS int64  `json:"ts"`
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil || input.ID == "" {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "не указана запись"})
		return
	}
	p, _ := requestPrincipal(r)
	chat := s.shareChat(p)
	if chat == 0 {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "отправлять некуда: Telegram-бот или чат уведомлений не настроены"})
		return
	}
	entries, index, ok := logfeed.AroundN(s.journal(), input.TS, input.ID, 0, 0)
	if !ok {
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "запись не найдена: возможно, файл уже ротирован"})
		return
	}
	entry := entries[index : index+1]
	maskEntries(entry, maskSecrets)
	c := s.telegram
	// Без включённого управления кнопки под сообщением некому обработать — шлём его без них.
	var keyboard map[string]any
	if !c.canAct() {
		keyboard = map[string]any{"inline_keyboard": [][]map[string]string{}}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if c.sendMessageID(ctx, chat, sharedEntryText(entry[0]), keyboard) == 0 {
		message := "Telegram не принял сообщение"
		if p.telegram != nil {
			message += ": напишите боту /start и повторите"
		}
		writeJSON(w, http.StatusBadGateway, actionResponse{Message: message})
		return
	}
	s.record(r, p.name, "log.share", fmt.Sprintf("%s %s", entry[0].Level, clip(entry[0].Message, 80)))
	where := "в чат уведомлений"
	if p.telegram != nil {
		where = "в чат с ботом"
	}
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: "запись отправлена " + where})
}

// sharedEntryText — запись журнала сообщением Telegram: уровень, модуль, время и текст с traceback.
func sharedEntryText(e logfeed.Entry) string {
	icon := "📜"
	switch e.Level {
	case "ERROR", "CRITICAL":
		icon = "🔴"
	case "WARNING":
		icon = "🟡"
	}
	head := icon + " <b>" + html.EscapeString(e.Level) + "</b>"
	if e.Module != "" {
		head += " · " + html.EscapeString(e.Module)
	}
	if e.Time != "" {
		head += " · " + html.EscapeString(e.Time)
	}
	body := strings.Join(append([]string{e.Message}, e.Trace...), "\n")
	if runes := []rune(body); len(runes) > shareTextLimit {
		body = string(runes[:shareTextLimit]) + "\n…"
	}
	return head + "\n<pre>" + html.EscapeString(body) + "</pre>"
}
