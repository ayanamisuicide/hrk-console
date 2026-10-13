// Package telegram — независимая от панели часть работы с Telegram: клиент Bot API и типы
// обновлений, разбор обычных фраз («покажи ошибки», «тишина на час»), тишина уведомлений,
// безопасная обрезка HTML для ответов по частям и проверка подписи мини-приложения. Связка с
// состоянием панели (команды, меню, журнал, обновления) — в cmd/hkc-web.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client — вызовы Bot API. Токен входит в адрес, поэтому ошибки пересказываются без него.
type Client struct {
	Token string
	Base  string
	HTTP  *http.Client
}

// APIError — отказ Bot API: код ошибки Telegram (или HTTP) и описание без токена.
type APIError struct {
	Code        int
	Description string
}

// Error пересказывает отказ для журнала и ответа в админке.
func (e *APIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("Telegram %d: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("Telegram HTTP %d", e.Code)
}

// Call вызывает метод Bot API с JSON-телом и разбирает result, если он нужен.
func (api *Client) Call(ctx context.Context, method string, payload, result any) error {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.Base+"/bot"+api.Token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return errors.New("некорректный запрос к Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := api.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return redactURLError(err)
	}
	defer res.Body.Close()
	var answer struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&answer); err != nil {
		return &APIError{Code: res.StatusCode}
	}
	if !answer.OK {
		code := answer.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &APIError{Code: code, Description: strings.ReplaceAll(answer.Description, api.Token, "***")}
	}
	if result != nil {
		return json.Unmarshal(answer.Result, result)
	}
	return nil
}

// User — пользователь или бот Telegram.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// DisplayName — имя и фамилия, а без них числовой ID.
func (u User) DisplayName() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = strconv.FormatInt(u.ID, 10)
	}
	return name
}

// Actor — подпись в журнале действий: @username, иначе имя.
func (u User) Actor() string {
	if u.Username != "" {
		return "telegram:@" + u.Username
	}
	return "telegram:" + u.DisplayName()
}

// Chat — чат, из которого пришло сообщение.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message — входящее сообщение.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

// Callback — нажатие кнопки под сообщением.
type Callback struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update — одно событие из getUpdates.
type Update struct {
	UpdateID int64     `json:"update_id"`
	Message  *Message  `json:"message"`
	Callback *Callback `json:"callback_query"`
}

// Upload отправляет файл multipart-запросом (sendDocument): JSON такой запрос не умеет.
func (api *Client) Upload(ctx context.Context, method string, fields map[string]string, field, filename string, data []byte) error {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.Base+"/bot"+api.Token+"/"+method, &body)
	if err != nil {
		return errors.New("некорректный запрос к Telegram")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := api.HTTP.Do(req)
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
		return &APIError{Code: res.StatusCode}
	}
	if !answer.OK {
		code := answer.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &APIError{Code: code, Description: strings.ReplaceAll(answer.Description, api.Token, "***")}
	}
	return nil
}

// redactURLError убирает адрес из сетевой ошибки: в нём токен бота.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return errors.New("превышено время ожидания ответа")
		}
		return fmt.Errorf("сетевая ошибка: %v", urlErr.Err)
	}
	return err
}
