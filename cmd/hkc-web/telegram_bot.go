package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"heroku-console/internal/telegram"
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
	// DigestOff выключает утреннюю сводку. Поле отрицательное: у файлов настроек, сохранённых до
	// появления сводки, она тоже включена.
	DigestOff bool `json:"digestOff"`
	// DigestAt — время утренней сводки «ЧЧ:ММ» по часам сервера; пусто — 08:00.
	DigestAt string `json:"digestAt,omitempty"`
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
	{Name: "menu", Description: "Главное меню"},
	{Name: "status", Description: "Состояние бота и сервера"},
	{Name: "logs", Description: "Последние записи журнала"},
	{Name: "errors", Description: "Что упало: ошибки, сгруппированные"},
	{Name: "digest", Description: "Сводка: что было за сутки"},
	{Name: "find", Description: "Найти в журнале"},
	{Name: "mute", Description: "Выключить уведомления на время"},
	{Name: "unmute", Description: "Включить уведомления"},
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
	api    *telegram.Client
	config telegramConfig
	admins map[int64]bool
	now    func() time.Time
	wake   chan struct{}

	mu         sync.Mutex
	state      string
	err        string
	bot        *telegram.User
	lastPoll   time.Time
	unknown    []telegramSender
	pending    map[string]telegramPending
	offset     int64
	cancelPoll context.CancelFunc
	web        telegram.WebSessions
	// Обновление из Telegram: наблюдатель за установкой и подмены для тестов.
	watching   bool
	updatePoll time.Duration
	installed  func() string
	// Анимированные ответы: пауза между шагами (отрицательная — без анимации) и признак того,
	// что Telegram отверг черновики.
	streamGap time.Duration
	draftOff  atomic.Bool
	// Просмотры журнала в чате, к которым привязаны кнопки «Старее», «Файлом» и т. п.
	views map[string]*logView
	// Тишина уведомлений.
	mute *telegram.MuteStore
	// Главное меню: сообщение-дашборд, которое бот сам обновляет.
	menu *menuStore
	// Клавиатура под полем ввода: в какие чаты она уже отправлена (nil — не отправлять).
	keyboard *chatKeyboardStore
	// Когда последний раз отвечали на сообщения, пришедшие, пока панель была недоступна.
	staleNotice map[int64]time.Time
}

func newTelegramControl(s *server, config telegramConfig) *telegramControl {
	c := &telegramControl{s: s, config: config, admins: map[int64]bool{}, now: time.Now,
		wake: make(chan struct{}, 1), pending: map[string]telegramPending{}, state: "off",
		streamGap: streamDefaultGap * time.Millisecond, views: map[string]*logView{}}
	c.mute = telegram.NewMuteStore(muteFilePath(s.dataDir), c.now)
	c.menu = newMenuStore(menuFilePath(s.dataDir))
	c.keyboard = newChatKeyboardStore(chatKeyboardFilePath(s.dataDir))
	c.staleNotice = map[int64]time.Time{}
	for _, id := range config.Admins {
		c.admins[id] = true
	}
	if config.Token != "" {
		// Длинный опрос держит соединение до telegramPollSeconds; запас покрывает сеть.
		c.api = &telegram.Client{Token: config.Token, Base: config.APIBase, HTTP: &http.Client{Timeout: (telegramPollSeconds + 15) * time.Second}}
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
	go c.runMenus(ctx)
	go c.runDigest(ctx)
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
		// session возвращается только с ошибкой; отмена опроса (смена настроек) — не сбой.
		if errors.Is(err, context.Canceled) {
			continue
		}
		var apiErr *telegram.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case http.StatusConflict:
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
			case http.StatusUnauthorized, http.StatusNotFound:
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
	var me telegram.User
	if err := c.api.Call(ctx, "getMe", map[string]any{}, &me); err != nil {
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
		var updates []telegram.Update
		err := c.api.Call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": telegramPollSeconds,
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
	if err := c.api.Call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil); err != nil && ctx.Err() == nil {
		log.Printf("telegram: меню команд не опубликовано: %v", err)
	}
	url := c.webAppURL()
	for id := range c.admins {
		button := map[string]any{"type": "commands"}
		if url != "" {
			button = map[string]any{"type": "web_app", "text": "Панель", "web_app": map[string]string{"url": url}}
		}
		// Личный чат администратора совпадает с его ID; если он ещё не писал боту, Telegram ответит ошибкой.
		_ = c.api.Call(ctx, "setChatMenuButton", map[string]any{"chat_id": id, "menu_button": button}, nil)
	}
}

// webAppURL — публичный HTTPS-адрес мини-приложения или пустая строка.
func (c *telegramControl) webAppURL() string {
	if c.config.WebAppURL == "" {
		return ""
	}
	return c.config.WebAppURL + "/tg/"
}

func (c *telegramControl) handleUpdate(update telegram.Update) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch {
	case update.Message != nil:
		c.handleMessage(ctx, update.Message)
	case update.Callback != nil:
		c.handleCallback(ctx, update.Callback)
	}
}

