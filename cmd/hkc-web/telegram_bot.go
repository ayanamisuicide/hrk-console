package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"heroku-console/botproc"
)

// Управление ботом из Telegram. Панель сама опрашивает Bot API (getUpdates), поэтому открытый
// порт не нужен. Команды принимаются только от ID из HKC_TELEGRAM_ADMIN_IDS и только когда
// управление включено в админке; остальным бот не отвечает. Опасные действия подтверждаются
// кнопкой под сообщением. Команды — основа: они работают и тогда, когда мини-приложение недоступно.

// telegramSettings — Переключатели раздела «Telegram» в админке. Управление по умолчанию выключено:
// опрос getUpdates мешает другой программе, если она слушает того же бота.
type telegramSettings struct {
	ControlEnabled bool `json:"controlEnabled"`
	Confirm        bool `json:"confirm"`
}

func defaultTelegramSettings() telegramSettings { return telegramSettings{Confirm: true} }

// telegramCommand — Команда бота. Action — действие над процессом; Confirm — нужна ли кнопка подтверждения.
type telegramCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Action      string `json:"action,omitempty"`
	Confirm     bool   `json:"confirm"`
}

// Один список питает ответ /help, меню команд в Telegram и раздел админки.
var telegramCommands = []telegramCommand{
	{Name: "status", Description: "Состояние бота и сервера"},
	{Name: "run", Description: "Запустить бота", Action: "start"},
	{Name: "restart", Description: "Перезапустить бота", Action: "restart", Confirm: true},
	{Name: "stop", Description: "Остановить бота", Action: "stop", Confirm: true},
	{Name: "update", Description: "Проверить обновление панели"},
	{Name: "app", Description: "Открыть мини-приложение"},
	{Name: "help", Description: "Список команд"},
}

const (
	telegramPollSeconds  = 25
	telegramStaleCommand = 2 * time.Minute
	telegramConfirmTTL   = time.Minute
	telegramUnknownLimit = 5
)

// telegramAPI — Вызовы Bot API. Токен входит в адрес, поэтому ошибки пересказываются без него.
type telegramAPI struct {
	token  string
	base   string
	client *http.Client
}

type telegramAPIError struct {
	Code        int
	Description string
}

func (e *telegramAPIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("Telegram %d: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("Telegram HTTP %d", e.Code)
}

func (api *telegramAPI) call(ctx context.Context, method string, payload, result any) error {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.base+"/bot"+api.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return errors.New("некорректный запрос к Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := api.client.Do(req)
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
		return &telegramAPIError{Code: res.StatusCode}
	}
	if !answer.OK {
		code := answer.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &telegramAPIError{Code: code, Description: strings.ReplaceAll(answer.Description, api.token, "***")}
	}
	if result != nil {
		return json.Unmarshal(answer.Result, result)
	}
	return nil
}

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u tgUser) displayName() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = strconv.FormatInt(u.ID, 10)
	}
	return name
}

