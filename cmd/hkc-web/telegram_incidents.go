package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Уведомления в Telegram как инциденты. Падение бота и его запуск — одно сообщение: оно
// появляется при падении с кнопками действий, а при восстановлении правится на «Бот запущен ·
// простоял 2 мин». Повторы одной и той же ошибки модуля не плодят сообщения, а увеличивают
// счётчик в первом (правка не звенит). Открытые инциденты хранятся в файле, поэтому перезапуск
// панели — например, при обновлении — не оставляет сообщение «висеть».

const (
	incidentFile   = "telegram-incidents.json"
	incidentMaxAge = 24 * time.Hour
	repeatWindow   = 30 * time.Minute
)

// openIncident — сообщение, которое ещё можно править.
type openIncident struct {
	Message int64     `json:"message"`
	Start   time.Time `json:"start"`
	Last    time.Time `json:"last"`
	Count   int       `json:"count"`
	Text    string    `json:"text"`
}

// incidentPhase — роль события в жизни инцидента.
type incidentPhase int

const (
	phasePlain   incidentPhase = iota // отдельное уведомление
	phaseOpen                         // что-то сломалось
	phaseResolve                      // починилось
	phaseRepeat                       // то же самое снова
)

// incidentKey определяет, к какому инциденту относится событие и какова его роль.
func incidentKey(event alertEvent) (string, incidentPhase) {
	switch {
	case event.Kind == "bot.stopped":
		return "bot", phaseOpen
	case event.Kind == "bot.started":
		return "bot", phaseResolve
	case strings.HasPrefix(event.Kind, "host.") && event.Severity == "critical":
		return event.Kind, phaseOpen
	case strings.HasPrefix(event.Kind, "host.") && event.Severity == "ok":
		return event.Kind, phaseResolve
	case event.Kind == "module.error":
		return "module:" + event.Title, phaseRepeat
	}
	return "", phasePlain
}

// request вызывает метод Bot API и возвращает result.
func (sink *telegramSink) request(ctx context.Context, method string, payload map[string]any) (json.RawMessage, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sink.apiBase+"/bot"+sink.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("некорректный запрос к Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := sink.client.Do(req)
	if err != nil {
		return nil, redactURLError(err)
	}
	defer res.Body.Close()
	var answer struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&answer)
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return answer.Result, nil
	}
	if answer.Description != "" {
		return nil, &telegramAPIError{Code: res.StatusCode, Description: strings.ReplaceAll(answer.Description, sink.token, "***")}
	}
	return nil, &telegramAPIError{Code: res.StatusCode}
}

func (sink *telegramSink) clockNow() time.Time {
	if sink.now != nil {
		return sink.now()
	}
	return time.Now()
}

// load читает открытые инциденты один раз и забывает устаревшие.
func (sink *telegramSink) load() {
	if sink.open != nil {
		return
	}
	sink.open = map[string]*openIncident{}
	if sink.incidentsPath == "" {
		return
	}
	data, err := os.ReadFile(sink.incidentsPath)
	if err != nil {
		return
	}
	var saved map[string]*openIncident
	if json.Unmarshal(data, &saved) != nil {
		return
	}
	now := sink.clockNow()
	for key, inc := range saved {
		if inc != nil && now.Sub(inc.Last) < incidentMaxAge {
			sink.open[key] = inc
		}
	}
}

func (sink *telegramSink) save() {
	if sink.incidentsPath == "" {
		return
	}
	data, err := json.Marshal(sink.open)
	if err != nil {
		return
	}
	tmp := sink.incidentsPath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, sink.incidentsPath)
	}
}

func incidentsFilePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, incidentFile)
}

// send доставляет событие: отдельным сообщением либо правкой уже показанного.
func (sink *telegramSink) send(ctx context.Context, event alertEvent) error {
	sink.flow.Lock()
	defer sink.flow.Unlock()
	sink.load()
	key, phase := incidentKey(event)
	switch phase {
	case phaseOpen:
		return sink.openIncident(ctx, key, event)
	case phaseResolve:
		return sink.resolveIncident(ctx, key, event)
	case phaseRepeat:
		return sink.repeatIncident(ctx, key, event)
	}
	return sink.deliverNew(ctx, event, nil)
}

// deliverNew отправляет новое сообщение и возвращает ошибку доставки. Приглушённые события
// не отправляются вовсе.
func (sink *telegramSink) deliverNew(ctx context.Context, event alertEvent, kept *openIncident) error {
	if sink.mute != nil && sink.mute.silences(event) {
		return nil
	}
	_, err := sink.post(ctx, event, kept)
	return err
}