func (c *telegramControl) rememberUnknown(user telegram.User, chat string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := strconv.FormatInt(user.ID, 10)
	kept := []telegramSender{{ID: id, Name: user.DisplayName(), Username: user.Username, Chat: chat, At: c.now().UTC()}}
	for _, sender := range c.unknown {
		if sender.ID != id && len(kept) < telegramUnknownLimit {
			kept = append(kept, sender)
		}
	}
	c.unknown = kept
}

func (c *telegramControl) handleMessage(ctx context.Context, message *telegram.Message) {
	if message.From == nil || message.From.IsBot {
		return
	}
	if !c.admins[message.From.ID] {
		// Чужим не отвечаем: бот не должен подтверждать, что он чем-то управляет.
		c.rememberUnknown(*message.From, message.Chat.Type)
		return
	}
	private := message.Chat.Type == "private"
	// Команды, пришедшие пока управление было выключено или панель лежала, не исполняются, но в
	// личном чате бот говорит об этом, а не молчит.
	if c.now().Sub(time.Unix(message.Date, 0)) > telegramStaleCommand {
		if private {
			c.noticeStale(ctx, message.Chat.ID)
		}
		return
	}
	c.menu.touch(message.Chat.ID, c.now())
	text := strings.TrimSpace(message.Text)
	if private {
		c.ensureChatKeyboard(ctx, message.Chat.ID)
		if text == "" {
			// Картинка, стикер, голосовое: разбирать нечего, но ответить нужно.
			c.replyUnsupported(ctx, message.Chat.ID)
			return
		}
	}
	c.mu.Lock()
	bot := c.bot
	c.mu.Unlock()
	if !strings.HasPrefix(text, "/") {
		// Обычные слова. В группе бот откликается только на обращение по имени: иначе он
		// принимал бы за команду любую реплику в разговоре людей.
		if !private {
			username := ""
			if bot != nil {
				username = bot.Username
			}
			var mentioned bool
			if text, mentioned = telegram.StripMention(text, username); !mentioned {
				return
			}
		}
		if it, ok := telegram.ParseIntent(text); ok {
			c.handleIntent(ctx, message, it)
		} else {
			// Одно обращение по имени без слов: бот слышит и предлагает выбор.
			c.reply(ctx, message.Chat.ID, "Слушаю 👂 Что сделать?", c.nextKeyboard(message.Chat.ID, nextGeneral))
		}
		return
	}
	fields := strings.Fields(text)
	name := strings.TrimPrefix(fields[0], "/")
	args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if at := strings.IndexByte(name, '@'); at >= 0 {
		// В группе команда может адресоваться другому боту.
		if bot != nil && !strings.EqualFold(name[at+1:], bot.Username) {
			return
		}
		name = name[:at]
	}
	name = strings.ToLower(name)
	chat := message.Chat.ID
	// Команды, у которых есть слова-синонимы, идут тем же путём, что и фразы.
	slash := map[string]telegram.IntentKind{"start": telegram.IntentMenu, "menu": telegram.IntentMenu, "help": telegram.IntentHelp, "status": telegram.IntentStatus, "update": telegram.IntentUpdate, "app": telegram.IntentApp,
		"logs": telegram.IntentLogs, "errors": telegram.IntentErrors, "digest": telegram.IntentDigest, "find": telegram.IntentSearch, "mute": telegram.IntentMute, "unmute": telegram.IntentUnmute}
	if kind, ok := slash[name]; ok {
		it, _ := telegram.ParseIntent(args)
		if args == "" {
			it = telegram.Intent{}
		}
		// Слова после команды уточняют её («/logs 30 updates», «/mute 2h»), но вид задаёт сама команда.
		words := telegram.Tokenize(telegram.Normalize(args))
		it.Words = words
		it.Kind = kind
		telegram.FillView(&it, telegram.Normalize(args))
		switch kind {
		case telegram.IntentErrors:
			it.Level = "ERROR"
		case telegram.IntentSearch:
			it.Text = strings.Trim(strings.TrimSpace(args), `"'«»`)
			if it.Text == "" {
				c.reply(ctx, chat, "Что искать? Например: /find таймаут", c.nextKeyboard(chat, nextGeneral))
				return
			}
		case telegram.IntentMute:
			it.Mute, it.MuteSet = telegram.ParseMuteDuration(telegram.Normalize(args), c.now())
			it.MuteAll = telegram.HasStem(words, "совсем", "полностью", "вообще", "all")
		}
		c.handleIntent(ctx, message, it)
		return
	}
	for _, command := range telegramCommands {
		if command.Name != name || command.Action == "" {
			continue
		}
		if command.Confirm && c.settings().Confirm {
			c.askConfirm(ctx, chat, *message.From, command)
		} else {
			c.reply(ctx, chat, c.execute(*message.From, command.Action), c.nextKeyboard(chat, nextAfterAction))
		}
		return
	}
	c.send(ctx, chat, "Не знаю команду /"+html.EscapeString(name)+". Вот что можно сделать:", c.nextKeyboard(chat, nextGeneral))
}

