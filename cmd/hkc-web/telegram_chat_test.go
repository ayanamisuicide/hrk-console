package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHTMLPrefixKeepsMarkupValid проверяет, что набор текста не ломает HTML: теги закрываются,
// сущности не рвутся, а обрезка может дойти до конца слова.
func TestHTMLPrefixKeepsMarkupValid(t *testing.T) {
	src := "<b>Привет</b> мир &amp; <code>код</code>"
	if n := htmlVisibleLen(src); n != 16 {
		t.Fatalf("видимых символов %d, ожидали 16", n)
	}
	cases := []struct {
		visible int
		toWord  bool
		want    string
	}{
		{3, false, "<b>При</b>"},
		{3, true, "<b>Привет</b>"},
		{11, false, "<b>Привет</b> мир "},
		{12, false, "<b>Привет</b> мир &amp;"},
		{0, false, ""},
	}
	for _, c := range cases {
		if got := htmlPrefix(src, c.visible, c.toWord); got != c.want {
			t.Errorf("htmlPrefix(%d, %t) = %q, want %q", c.visible, c.toWord, got, c.want)
		}
	}
	if got := htmlPrefix("<pre>abc</pre>", 2, false); got != "<pre>ab</pre>" {
		t.Errorf("незакрытый тег: %q", got)
	}
	// Для любой длины открытые и закрытые теги сходятся, а сущность целая.
	long := "<b>Заголовок</b>\n<pre>строка &lt;1&gt; &amp; <i>вторая</i> строка</pre> конец"
	for n := 0; n <= htmlVisibleLen(long)+2; n++ {
		got := htmlPrefix(long, n, n%2 == 0)
		for _, tag := range []string{"b", "pre", "i"} {
			if strings.Count(got, "<"+tag+">") != strings.Count(got, "</"+tag+">") {
				t.Fatalf("n=%d: теги <%s> не сходятся: %q", n, tag, got)
			}
		}
		for i := 0; i < len(got); i++ {
			if got[i] == '&' && !strings.Contains(got[i:min(i+7, len(got))], ";") {
				t.Fatalf("n=%d: оборванная сущность: %q", n, got)
			}
		}
	}
}

// TestParseIntentPhrases — как бот понимает обычные фразы.
func TestParseIntentPhrases(t *testing.T) {
	cases := []struct {
		phrase string
		want   intentKind
	}{
		{"меню", intentMenu},
		{"главное меню", intentMenu},
		{"открой меню", intentMenu},
		{"дашборд", intentMenu},
		{"статус", intentStatus},
		{"как дела?", intentStatus},
		{"бот, как ты там", intentStatus},
		{"что с ботом", intentStatus},
		{"обнови статус", intentStatus},
		{"покажи логи", intentLogs},
		{"логи 30", intentLogs},
		{"логи updates", intentLogs},
		{"последние предупреждения", intentLogs},
		{"покажи ошибки", intentErrors},
		{"что упало", intentErrors},
		{"что случилось", intentErrors},
		{"какие были ошибки за час", intentErrors},
		{"найди таймаут", intentSearch},
		{"поищи connection reset", intentSearch},
		{"перезапусти", intentRestart},
		{"перезапусти бота пожалуйста", intentRestart},
		{"останови бота", intentStop},
		{"выключи бота", intentStop},
		{"запусти", intentRun},
		{"включи бота", intentRun},
		{"обнови", intentUpdate},
		{"есть обновления?", intentUpdate},
		{"какая версия", intentUpdate},
		{"тишина на час", intentMute},
		{"помолчи до утра", intentMute},
		{"не беспокой меня 2 часа", intentMute},
		{"выключи уведомления на 30 минут", intentMute},
		{"включи уведомления", intentUnmute},
		{"размуть", intentUnmute},
		{"помощь", intentHelp},
		{"что ты умеешь", intentHelp},
		{"привет", intentHello},
		{"спасибо", intentThanks},
		{"да", intentYes},
		{"ага", intentYes},
		{"нет", intentNo},
		{"отмена", intentNo},
		{"стоп", intentNo},
		{"не перезапускай", intentCancel},
		{"не надо останавливать", intentCancel},
		{"открой панель", intentApp},
		{"абракадабра", intentFallback},
	}
	for _, c := range cases {
		got, ok := parseIntent(c.phrase)
		if !ok || got.kind != c.want {
			t.Errorf("%q → %q, ожидали %q", c.phrase, got.kind, c.want)
		}
	}

	// Параметры из фразы.
	it, _ := parseIntent("логи 30")
	if it.limit != 30 {
		t.Errorf("логи 30: limit=%d", it.limit)
	}
	it, _ = parseIntent("какие были ошибки за 2 часа")
	if it.kind != intentErrors || it.since != 2*time.Hour {
		t.Errorf("ошибки за 2 часа: %+v", it)
	}
	it, _ = parseIntent("последние предупреждения")
	if it.level != "WARNING" {
		t.Errorf("предупреждения: level=%q", it.level)
	}
	it, _ = parseIntent("найди connection reset")
	if it.text != "connection reset" {
		t.Errorf("поиск: text=%q", it.text)
	}
	it, _ = parseIntent("выключи уведомления совсем")
	if it.kind != intentMute || !it.muteAll {
		t.Errorf("совсем: %+v", it)
	}
	it, _ = parseIntent("не беспокой меня 2 часа")
	if !it.muteSet || it.mute != 2*time.Hour {
		t.Errorf("2 часа: %+v", it)
	}
	if name := moduleFrom([]string{"логи", "updates"}, []string{"heroku.core", "heroku.updates"}); name != "heroku.updates" {
		t.Errorf("модуль по слову: %q", name)
	}
	if name := moduleFrom([]string{"логи"}, []string{"heroku.core"}); name != "" {
		t.Errorf("лишний модуль: %q", name)
	}
}

