package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heroku-console/internal/telegram"
)

// menuControl — бот, который слушает команды (иначе меню не обновляется).
func menuControl(t *testing.T) (*fakeBotAPI, *server, *telegramControl) {
	t.Helper()
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	control.setState("running", "")
	if err := s.operations.setTelegram(telegramSettings{ControlEnabled: true, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	return fake, s, control
}

func menuCallback(control *telegramControl, fake *fakeBotAPI, t *testing.T, act string) {
	t.Helper()
	entry, ok := control.menu.get(1)
	if !ok {
		t.Fatal("меню не открыто")
	}
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "cb-" + act, From: telegram.User{ID: 1, Username: "anya"}, Data: "mn:" + act,
		Message: &telegram.Message{MessageID: entry.Message, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
}

func lastEdit(t *testing.T, fake *fakeBotAPI) map[string]any {
	t.Helper()
	edits := fake.byMethod("editMessageText")
	if len(edits) == 0 {
		t.Fatal("сообщение не правилось")
	}
	return edits[len(edits)-1]
}

func TestBarAndPlural(t *testing.T) {
	cases := map[float64]string{0: "▱▱▱▱▱▱▱▱", 50: "▰▰▰▰▱▱▱▱", 100: "▰▰▰▰▰▰▰▰", 150: "▰▰▰▰▰▰▰▰", -5: "▱▱▱▱▱▱▱▱"}
	for percent, want := range cases {
		if got := bar(percent); got != want {
			t.Errorf("bar(%v) = %q, want %q", percent, got, want)
		}
	}
	if ruPlural(1, "ошибка", "ошибки", "ошибок") != "ошибка" || ruPlural(3, "ошибка", "ошибки", "ошибок") != "ошибки" ||
		ruPlural(5, "ошибка", "ошибки", "ошибок") != "ошибок" || ruPlural(11, "ошибка", "ошибки", "ошибок") != "ошибок" || ruPlural(22, "ошибка", "ошибки", "ошибок") != "ошибки" {
		t.Error("склонение")
	}
}

// TestMenuOpensWithoutJournalRecords — меню открывается словами и /start; записей журнала в нём нет.
func TestMenuOpensWithoutJournalRecords(t *testing.T) {
	fake, s, control := menuControl(t)
	journalFor(t, s) // в журнале есть записи уровней ERROR и WARNING
	control.handleUpdate(message(1, "меню", 0))
	sent := lastSent(t, fake)
	text := sent["text"].(string)
	for _, want := range []string{"главное меню", "Бот остановлен", "Версии:", "Уведомления включены", "обновлено", "каждые 10 с"} {
		if !strings.Contains(text, want) {
			t.Errorf("в меню нет %q:\n%s", want, text)
		}
	}
	// Счётчики ошибок за час допустимы, но самих записей быть не должно.
	for _, banned := range []string{"manifest failed", "slow reply", "boom", "[ERROR]", "Traceback", leakedSecret} {
		if strings.Contains(text, banned) {
			t.Errorf("в меню попала запись журнала %q", banned)
		}
	}
	// Бот в тесте остановлен, поэтому вместо «Перезапустить» — «Запустить».
	for _, suffix := range []string{"mn:mod", "mn:more", "mn:cy", "mn:r", "st:l", "st:e", "mn:mu1", "mn:run"} {
		callbackData(t, sent["reply_markup"], suffix)
	}
	entry, ok := control.menu.get(1)
	if !ok || entry.Message == 0 || entry.Interval != 10 || entry.Screen != "main" {
		t.Fatalf("состояние меню: %+v %t", entry, ok)
	}
	// Анимация набора, как у остальных ответов.
	if len(fake.byMethod("sendMessageDraft")) < 2 {
		t.Fatal("меню открылось без анимации")
	}

	// /start теперь тоже открывает меню, а повторное открытие переносит его вниз.
	control.handleUpdate(message(1, "/start", 0))
	deletes := fake.byMethod("deleteMessage")
	if len(deletes) != 1 || deletes[0]["message_id"].(float64) != float64(entry.Message) {
		t.Fatalf("старое меню не удалено: %v", deletes)
	}
	if again, _ := control.menu.get(1); again.Message == entry.Message {
		t.Fatal("номер сообщения не обновился")
	}
}

func TestMenuOnlyInPrivateChat(t *testing.T) {
	fake, _, control := menuControl(t)
	control.mu.Lock()
	control.bot = &telegram.User{ID: 42, Username: "panel_bot", IsBot: true}
	control.mu.Unlock()
	control.handleUpdate(groupMessage(1, "@panel_bot меню"))
	if !strings.Contains(lastSent(t, fake)["text"].(string), "только в личном чате") {
		t.Fatalf("ответ в группе: %v", lastSent(t, fake))
	}
	if len(control.menu.all()) != 0 {
		t.Fatal("в группе создано меню")
	}
}

// TestMenuRefreshSchedule — меню правится по таймеру, реже в простое, и не правится при «выкл».
func TestMenuRefreshSchedule(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	t0 := time.Now()

	control.menuTick(t.Context(), t0) // первый проход только назначает срок
	control.menuTick(t.Context(), t0.Add(5*time.Second))
	if n := len(fake.byMethod("editMessageText")); n != 0 {
		t.Fatalf("правок раньше срока: %d", n)
	}
	control.menuTick(t.Context(), t0.Add(11*time.Second))
	edit := lastEdit(t, fake)
	entry, _ := control.menu.get(1)
	if edit["message_id"].(float64) != float64(entry.Message) || !strings.Contains(edit["text"].(string), "главное меню") {
		t.Fatalf("плановая правка: %v", edit)
	}
	if edit["reply_markup"] == nil {
		t.Fatal("после правки пропали кнопки")
	}

	// Давно не нажимали — обновляем раз в минуту. Первый поздний проход правит по старому сроку,
	// дальше действует минутный период.
	late := t0.Add(10 * time.Minute)
	control.menuTick(t.Context(), late)
	count := len(fake.byMethod("editMessageText"))
	control.menuTick(t.Context(), late.Add(20*time.Second))
	control.menuTick(t.Context(), late.Add(40*time.Second))
	if len(fake.byMethod("editMessageText")) != count {
		t.Fatal("в простое меню обновляется слишком часто")
	}
	control.menuTick(t.Context(), late.Add(61*time.Second))
	if len(fake.byMethod("editMessageText")) != count+1 {
		t.Fatal("в простое меню не обновилось через минуту")
	}

	// Нажатие возвращает частое обновление; «выкл» останавливает его совсем.
	control.menu.touch(1, late.Add(2*time.Minute))
	control.menu.update(1, func(e *menuEntry) { e.Interval = 0 })
	count = len(fake.byMethod("editMessageText"))
	for i := 0; i < 5; i++ {
		control.menuTick(t.Context(), late.Add(time.Duration(i+3)*time.Minute))
	}
	if len(fake.byMethod("editMessageText")) != count {
		t.Fatal("выключенное автообновление всё равно правит меню")
	}
}

// TestMenuPausedWhenBotNotListening — пока бот не слушает команды, кнопки мертвы и меню не трогаем.
func TestMenuPausedWhenBotNotListening(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	control.setState("disabled", "")
	t0 := time.Now()
	control.menuTick(t.Context(), t0)
	control.menuTick(t.Context(), t0.Add(time.Hour))
	if len(fake.byMethod("editMessageText")) != 0 {
		t.Fatal("меню правится, хотя бот не слушает команды")
	}
}

// TestMenuEditErrors — «не изменилось» не страшно, лимит откладывает правку, пропавшее сообщение забывается.
func TestMenuEditErrors(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	t0 := time.Now()
	control.menuTick(t.Context(), t0)

	fake.mu.Lock()
	fake.failDescription = "Bad Request: message is not modified"
	fake.mu.Unlock()
	fake.setFail("editMessageText", true)
	control.menuTick(t.Context(), t0.Add(11*time.Second))
	if _, ok := control.menu.get(1); !ok {
		t.Fatal("«not modified» стёрло меню")
	}

	// Лимит Telegram: ждём, сколько он просит (retry after 5 → 6 с), и дальше обновляем реже.
	fake.mu.Lock()
	fake.failCode, fake.failDescription = 429, "Too Many Requests: retry after 5"
	fake.mu.Unlock()
	control.menuTick(t.Context(), t0.Add(22*time.Second))
	attempts := len(fake.byMethod("editMessageText"))
	control.menuTick(t.Context(), t0.Add(25*time.Second))
	if len(fake.byMethod("editMessageText")) != attempts {
		t.Fatal("после 429 бот не выдержал паузу")
	}
	if _, ok := control.menu.get(1); !ok {
		t.Fatal("429 стёрло меню")
	}

	// Сообщение удалено — меню забывается и больше не дёргается.
	fake.mu.Lock()
	fake.failCode, fake.failDescription = 400, "Bad Request: message to edit not found"
	fake.mu.Unlock()
	control.menuTick(t.Context(), t0.Add(2*time.Minute))
	if _, ok := control.menu.get(1); ok {
		t.Fatal("меню удалённого сообщения осталось")
	}
	attempts = len(fake.byMethod("editMessageText"))
	control.menuTick(t.Context(), t0.Add(10*time.Minute))
	if len(fake.byMethod("editMessageText")) != attempts {
		t.Fatal("бот продолжает править несуществующее сообщение")
	}
}

// TestMenuButtons — экраны, интервал, тишина и закрепление правят то же сообщение.
func TestMenuButtons(t *testing.T) {
	fake, s, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))

	menuCallback(control, fake, t, "mod")
	if text := lastEdit(t, fake)["text"].(string); !strings.Contains(text, "Модули бота") || !strings.Contains(text, "остановлен") {
		t.Fatalf("экран модулей: %s", text)
	}
	if entry, _ := control.menu.get(1); entry.Screen != "modules" {
		t.Fatalf("экран не запомнился: %+v", entry)
	}
	callbackData(t, lastEdit(t, fake)["reply_markup"], "mn:m")
	menuCallback(control, fake, t, "m")
	if !strings.Contains(lastEdit(t, fake)["text"].(string), "главное меню") {
		t.Fatal("«Назад» не вернул главный экран")
	}

	menuCallback(control, fake, t, "cy")
	if entry, _ := control.menu.get(1); entry.Interval != 60 {
		t.Fatalf("после смены интервала: %+v", entry)
	}
	menuCallback(control, fake, t, "iv5")
	if entry, _ := control.menu.get(1); entry.Interval != 5 {
		t.Fatalf("iv5: %+v", entry)
	}
	menuCallback(control, fake, t, "iv0")
	if entry, _ := control.menu.get(1); entry.Interval != 0 {
		t.Fatalf("iv0: %+v", entry)
	}
	if !strings.Contains(lastEdit(t, fake)["text"].(string), "автообновление выключено") {
		t.Fatal("подпись не отражает выключенное автообновление")
	}
	answers := len(fake.byMethod("answerCallbackQuery"))
	menuCallback(control, fake, t, "iv7") // нет такого периода
	if !strings.Contains(fake.byMethod("answerCallbackQuery")[answers]["text"].(string), "устарела") {
		t.Fatal("неизвестный интервал принят")
	}

	menuCallback(control, fake, t, "mu1")
	if _, ok := control.mute.Active(); !ok {
		t.Fatal("тишина не включилась из меню")
	}
	if !strings.Contains(lastEdit(t, fake)["text"].(string), "Уведомления выключены до") {
		t.Fatal("меню не показывает тишину")
	}
	callbackData(t, lastEdit(t, fake)["reply_markup"], "mn:mu0")
	menuCallback(control, fake, t, "mu0")
	if _, ok := control.mute.Active(); ok {
		t.Fatal("тишина не снялась")
	}
	if events, _ := s.audit.recent(10); len(events) != 2 || events[1].Action != "telegram.mute" {
		t.Fatalf("журнал действий: %+v", events)
	}

	entry, _ := control.menu.get(1)
	menuCallback(control, fake, t, "pin")
	pins := fake.byMethod("pinChatMessage")
	if len(pins) != 1 || pins[0]["message_id"].(float64) != float64(entry.Message) || pins[0]["disable_notification"] != true {
		t.Fatalf("закрепление: %v", pins)
	}
	if entry, _ = control.menu.get(1); !entry.Pinned {
		t.Fatal("закрепление не запомнилось")
	}
	menuCallback(control, fake, t, "unpin")
	if len(fake.byMethod("unpinChatMessage")) != 1 {
		t.Fatal("открепление не вызвано")
	}

	// Повторное открытие переносит меню и сохраняет выбранный интервал и закрепление.
	menuCallback(control, fake, t, "pin")
	control.menu.update(1, func(e *menuEntry) { e.Interval = 60 })
	control.handleUpdate(message(1, "меню", 0))
	reopened, _ := control.menu.get(1)
	if reopened.Interval != 60 || !reopened.Pinned || len(fake.byMethod("pinChatMessage")) != 3 {
		t.Fatalf("переоткрытие потеряло настройки: %+v", reopened)
	}
}

// TestMenuStaleButtons — кнопки старого меню ничего не делают и объясняют, что делать.
func TestMenuStaleButtons(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	edits := len(fake.byMethod("editMessageText"))
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "old", From: telegram.User{ID: 1}, Data: "mn:mod",
		Message: &telegram.Message{MessageID: 999999, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	answers := fake.byMethod("answerCallbackQuery")
	if len(answers) != 1 || !strings.Contains(answers[0]["text"].(string), "старое меню") {
		t.Fatalf("ответ на кнопку старого меню: %v", answers)
	}
	if len(fake.byMethod("editMessageText")) != edits {
		t.Fatal("кнопка старого меню правила сообщение")
	}
	// Чужой не может нажимать кнопки меню.
	entry, _ := control.menu.get(1)
	control.handleUpdate(telegram.Update{Callback: &telegram.Callback{ID: "x", From: telegram.User{ID: 99}, Data: "mn:mu1",
		Message: &telegram.Message{MessageID: entry.Message, Chat: telegram.Chat{ID: 1, Type: "private"}}}})
	if _, ok := control.mute.Active(); ok {
		t.Fatal("чужой включил тишину")
	}
}

// TestMenuSurvivesRestart — после перезапуска панели меню продолжает обновляться в том же сообщении.
func TestMenuSurvivesRestart(t *testing.T) {
	fake, s, control := menuControl(t)
	s.dataDir = t.TempDir()
	control.menu = newMenuStore(menuFilePath(s.dataDir))
	control.handleUpdate(message(1, "меню", 0))
	opened, _ := control.menu.get(1)
	menuCallback(control, fake, t, "iv60")

	restarted := newTelegramControl(s, telegramConfig{Token: testBotToken, APIBase: control.config.APIBase, Admins: []int64{1}})
	restarted.streamGap = 0
	restarted.setState("running", "")
	got, ok := restarted.menu.get(1)
	if !ok || got.Message != opened.Message || got.Interval != 60 {
		t.Fatalf("после перезапуска: %+v %t (было %+v)", got, ok, opened)
	}
	t0 := time.Now()
	restarted.menuTick(t.Context(), t0)
	restarted.menuTick(t.Context(), t0.Add(2*time.Minute))
	if edit := lastEdit(t, fake); edit["message_id"].(float64) != float64(opened.Message) {
		t.Fatalf("правится другое сообщение: %v", edit)
	}
	// Мусор в файле не ломает запуск.
	if err := writeFile(filepath.Join(s.dataDir, menuFile), "{не json"); err != nil {
		t.Fatal(err)
	}
	if n := len(newMenuStore(filepath.Join(s.dataDir, menuFile)).all()); n != 0 {
		t.Fatalf("из повреждённого файла прочитано %d меню", n)
	}
}

// TestMenuContent — наполнение главного экрана для разных состояний.
func TestMenuContent(t *testing.T) {
	_, _, control := menuControl(t)
	now := time.Date(2026, 10, 11, 18, 47, 12, 0, time.Local)
	entry := menuEntry{Screen: "main", Interval: 10}
	data := menuData{running: true, pid: 4821, uptime: "3д 4ч", haveSystem: true, cpu: 23, mem: 51, dsk: 34,
		botCPU: 2.5, botRSS: 180 << 20, rx: 5120, tx: 1024, haveNet: true,
		modulesLive: true, modulesTotal: 42, modulesReady: 41, modulesFailed: []moduleEntry{{Name: "Weather", State: "error", Error: "ValueError: bad token=" + leakedSecret}},
		heavy:     menuHeavy{errs: 2, warns: 5, haveJournal: true, botVersion: "1.9.2"},
		lastEvent: "18:42 Бот перезапущен автоматически", watchdog: "включено", updateTo: "v9.9.9", panelVer: "v2.14.0"}
	text, keyboard := control.renderMenuMain(1, entry, data, now)
	for _, want := range []string{"🟢", "Бот работает", "PID 4821", "бот 1.9.2", "панель v2.14.0", "⬆️ доступна v9.9.9",
		"CPU    ▰▰▱▱▱▱▱▱  23%", "Память ▰▰▰▰▱▱▱▱  51%", "Бот: CPU 2.5%", "Сеть: ↓ 5.0 КБ/с", "41 из 42 работают", "с ошибкой: Weather",
		"2 ошибки", "5 предупреждений", "Последнее событие: 18:42", "Автовосстановление: включено", "Уведомления включены", "обновлено 18:47:12"} {
		if !strings.Contains(text, want) {
			t.Errorf("в меню нет %q:\n%s", want, text)
		}
	}
	callbackData(t, keyboard, "st:sp")
	callbackData(t, keyboard, "mn:mu1")

	// Остановленный бот: кнопка запуска, без метрик процесса.
	data.running = false
	stopped, keyboard := control.renderMenuMain(1, entry, data, now)
	if !strings.Contains(stopped, "🔴") || !strings.Contains(stopped, "Бот остановлен") || strings.Contains(stopped, "Бот: CPU") {
		t.Errorf("остановленный бот:\n%s", stopped)
	}
	callbackData(t, keyboard, "mn:run")

	// Экран модулей: причина ошибки без секрета.
	data.running = true
	modules, _ := control.renderMenuModules(1, entry, data, now)
	if !strings.Contains(modules, "Weather") || !strings.Contains(modules, "ValueError") || strings.Contains(modules, leakedSecret) {
		t.Errorf("экран модулей:\n%s", modules)
	}
	// Тишина отражается в меню и меняет кнопку.
	control.mute = telegram.NewMuteStore("", func() time.Time { return now })
	if err := control.mute.Set(now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	muted, keyboard := control.renderMenuMain(1, entry, control.menuSnapshot(now), now)
	if !strings.Contains(muted, "Уведомления выключены до") || !strings.Contains(muted, "критичные приходят") {
		t.Errorf("тишина в меню:\n%s", muted)
	}
	callbackData(t, keyboard, "mn:mu0")
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }

// tickEvery прогоняет планировщик шагом step от start до end включительно.
func tickEvery(t *testing.T, control *telegramControl, start time.Time, step, length time.Duration) {
	t.Helper()
	for offset := time.Duration(0); offset <= length; offset += step {
		control.menuTick(t.Context(), start.Add(offset))
	}
}

// TestMenuOneSecondMode — секундный режим держит ровную частоту и каждый раз меняет текст.
func TestMenuOneSecondMode(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	control.menu.update(1, func(e *menuEntry) { e.Interval = 1 })
	t0 := time.Now()
	control.menu.touch(1, t0)

	tickEvery(t, control, t0, menuTickEvery, 10*time.Second)
	edits := fake.byMethod("editMessageText")
	if len(edits) < 9 || len(edits) > 10 {
		t.Fatalf("за 10 секунд %d правок, ожидали 9–10", len(edits))
	}
	if edits[len(edits)-1]["text"] == edits[len(edits)-2]["text"] {
		t.Fatal("соседние правки одинаковы: Telegram отверг бы их как «не изменилось»")
	}
	if !strings.Contains(edits[len(edits)-1]["text"].(string), "каждую секунду") {
		t.Fatalf("подпись: %v", edits[len(edits)-1]["text"])
	}

	// Давно не нажимали — секундный режим сам замедляется до раза в минуту.
	late := t0.Add(5 * time.Minute)
	control.menuTick(t.Context(), late) // по старому сроку
	before := len(fake.byMethod("editMessageText"))
	tickEvery(t, control, late.Add(menuTickEvery), menuTickEvery, 30*time.Second)
	if len(fake.byMethod("editMessageText")) != before {
		t.Fatal("в простое секундный режим продолжает править каждую секунду")
	}
	// Нажатие снова включает частые обновления.
	control.menu.touch(1, late.Add(time.Minute))
	tickEvery(t, control, late.Add(time.Minute), menuTickEvery, 5*time.Second)
	if n := len(fake.byMethod("editMessageText")) - before; n < 3 {
		t.Fatalf("после нажатия правок %d", n)
	}
}

// TestMenuRateLimitLadder — на каждый 429 бот обновляет реже на ступень, об этом сказано в меню,
// а новый выбор периода возвращает выбранную частоту.
func TestMenuRateLimitLadder(t *testing.T) {
	fake, _, control := menuControl(t)
	control.handleUpdate(message(1, "меню", 0))
	control.menu.update(1, func(e *menuEntry) { e.Interval = 1 })
	t0 := time.Now()
	control.menu.touch(1, t0)
	control.menuTick(t.Context(), t0) // назначает срок

	fake.mu.Lock()
	fake.failCode, fake.failDescription = 429, "Too Many Requests: retry after 1"
	fake.mu.Unlock()
	fake.setFail("editMessageText", true)

	floor := func() time.Duration {
		control.menu.mu.Lock()
		defer control.menu.mu.Unlock()
		return control.menu.floor[1]
	}
	next := func() time.Time {
		control.menu.mu.Lock()
		defer control.menu.mu.Unlock()
		return control.menu.nextAt[1]
	}
	var seen []time.Duration
	now := t0
	for i := 0; i < 5; i++ {
		now = next()
		control.menuTick(t.Context(), now)
		seen = append(seen, floor())
	}
	want := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 30 * time.Second}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("ступени после 429: %v, ожидали %v", seen, want)
		}
	}
	entry, _ := control.menu.get(1)
	if footer := control.menuFooter(1, entry, now); !strings.Contains(footer, "частота снижена") || !strings.Contains(footer, "каждые 30 с") {
		t.Fatalf("подпись после снижения: %s", footer)
	}

	// Telegram снова принимает правки; человек сам выбирает период — вынужденное снижение снимается.
	fake.setFail("editMessageText", false)
	menuCallback(control, fake, t, "iv1")
	if floor() != 0 {
		t.Fatal("вынужденное снижение не сброшено после выбора периода")
	}
	entry, _ = control.menu.get(1)
	if footer := control.menuFooter(1, entry, now); strings.Contains(footer, "снижена") {
		t.Fatalf("подпись всё ещё говорит о снижении: %s", footer)
	}
	// Сам по себе обычный период выше ступени снижения не меняется: 429 при 10 с даёт 30 с.
	control.menu.update(1, func(e *menuEntry) { e.Interval = 60 })
	control.menu.resetFloor(1)
	if got := control.menu.effectiveInterval(1, 60, now); got != time.Minute {
		t.Fatalf("минутный период изменился: %v", got)
	}
}