// appKeyboard — кнопка открытия мини-приложения под сообщением, если оно настроено.
func (c *telegramControl) appKeyboard() map[string]any {
	url := c.webAppURL()
	if url == "" {
		return nil
	}
	return map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "Открыть панель", "web_app": map[string]string{"url": url}}}}}
}

func (c *telegramControl) askConfirm(ctx context.Context, chat int64, user telegram.User, command telegramCommand) {
	nonce, err := randomToken(9)
	if err != nil {
		c.send(ctx, chat, "Не удалось подготовить подтверждение, попробуйте ещё раз.", c.nextKeyboard(chat, nextShort))
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
	c.reply(ctx, chat, question, keyboard)
}

func (c *telegramControl) handleCallback(ctx context.Context, callback *telegram.Callback) {
	answer := func(text string) {
		_ = c.api.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback.ID, "text": text}, nil)
	}
	if !c.admins[callback.From.ID] {
		c.rememberUnknown(callback.From, "callback")
		answer("Нет доступа")
		return
	}
	kind, nonce, _ := strings.Cut(callback.Data, ":")
	switch kind {
	case "upd":
		c.handleUpdateCallback(ctx, callback, nonce, answer)
		return
	case "st":
		c.handleStatusCallback(ctx, callback, nonce, answer)
		return
	case "lg":
		c.handleLogCallback(ctx, callback, nonce, answer)
		return
	case "mu":
		c.handleMuteCallback(ctx, callback, nonce, answer)
		return
	case "rg":
		c.handleIncidentCallback(ctx, callback, nonce, answer)
		return
	case "ic":
		c.handleIncidentAction(ctx, callback, nonce, answer)
		return
	case "mn":
		c.handleMenuCallback(ctx, callback, nonce, answer)
		return
	case "nx":
		c.handleNextCallback(ctx, callback, nonce, answer)
		return
	}
	c.mu.Lock()
	pending, ok := c.pending[nonce]
	delete(c.pending, nonce)
	c.mu.Unlock()
	// Вопрос заменяется итогом, и под ним сразу следующий шаг.
	edit := func(text string, set nextSet) {
		if callback.Message == nil {
			return
		}
		chat := callback.Message.Chat.ID
		_ = c.api.Call(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": callback.Message.MessageID,
			"text": text, "parse_mode": "HTML", "reply_markup": c.nextKeyboard(chat, set)}, nil)
	}
	if !ok || c.now().After(pending.expires) {
		answer("Кнопка устарела")
		edit("Кнопка устарела — отправьте команду ещё раз.", nextGeneral)
		return
	}
	if kind != "ok" {
		answer("Отменено")
		edit("Отменено.", nextShort)
		return
	}
	answer("Выполняю…")
	edit(c.execute(callback.From, pending.action), nextAfterAction)
}