// TestParseMuteDuration — длительность тишины из фразы.
func TestParseMuteDuration(t *testing.T) {
	now := time.Date(2026, 10, 11, 23, 0, 0, 0, time.Local)
	cases := map[string]time.Duration{
		"на час": time.Hour, "тишина на 2 часа": 2 * time.Hour, "на 30 минут": 30 * time.Minute, "полчаса": 30 * time.Minute,
		"на 3 дня": 72 * time.Hour, "1h": time.Hour, "45m": 45 * time.Minute, "на сутки": 24 * time.Hour,
		"до утра": 9 * time.Hour, // с 23:00 до 08:00
	}
	for phrase, want := range cases {
		got, ok := parseMuteDuration(phrase, now)
		if !ok || got != want {
			t.Errorf("%q → %v (%t), ожидали %v", phrase, got, ok, want)
		}
	}
	if _, ok := parseMuteDuration("помолчи", now); ok {
		t.Error("длительности нет, но она нашлась")
	}
}

// TestMuteStore проверяет срок тишины, серьёзность событий и сохранение между запусками.
func TestMuteStore(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "mute.json")
	m := newMuteStore(path, clock)
	if _, ok := m.active(); ok || m.silences(alertEvent{Severity: "warning"}) {
		t.Fatal("тишина включена без просьбы")
	}
	if err := m.set(now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if !m.silences(alertEvent{Severity: "warning"}) || !m.silences(alertEvent{Severity: "ok"}) {
		t.Error("обычные события должны молчать")
	}
	if m.silences(alertEvent{Severity: "critical"}) {
		t.Error("критичное событие должно пробиваться")
	}
	// После перезапуска панели тишина сохраняется.
	if _, ok := newMuteStore(path, clock).active(); !ok {
		t.Error("тишина не пережила перезапуск")
	}
	// По истечении срока тишина снимается сама.
	now = now.Add(2 * time.Hour)
	if _, ok := m.active(); ok {
		t.Error("тишина не истекла")
	}
	if err := m.set(now.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if !m.silences(alertEvent{Severity: "critical"}) {
		t.Error("«совсем» должно глушить и критичное")
	}
	if err := m.clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok := newMuteStore(path, clock).active(); ok {
		t.Error("снятая тишина вернулась")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл состояния: %v", err)
	}
}

// groupMessage — сообщение в группе.
func groupMessage(from int64, text string) tgUpdate {
	update := message(from, text, 0)
	update.Message.Chat = tgChat{ID: -1001, Type: "supergroup"}
	return update
}

func lastSent(t *testing.T, fake *fakeBotAPI) map[string]any {
	t.Helper()
	sent := fake.byMethod("sendMessage")
	if len(sent) == 0 {
		t.Fatal("бот ничего не ответил")
	}
	return sent[len(sent)-1]
}

func callbackData(t *testing.T, markup any, suffix string) string {
	t.Helper()
	data, _ := json.Marshal(markup)
	var keyboard struct {
		Rows [][]struct {
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(data, &keyboard); err != nil {
		t.Fatal(err)
	}
	for _, row := range keyboard.Rows {
		for _, button := range row {
			if strings.HasSuffix(button.Data, suffix) {
				return button.Data
			}
		}
	}
	t.Fatalf("нет кнопки …%s в %s", suffix, data)
	return ""
}

// TestTelegramStreamsRepliesInPrivateChat — ответ набирается черновиком, а в конце приходит обычным сообщением.
func TestTelegramStreamsRepliesInPrivateChat(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "помощь", 0))

	drafts := fake.byMethod("sendMessageDraft")
	if len(drafts) < 3 {
		t.Fatalf("шагов набора %d, ожидали несколько", len(drafts))
	}
	final := lastSent(t, fake)["text"].(string)
	previous := 0
	for i, draft := range drafts {
		if draft["draft_id"] != drafts[0]["draft_id"] || draft["draft_id"].(float64) == 0 {
			t.Fatalf("шаг %d: draft_id %v, должен быть один и ненулевой", i, draft["draft_id"])
		}
		text := draft["text"].(string)
		if n := htmlVisibleLen(text); n < previous || n > htmlVisibleLen(final) {
			t.Fatalf("шаг %d: текст растёт неправильно (%d после %d из %d)", i, n, previous, htmlVisibleLen(final))
		}
		previous = htmlVisibleLen(text)
		if !strings.HasPrefix(final, strings.SplitN(text, "</", 2)[0][:min(len(text), 3)]) {
			t.Fatalf("шаг %d: черновик не начало ответа: %q", i, text)
		}
	}
	if !strings.Contains(final, "Пишите словами") || len(fake.byMethod("sendMessage")) != 1 {
		t.Fatalf("итоговое сообщение: %q", final)
	}
}

// TestTelegramThinksWhileWorking — для работы с ожиданием сначала показывается «Думаю…».
func TestTelegramThinksWhileWorking(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "статус", 0))
	drafts := fake.byMethod("sendMessageDraft")
	if len(drafts) == 0 || drafts[0]["text"] != "" {
		t.Fatalf("первым должен идти пустой черновик «Думаю…»: %v", drafts)
	}
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Бот остановлен") {
		t.Fatalf("статус: %v", lastSent(t, fake))
	}
}