// post отправляет сообщение с кнопками и возвращает его номер.
func (sink *telegramSink) post(ctx context.Context, event alertEvent, kept *openIncident) (int64, error) {
	payload := map[string]any{"chat_id": sink.chatID, "text": telegramText(event),
		"parse_mode": "HTML", "disable_web_page_preview": true}
	if keyboard := sink.keyboardFor(event); keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	result, err := sink.request(ctx, "sendMessage", payload)
	if err != nil {
		return 0, err
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(result, &sent)
	return sent.MessageID, nil
}

// keyboardFor собирает кнопки: «Обновить» у новой версии и действия у инцидентов.
func (sink *telegramSink) keyboardFor(event alertEvent) map[string]any {
	if sink.markup != nil {
		if keyboard := sink.markup(event); keyboard != nil {
			return keyboard
		}
	}
	if sink.incidentMarkup != nil {
		key, phase := incidentKey(event)
		if key != "" && phase != phaseResolve {
			return sink.incidentMarkup(key, phase, time.Time{}, time.Time{})
		}
	}
	return nil
}

func (sink *telegramSink) openIncident(ctx context.Context, key string, event alertEvent) error {
	now := event.Time
	if now.IsZero() {
		now = sink.clockNow()
	}
	if inc := sink.open[key]; inc != nil && inc.Message != 0 && now.Sub(inc.Last) < incidentMaxAge {
		// Событие повторилось, не дождавшись починки: не плодим сообщения.
		return sink.bump(ctx, key, inc, event, now)
	}
	if sink.mute != nil && sink.mute.silences(event) {
		sink.open[key] = &openIncident{Start: now, Last: now, Count: 1}
		sink.save()
		return nil
	}
	id, err := sink.post(ctx, event, nil)
	if err != nil {
		return err
	}
	sink.open[key] = &openIncident{Message: id, Start: now, Last: now, Count: 1, Text: telegramText(event)}
	sink.save()
	return nil
}

func (sink *telegramSink) repeatIncident(ctx context.Context, key string, event alertEvent) error {
	now := event.Time
	if now.IsZero() {
		now = sink.clockNow()
	}
	if inc := sink.open[key]; inc != nil && inc.Message != 0 && now.Sub(inc.Last) < repeatWindow {
		return sink.bump(ctx, key, inc, event, now)
	}
	if sink.mute != nil && sink.mute.silences(event) {
		return nil
	}
	id, err := sink.post(ctx, event, nil)
	if err != nil {
		return err
	}
	sink.open[key] = &openIncident{Message: id, Start: now, Last: now, Count: 1, Text: telegramText(event)}
	sink.save()
	return nil
}

// bump увеличивает счётчик в сообщении. Правка не присылает звукового уведомления, поэтому
// повторы не мешают.
func (sink *telegramSink) bump(ctx context.Context, key string, inc *openIncident, event alertEvent, now time.Time) error {
	inc.Count++
	inc.Last = now
	text := inc.Text + fmt.Sprintf("\n<b>×%d</b> · последний раз %s", inc.Count, now.In(time.Local).Format("15:04:05"))
	payload := map[string]any{"chat_id": sink.chatID, "message_id": inc.Message, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if keyboard := sink.keyboardFor(event); keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	_, err := sink.request(ctx, "editMessageText", payload)
	sink.save()
	if err != nil && !isNotModified(err) {
		// Сообщение удалено или слишком старое: следующее событие начнёт новое.
		delete(sink.open, key)
		sink.save()
		return err
	}
	return nil
}

func (sink *telegramSink) resolveIncident(ctx context.Context, key string, event alertEvent) error {
	inc := sink.open[key]
	delete(sink.open, key)
	sink.save()
	if inc == nil || inc.Message == 0 {
		return sink.deliverNew(ctx, event, nil)
	}
	end := event.Time
	if end.IsZero() {
		end = sink.clockNow()
	}
	label := "Длилось"
	if key == "bot" {
		label = "Простоял"
	}
	text := telegramText(event) + "\n⏱ " + label + " " + humanDuration(end.Sub(inc.Start))
	payload := map[string]any{"chat_id": sink.chatID, "message_id": inc.Message, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if sink.incidentMarkup != nil {
		if keyboard := sink.incidentMarkup(key, phaseResolve, inc.Start, end); keyboard != nil {
			payload["reply_markup"] = keyboard
		}
	}
	if payload["reply_markup"] == nil {
		// Без кнопок прежняя клавиатура снимается: «Запустить» под восстановленным ботом лишняя.
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{}}
	}
	if _, err := sink.request(ctx, "editMessageText", payload); err != nil && !isNotModified(err) {
		// Сообщение не удалось исправить — сообщаем о восстановлении новым.
		return sink.deliverNew(ctx, event, nil)
	}
	return nil
}

// humanDuration пишет длительность коротко и по-русски: «45 с», «2 мин 10 с», «3 ч 5 мин».
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с", int(d/time.Second))
	case d < 10*time.Minute:
		if s := int(d/time.Second) % 60; s > 0 {
			return fmt.Sprintf("%d мин %d с", int(d/time.Minute), s)
		}
		return fmt.Sprintf("%d мин", int(d/time.Minute))
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d/time.Minute))
	}
	return fmt.Sprintf("%d ч %d мин", int(d/time.Hour), int(d/time.Minute)%60)
}
