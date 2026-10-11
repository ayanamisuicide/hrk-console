package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
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

// draftCounter даёт неповторяющиеся ненулевые идентификаторы черновиков: одинаковый
// draft_id анимирует обновления, разные — заменяют черновик без анимации.
var draftCounter atomic.Int64

func nextDraftID() int64 {
	return draftCounter.Add(1)<<8 | 1
}

// htmlPrefix возвращает начало HTML-текста длиной visible видимых символов. Открытые теги
// закрываются, а HTML-сущности (&amp;) не рвутся посередине: иначе Telegram отклонил бы
// черновик. toWord продлевает обрезку до конца слова, чтобы текст набирался словами.
func htmlPrefix(src string, visible int, toWord bool) string {
	var out strings.Builder
	var open []string
	count := 0
	i := 0
	extra := 0
	for i < len(src) {
		if count >= visible {
			if !toWord || extra > 24 {
				break
			}
		}
		c := src[i]
		if c == '<' {
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				break
			}
			tag := src[i : i+end+1]
			fields := strings.Fields(strings.Trim(tag, "<>/"))
			if len(fields) == 0 {
				out.WriteString(tag)
				i += end + 1
				continue
			}
			name := strings.ToLower(fields[0])
			if strings.HasPrefix(tag, "</") {
				for k := len(open) - 1; k >= 0; k-- {
					if open[k] == name {
						open = append(open[:k], open[k+1:]...)
						break
					}
				}
			} else if !strings.HasSuffix(tag, "/>") {
				open = append(open, name)
			}
			out.WriteString(tag)
			i += end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(src[i:])
		unit := size
		if c == '&' {
			if semi := strings.IndexByte(src[i:], ';'); semi > 0 && semi <= 10 {
				out.WriteString(src[i : i+semi+1])
				i += semi + 1
				count++
				if count > visible {
					extra++
				}
				continue
			}
		}
		if count >= visible && (r == ' ' || r == '\n') {
			break
		}
		out.WriteString(src[i : i+unit])
		i += unit
		count++
		if count > visible {
			extra++
		}
	}
	for k := len(open) - 1; k >= 0; k-- {
		out.WriteString("</" + open[k] + ">")
	}
	return out.String()
}

// htmlVisibleLen считает видимые символы HTML-текста: теги не в счёт, сущность — один символ.
func htmlVisibleLen(src string) int {
	count := 0
	for i := 0; i < len(src); {
		switch {
		case src[i] == '<':
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				return count
			}
			i += end + 1
		case src[i] == '&':
			if semi := strings.IndexByte(src[i:], ';'); semi > 0 && semi <= 10 {
				i += semi + 1
			} else {
				i++
			}
			count++
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			i += size
			count++
		}
	}
	return count
}

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
	err := c.api.call(ctx, "sendMessageDraft", payload, nil)
	if err == nil {
		return true
	}
	var apiErr *telegramAPIError
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
			_ = c.api.call(ctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "typing"}, nil)
		}
		return 0
	}
	id := nextDraftID()
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
	total := htmlVisibleLen(text)
	if c.canStream(chat) && total >= streamMinVisible {
		if draft == 0 {
			draft = nextDraftID()
		}
		steps := min(streamMaxSteps, max(streamMinSteps, total/streamStepChars))
		for i := 1; i < steps; i++ {
			if !c.draftCall(ctx, chat, draft, htmlPrefix(text, total*i/steps, true)) {
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
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.api.call(ctx, "sendMessage", payload, &sent); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			log.Printf("telegram: ответ не отправлен: %v", err)
		}
	}
	return sent.MessageID
}

// upload отправляет файл multipart-запросом (sendDocument): JSON такой запрос не умеет.
func (api *telegramAPI) upload(ctx context.Context, method string, fields map[string]string, field, filename string, data []byte) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := form.WriteField(key, value); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile(field, filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.base+"/bot"+api.token+"/"+method, &body)
	if err != nil {
		return errors.New("некорректный запрос к Telegram")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := api.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return redactURLError(err)
	}
	defer res.Body.Close()
	var answer struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&answer); err != nil {
		return &telegramAPIError{Code: res.StatusCode}
	}
	if !answer.OK {
		code := answer.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &telegramAPIError{Code: code, Description: strings.ReplaceAll(answer.Description, api.token, "***")}
	}
	return nil
}

// describeSize — размер для подписи к файлу.
func describeSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d Б", n)
	}
	return fmt.Sprintf("%.0f КБ", float64(n)/1024)
}