// TestTelegramNoStreamInGroups — в группах черновиков нет, ответ обычный.
func TestTelegramNoStreamInGroups(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.mu.Lock()
	control.bot = &tgUser{ID: 42, Username: "panel_bot", IsBot: true}
	control.mu.Unlock()
	control.handleUpdate(groupMessage(1, "@panel_bot помощь"))
	if len(fake.byMethod("sendMessageDraft")) != 0 {
		t.Fatal("в группе отправлены черновики")
	}
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("в группе нет ответа")
	}
}

// TestTelegramStreamFallback — если Telegram отверг черновики, ответы идут обычными сообщениями и
// анимацию больше не пробуют.
func TestTelegramStreamFallback(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	fake.setFail("sendMessageDraft", true)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "помощь", 0))
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("ответ потерян из-за отказа черновиков")
	}
	tried := len(fake.byMethod("sendMessageDraft"))
	if tried == 0 || !control.draftOff.Load() {
		t.Fatalf("после отказа анимация должна отключиться (попыток %d)", tried)
	}
	control.handleUpdate(message(1, "помощь", 0))
	if len(fake.byMethod("sendMessageDraft")) != tried || len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("отключённая анимация всё равно пробуется")
	}
}

// TestTelegramFreeTextControl — действия словами, подтверждение словом «да» и отказ на «не».
func TestTelegramFreeTextControl(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)

	control.handleUpdate(message(1, "останови бота", 0))
	ask := lastSent(t, fake)
	if !strings.Contains(ask["text"].(string), "Остановить бота?") {
		t.Fatalf("нет вопроса: %v", ask)
	}
	callbackData(t, ask["reply_markup"], "") // кнопки подтверждения есть

	control.handleUpdate(message(1, "да", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "уже остановлен") {
		t.Fatalf("действие не выполнено: %v", lastSent(t, fake))
	}
	events, _ := s.audit.recent(10)
	if len(events) != 1 || events[0].Action != "bot.stop" || events[0].Actor != "telegram:@anya" {
		t.Fatalf("журнал действий: %+v", events)
	}

	// Повторное «да» ничего не делает: подтверждение одноразовое.
	control.handleUpdate(message(1, "да", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Подтверждать нечего") {
		t.Fatalf("повторное да: %v", lastSent(t, fake))
	}

	// «Не останавливай» — это отказ, а не команда остановки.
	control.handleUpdate(message(1, "не останавливай", 0))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "ничего не делаю") {
		t.Fatalf("отрицание: %v", lastSent(t, fake))
	}
	// «Нет» снимает вопрос в ожидании.
	control.handleUpdate(message(1, "останови", 0))
	control.handleUpdate(message(1, "нет", 0))
	control.handleUpdate(message(1, "да", 0))
	if events, _ := s.audit.recent(10); len(events) != 1 {
		t.Fatalf("после «нет» действие всё же выполнилось: %+v", events)
	}
}