// actor — подпись в журнале действий: @username, иначе имя.
func (u tgUser) actor() string {
	if u.Username != "" {
		return "telegram:@" + u.Username
	}
	return "telegram:" + u.displayName()
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      tgChat  `json:"chat"`
	Date      int64   `json:"date"`
	Text      string  `json:"text"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Callback *tgCallback `json:"callback_query"`
}

// telegramSender — Кто писал боту без доступа: помогает узнать свой ID при настройке. Текст не хранится.
type telegramSender struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Username string    `json:"username,omitempty"`
	Chat     string    `json:"chat"`
	At       time.Time `json:"at"`
}

type telegramPending struct {
	action  string
	userID  int64
	expires time.Time
}

// telegramControl — Опрос Bot API и обработка команд. Состояние читает раздел админки.
type telegramControl struct {
	s      *server
	api    *telegramAPI
	config telegramConfig
	admins map[int64]bool
	now    func() time.Time
	wake   chan struct{}

	mu         sync.Mutex
	state      string
	err        string
	bot        *tgUser
	lastPoll   time.Time
	unknown    []telegramSender
	pending    map[string]telegramPending
	offset     int64
	cancelPoll context.CancelFunc
	web        telegramWebSessions
	// Обновление из Telegram: наблюдатель за установкой и подмены для тестов.
	watching   bool
	updatePoll time.Duration
	installed  func() string
}

func newTelegramControl(s *server, config telegramConfig) *telegramControl {
	c := &telegramControl{s: s, config: config, admins: map[int64]bool{}, now: time.Now,
		wake: make(chan struct{}, 1), pending: map[string]telegramPending{}, state: "off"}
	for _, id := range config.Admins {
		c.admins[id] = true
	}
	if config.Token != "" {
		// Длинный опрос держит соединение до telegramPollSeconds; запас покрывает сеть.
		c.api = &telegramAPI{token: config.Token, base: config.APIBase, client: &http.Client{Timeout: (telegramPollSeconds + 15) * time.Second}}
		c.state = "disabled"
	}
	return c
}

func (c *telegramControl) settings() telegramSettings {
	if c.s.operations == nil {
		return defaultTelegramSettings()
	}
	return c.s.operations.telegramSettings()
}

func (c *telegramControl) setState(state, message string) {
	c.mu.Lock()
	c.state, c.err = state, message
	c.mu.Unlock()
}

// reload прерывает текущий опрос, чтобы цикл перечитал настройки.
func (c *telegramControl) reload() {
	c.mu.Lock()
	if c.cancelPoll != nil {
		c.cancelPoll()
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// waitWake ждёт смены настроек или паузы; false — служба останавливается.
func (c *telegramControl) waitWake(ctx context.Context, pause time.Duration) bool {
	var timer <-chan time.Time
	if pause > 0 {
		t := time.NewTimer(pause)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-c.wake:
	case <-timer:
	}
	return true
}

// run — цикл жизни опроса: выключено → ждём; ошибка сети → пауза с ростом; конфликт и
// отказ токена → ждём, пока администратор что-то поменяет, а не спорим с другой программой.
func (c *telegramControl) run(ctx context.Context) {
	if c.api == nil {
		return
	}
	// После перезапуска панели дописываем итог начатого из Telegram обновления; управление для этого не нужно.
	go c.resumePendingUpdate()
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		if !c.settings().ControlEnabled {
			c.setState("disabled", "")
			if !c.waitWake(ctx, 0) {
				return
			}
			continue
		}
		c.setState("connecting", "")
		pollCtx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.cancelPoll = cancel
		c.mu.Unlock()
		err := c.session(pollCtx, func() { backoff = 5 * time.Second })
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil || errors.Is(err, context.Canceled) {
			continue
		}
		var apiErr *telegramAPIError
		if errors.As(err, &apiErr) {
			switch {
			case apiErr.Code == http.StatusConflict:
				message := "Этого бота уже опрашивает другая программа — например, инлайн-бот Heroku. Создайте для панели отдельного бота у @BotFather."
				if strings.Contains(strings.ToLower(apiErr.Description), "webhook") {
					message = "У этого бота настроен webhook, поэтому панель не может получать команды. Создайте для панели отдельного бота у @BotFather."
				}
				c.setState("conflict", message)
				log.Printf("telegram: %s", message)
				if !c.waitWake(ctx, 0) {
					return
				}
				continue
			case apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusNotFound:
				c.setState("error", "Telegram отклонил токен бота. Проверьте HKC_TELEGRAM_BOT_TOKEN.")
				if !c.waitWake(ctx, 0) {
					return
				}
				continue
			}
		}
		c.setState("error", err.Error())
		if !c.waitWake(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// session — одно подключение: знакомимся с ботом, публикуем меню команд и слушаем обновления.
func (c *telegramControl) session(ctx context.Context, connected func()) error {
	var me tgUser
	if err := c.api.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	c.mu.Lock()
	c.bot = &me
	c.mu.Unlock()
	c.publishMenu(ctx)
	c.setState("running", "")
	connected()
	for {
		c.mu.Lock()
		offset := c.offset
		c.mu.Unlock()
		var updates []tgUpdate
		err := c.api.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": telegramPollSeconds,
			"allowed_updates": []string{"message", "callback_query"}}, &updates)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.lastPoll = c.now()
		for _, update := range updates {
			if update.UpdateID >= c.offset {
				c.offset = update.UpdateID + 1
			}
		}
		c.mu.Unlock()
		for _, update := range updates {
			c.handleUpdate(update)
		}
	}
}

// publishMenu показывает команды в меню Telegram, а администраторам — кнопку мини-приложения.
// Ошибки не мешают работе: команды можно набрать и вручную.
func (c *telegramControl) publishMenu(ctx context.Context) {
	commands := []map[string]string{}
	for _, command := range telegramCommands {
		if command.Name == "app" && c.webAppURL() == "" {
			continue
		}
		commands = append(commands, map[string]string{"command": command.Name, "description": command.Description})
	}
	if err := c.api.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil); err != nil && ctx.Err() == nil {
		log.Printf("telegram: меню команд не опубликовано: %v", err)
	}
	url := c.webAppURL()
	for id := range c.admins {
		button := map[string]any{"type": "commands"}
		if url != "" {
			button = map[string]any{"type": "web_app", "text": "Панель", "web_app": map[string]string{"url": url}}
		}
		// Личный чат администратора совпадает с его ID; если он ещё не писал боту, Telegram ответит ошибкой.
		_ = c.api.call(ctx, "setChatMenuButton", map[string]any{"chat_id": id, "menu_button": button}, nil)
	}
}

// webAppURL — публичный HTTPS-адрес мини-приложения или пустая строка.
func (c *telegramControl) webAppURL() string {
	if c.config.WebAppURL == "" {
		return ""
	}
	return c.config.WebAppURL + "/tg/"
}

func (c *telegramControl) handleUpdate(update tgUpdate) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch {
	case update.Message != nil:
		c.handleMessage(ctx, update.Message)
	case update.Callback != nil:
		c.handleCallback(ctx, update.Callback)
	}
}

func (c *telegramControl) rememberUnknown(user tgUser, chat string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := strconv.FormatInt(user.ID, 10)
	kept := []telegramSender{{ID: id, Name: user.displayName(), Username: user.Username, Chat: chat, At: c.now().UTC()}}
	for _, sender := range c.unknown {
		if sender.ID != id && len(kept) < telegramUnknownLimit {
			kept = append(kept, sender)
		}
	}
	c.unknown = kept
}

func (c *telegramControl) handleMessage(ctx context.Context, message *tgMessage) {
	if message.From == nil || message.From.IsBot {
		return
	}
	if !c.admins[message.From.ID] {
		// Чужим не отвечаем: бот не должен подтверждать, что он чем-то управляет.
		c.rememberUnknown(*message.From, message.Chat.Type)
		return
	}
	// Команды, пришедшие пока управление было выключено или панель лежала, не исполняются.
	if c.now().Sub(time.Unix(message.Date, 0)) > telegramStaleCommand {
		return
	}
	text := strings.TrimSpace(message.Text)
	private := message.Chat.Type == "private"
	if !strings.HasPrefix(text, "/") {
		if private {
			c.send(ctx, message.Chat.ID, "Команды — /help.", nil)
		}
		return
	}
	name := strings.TrimPrefix(strings.Fields(text)[0], "/")
	if at := strings.IndexByte(name, '@'); at >= 0 {
		c.mu.Lock()
		bot := c.bot
		c.mu.Unlock()
		// В группе команда может адресоваться другому боту.
		if bot != nil && !strings.EqualFold(name[at+1:], bot.Username) {
			return
		}
		name = name[:at]
	}
	name = strings.ToLower(name)
	switch name {
	case "start", "help":
		c.send(ctx, message.Chat.ID, c.helpText(), nil)
		return
	case "status":
		c.send(ctx, message.Chat.ID, c.statusText(), c.appKeyboard())
		return
	case "update":
		c.cmdUpdate(ctx, message.Chat.ID)
		return
	case "app":
		if keyboard := c.appKeyboard(); keyboard != nil {
			c.send(ctx, message.Chat.ID, "Мини-приложение панели:", keyboard)
		} else {
			c.send(ctx, message.Chat.ID, "Мини-приложение не настроено: нужен публичный HTTPS-адрес панели в HKC_TELEGRAM_WEBAPP_URL. Команды работают и без него — /help.", nil)
		}
		return
	}
	for _, command := range telegramCommands {
		if command.Name != name || command.Action == "" {
			continue
		}
		if command.Confirm && c.settings().Confirm {
			c.askConfirm(ctx, message.Chat.ID, *message.From, command)
		} else {
			c.send(ctx, message.Chat.ID, c.execute(*message.From, command.Action), nil)
		}
		return
	}
	c.send(ctx, message.Chat.ID, "Не знаю команду /"+html.EscapeString(name)+". Список — /help.", nil)
}

// appKeyboard — кнопка открытия мини-приложения под сообщением, если оно настроено.
func (c *telegramControl) appKeyboard() map[string]any {
	url := c.webAppURL()
	if url == "" {
		return nil
	}
	return map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "Открыть панель", "web_app": map[string]string{"url": url}}}}}
}

func (c *telegramControl) askConfirm(ctx context.Context, chat int64, user tgUser, command telegramCommand) {
	nonce, err := randomToken(9)
	if err != nil {
		c.send(ctx, chat, "Не удалось подготовить подтверждение, попробуйте ещё раз.", nil)
		return
	}
	now := c.now()
	c.mu.Lock()
	for key, pending := range c.pending {
		if now.After(pending.expires) {
			delete(c.pending, key)
		}
	}
	c.pending[nonce] = telegramPending{action: command.Action, userID: user.ID, expires: now.Add(telegramConfirmTTL)}
	c.mu.Unlock()
	question := map[string]string{"restart": "Перезапустить бота?", "stop": "Остановить бота?"}[command.Action]
	if question == "" {
		question = command.Description + "?"
	}
	if command.Action == "stop" && c.s.operations != nil && c.s.operations.watchdogSettings().Enabled {
		question += "\n<i>Автовосстановление включено и через время запустит бота снова. Чтобы бот не поднимался, выключите его в админке.</i>"
	}
	accept := map[string]string{"restart": "🔄 Перезапустить", "stop": "⏹ Остановить"}[command.Action]
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{{
		{"text": accept, "callback_data": "ok:" + nonce},
		{"text": "Отмена", "callback_data": "no:" + nonce},
	}}}
	c.send(ctx, chat, question, keyboard)
}

func (c *telegramControl) handleCallback(ctx context.Context, callback *tgCallback) {
	answer := func(text string) {
		_ = c.api.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback.ID, "text": text}, nil)
	}
	if !c.admins[callback.From.ID] {
		c.rememberUnknown(callback.From, "callback")
		answer("Нет доступа")
		return
	}
	kind, nonce, _ := strings.Cut(callback.Data, ":")
	if kind == "upd" {
		c.handleUpdateCallback(ctx, callback, nonce, answer)
		return
	}
	c.mu.Lock()
	pending, ok := c.pending[nonce]
	delete(c.pending, nonce)
	c.mu.Unlock()
	edit := func(text string) {
		if callback.Message == nil {
			return
		}
		_ = c.api.call(ctx, "editMessageText", map[string]any{"chat_id": callback.Message.Chat.ID, "message_id": callback.Message.MessageID,
			"text": text, "parse_mode": "HTML"}, nil)
	}
	if !ok || c.now().After(pending.expires) {
		answer("Кнопка устарела")
		edit("Кнопка устарела — отправьте команду ещё раз.")
		return
	}
	if kind != "ok" {
		answer("Отменено")
		edit("Отменено.")
		return
	}
	answer("Выполняю…")
	edit(c.execute(callback.From, pending.action))
}

// execute выполняет действие тем же путём, что и кнопки панели, и пишет его в журнал действий.
func (c *telegramControl) execute(user tgUser, action string) string {
	result, status := c.s.performAction(action)
	if status == http.StatusOK && c.s.notifier != nil {
		c.s.notifier.observe(c.s.bot.PID() != 0)
	}
	if c.s.audit != nil {
		event := auditEvent{Time: c.now().UTC(), Actor: user.actor(), Action: "bot." + action, Detail: result.Message, IP: "telegram"}
		if err := c.s.audit.add(event); err != nil {
			fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		}
	}
	mark := "✅"
	if !result.OK {
		mark = "⚠️"
	}
	text := mark + " " + html.EscapeString(capitalize(result.Message))
	if result.PID != 0 {
		text += fmt.Sprintf("\nPID %d", result.PID)
	}
	return text
}

func capitalize(text string) string {
	for index, letter := range text {
		return strings.ToUpper(string(letter)) + text[index+len(string(letter)):]
	}
	return text
}

func (c *telegramControl) helpText() string {
	var text strings.Builder
	text.WriteString("<b>Управление Heroku</b>\n")
	for _, command := range telegramCommands {
		if command.Name == "app" && c.webAppURL() == "" {
			continue
		}
		fmt.Fprintf(&text, "\n/%s — %s", command.Name, html.EscapeString(strings.ToLower(command.Description[:1])+command.Description[1:]))
		if command.Confirm && c.settings().Confirm {
			text.WriteString(" (с подтверждением)")
		}
	}
	return text.String()
}

// statusText — короткая сводка: процесс, ресурсы сервера и автовосстановление.
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
	footer := "hrk-console " + currentVersion().Version
	if host, err := os.Hostname(); err == nil {
		footer = host + " · " + footer
	}
	text.WriteString("\n<i>" + html.EscapeString(footer) + "</i>")
	return text.String()
}

func (c *telegramControl) send(ctx context.Context, chat int64, text string, keyboard map[string]any) {
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	if err := c.api.call(ctx, "sendMessage", payload, nil); err != nil {
		log.Printf("telegram: ответ не отправлен: %v", err)
	}
}

// telegramView — Состояние для раздела админки. Токен в ответ не попадает.
func (c *telegramControl) view() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	admins := make([]string, 0, len(c.config.Admins))
	for _, id := range c.config.Admins {
		admins = append(admins, strconv.FormatInt(id, 10))
	}
	view := map[string]any{
		"token": c.config.Token != "", "tokenIssue": c.config.TokenIssue,
		"chat": c.config.ChatID != "", "chatIssue": c.config.ChatIssue,
		"admins": admins, "adminsIssue": c.config.AdminsIssue,
		"webApp": c.webAppURL(), "webAppIssue": c.config.WebAppIssue,
		"state": c.state, "error": c.err, "commands": telegramCommands,
		"unknown": append([]telegramSender{}, c.unknown...),
	}
	if c.bot != nil {
		view["bot"] = map[string]any{"id": strconv.FormatInt(c.bot.ID, 10), "username": c.bot.Username, "name": c.bot.displayName()}
	}
	if !c.lastPoll.IsZero() {
		view["lastPollAt"] = c.lastPoll.UTC()
	}
	return view
}

// telegramActivity — последние действия из Telegram по журналу действий.
func (s *server) telegramActivity(limit int) []auditEvent {
	result := []auditEvent{}
	if s.audit == nil {
		return result
	}
	events, err := s.audit.recent(auditRetention)
	if err != nil {
		return result
	}
	for _, event := range events {
		if strings.HasPrefix(event.Actor, "telegram:") || event.Action == "telegram.settings" {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result
}

func (s *server) getTelegram(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	control := s.telegram
	if control == nil {
		control = newTelegramControl(s, telegramConfig{})
	}
	view := control.view()
	settings := control.settings()
	view["settings"] = settings
	view["activity"] = s.telegramActivity(15)
	view["notifications"] = s.alerts.channels()["telegram"]
	writeJSON(w, http.StatusOK, view)
}

func (s *server) setTelegram(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperations(w, r) {
		return
	}
	settings := s.operations.telegramSettings()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректные настройки Telegram"})
		return
	}
	if err := s.operations.setTelegram(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	if s.telegram != nil {
		s.telegram.reload()
	}
	s.record(r, "admin", "telegram.settings", fmt.Sprintf("control=%t confirm=%t", settings.ControlEnabled, settings.Confirm))
	s.getTelegram(w, r)
}