// execute выполняет действие тем же путём, что и кнопки панели, и пишет его в журнал действий.
func (c *telegramControl) execute(user telegram.User, action string) string {
	result, status := c.s.performAction(action)
	if c.s.audit != nil {
		event := auditEvent{Time: c.now().UTC(), Actor: user.Actor(), Action: "bot." + action, Detail: result.Message, IP: "telegram"}
		if err := c.s.audit.add(event); err != nil {
			fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		}
	}
	// Аудит пишется до проверки процесса: по нему уведомление объясняет, кто остановил бота.
	if status == http.StatusOK && c.s.notifier != nil {
		c.s.notifier.observe(c.s.bot.PID() != 0)
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
	text.WriteString("<b>Управление Heroku</b>\nПишите словами — слэш не нужен:\n")
	text.WriteString("\n• «меню» — главное меню: живой статус, который сам обновляется, и кнопки")
	text.WriteString("\n• «статус» — как бот и сервер, с кнопками действий")
	text.WriteString("\n• «покажи логи», «логи 30», «логи updates» — записи журнала")
	text.WriteString("\n• «ошибки», «что упало» — ошибки, сгруппированные по причинам")
	text.WriteString("\n• «найди таймаут» — поиск по всему журналу")
	text.WriteString("\n• «сводка», «что было за ночь», «сводка за неделю» — что случилось за период; каждое утро она приходит сама («присылай сводку в 8:30», «не присылай сводку»)")
	text.WriteString("\n• «запусти», «перезапусти», «останови»")
	if c.settings().Confirm {
		text.WriteString(" (перезапуск и остановка — с подтверждением: кнопкой или словом «да»)")
	}
	text.WriteString("\n• «обнови» — проверить новую версию панели")
	text.WriteString("\n• «тишина на час», «до утра», «включи уведомления»")
	text.WriteString("\n\nВнизу чата — быстрые кнопки, а под каждым ответом — что можно сделать дальше.")
	text.WriteString("\n\nИ обычные команды: ")
	var names []string
	for _, command := range telegramCommands {
		if command.Name == "app" && c.webAppURL() == "" {
			continue
		}
		names = append(names, "/"+command.Name)
	}
	text.WriteString(strings.Join(names, " "))
	text.WriteString("\n<i>В группе обращайтесь по имени бота: «@имя статус».</i>")
	return text.String()
}

func (c *telegramControl) send(ctx context.Context, chat int64, text string, keyboard map[string]any) {
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	// Ответ без кнопок оставил бы человека без выбора.
	if keyboard == nil {
		keyboard = c.nextKeyboard(chat, nextShort)
	}
	payload["reply_markup"] = keyboard
	if err := c.api.Call(ctx, "sendMessage", payload, nil); err != nil {
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
		view["bot"] = map[string]any{"id": strconv.FormatInt(c.bot.ID, 10), "username": c.bot.Username, "name": c.bot.DisplayName()}
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