// TestTelegramGroupNeedsMention — в группе обычные слова без обращения к боту игнорируются.
func TestTelegramGroupNeedsMention(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.mu.Lock()
	control.bot = &tgUser{ID: 42, Username: "panel_bot", IsBot: true}
	control.mu.Unlock()
	control.handleUpdate(groupMessage(1, "статус"))
	control.handleUpdate(groupMessage(1, "останови бота"))
	if len(fake.byMethod("sendMessage")) != 0 {
		t.Fatalf("бот отозвался на разговор людей: %v", fake.byMethod("sendMessage"))
	}
	control.handleUpdate(groupMessage(1, "@panel_bot статус"))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "Бот остановлен") {
		t.Fatalf("на обращение нет ответа: %v", fake.byMethod("sendMessage"))
	}
	// Чужие по-прежнему не получают ничего.
	control.handleUpdate(groupMessage(99, "@panel_bot статус"))
	if len(fake.byMethod("sendMessage")) != 1 {
		t.Fatal("чужому ответили")
	}
}

// TestTelegramStatusKeyboardAndRefresh — статус с кнопками; «Обновить» правит то же сообщение.
func TestTelegramStatusKeyboardAndRefresh(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	_, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "статус", 0))
	markup := lastSent(t, fake)["reply_markup"]
	for _, suffix := range []string{":r", ":l", ":e", ":run"} {
		callbackData(t, markup, suffix)
	}
	refresh := callbackData(t, markup, ":r")
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c1", From: tgUser{ID: 1}, Data: refresh,
		Message: &tgMessage{MessageID: 55, Chat: tgChat{ID: 1, Type: "private"}}}})
	edits := fake.byMethod("editMessageText")
	if len(edits) != 1 || edits[0]["message_id"].(float64) != 55 || !strings.Contains(edits[0]["text"].(string), "Бот остановлен") {
		t.Fatalf("обновление статуса: %v", edits)
	}
	// Telegram не принимает правку без изменений: это не сбой, и нового сообщения быть не должно.
	fake.mu.Lock()
	fake.failDescription = "Bad Request: message is not modified"
	fake.mu.Unlock()
	fake.setFail("editMessageText", true)
	sentBefore := len(fake.byMethod("sendMessage"))
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c2", From: tgUser{ID: 1}, Data: refresh,
		Message: &tgMessage{MessageID: 55, Chat: tgChat{ID: 1, Type: "private"}}}})
	if len(fake.byMethod("sendMessage")) != sentBefore {
		t.Fatal("правка без изменений породила новое сообщение")
	}
	// Любая другая ошибка правки (сообщение удалено) допускает новое сообщение.
	fake.mu.Lock()
	fake.failDescription = "Bad Request: message to edit not found"
	fake.mu.Unlock()
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c3", From: tgUser{ID: 1}, Data: refresh,
		Message: &tgMessage{MessageID: 55, Chat: tgChat{ID: 1, Type: "private"}}}})
	if len(fake.byMethod("sendMessage")) != sentBefore+1 {
		t.Fatal("при недоступном сообщении статус потерялся")
	}
}

// TestTelegramMuteByWords — «тишина на 2 часа» глушит уведомления, «включи уведомления» возвращает.
func TestTelegramMuteByWords(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	control.handleUpdate(message(1, "тишина на 2 часа", 0))
	state, ok := control.mute.active()
	if !ok || state.All {
		t.Fatalf("тишина: %+v %t", state, ok)
	}
	if until := time.Until(state.Until); until < 119*time.Minute || until > 121*time.Minute {
		t.Fatalf("срок тишины %v", until)
	}
	reply := lastSent(t, fake)
	if !strings.Contains(reply["text"].(string), "Тишина на 2 ч") || !strings.Contains(reply["text"].(string), "Критичные") {
		t.Fatalf("ответ: %v", reply["text"])
	}
	callbackData(t, reply["reply_markup"], "mu:off")
	if events, _ := s.audit.recent(10); len(events) != 1 || events[0].Action != "telegram.mute" {
		t.Fatalf("журнал действий: %+v", events)
	}

	control.handleUpdate(message(1, "выключи уведомления совсем на час", 0))
	if state, _ := control.mute.active(); !state.All {
		t.Fatalf("«совсем»: %+v", state)
	}
	control.handleUpdate(message(1, "включи уведомления", 0))
	if _, ok := control.mute.active(); ok {
		t.Fatal("уведомления не включились")
	}
	// Кнопка «Включить» под ответом тоже снимает тишину.
	control.handleUpdate(message(1, "/mute 30m", 0))
	if _, ok := control.mute.active(); !ok {
		t.Fatal("/mute 30m не сработала")
	}
	control.handleUpdate(tgUpdate{Callback: &tgCallback{ID: "c1", From: tgUser{ID: 1}, Data: "mu:off",
		Message: &tgMessage{MessageID: 9, Chat: tgChat{ID: 1, Type: "private"}}}})
	if _, ok := control.mute.active(); ok {
		t.Fatal("кнопка не сняла тишину")
	}
}
