package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDigestSchedule: время сводки из настроек и последний наступивший срок.
func TestDigestSchedule(t *testing.T) {
	cases := map[string][2]int{"": {8, 0}, "08:30": {8, 30}, "7:05": {7, 5}, "23:59": {23, 59}, "25:00": {8, 0}, "мусор": {8, 0}}
	for at, want := range cases {
		if h, m := (telegramSettings{DigestAt: at}).digestAt(); h != want[0] || m != want[1] {
			t.Errorf("digestAt(%q) = %d:%d, want %d:%d", at, h, m, want[0], want[1])
		}
	}
	morning := time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)
	if slot := digestSlot(telegramSettings{}, morning); !slot.Equal(time.Date(2026, 10, 15, 8, 0, 0, 0, time.Local)) {
		t.Fatalf("после 08:00 срок сегодняшний: %v", slot)
	}
	if slot := digestSlot(telegramSettings{}, morning.Add(-2*time.Hour)); !slot.Equal(time.Date(2026, 10, 14, 8, 0, 0, 0, time.Local)) {
		t.Fatalf("до 08:00 срок вчерашний: %v", slot)
	}
	for text, want := range map[string]string{"присылай в 8": "08:00", "присылай сводку в 8:30": "08:30", "к 9.15": "09:15", "в 25": "", "сводку": ""} {
		if got, _ := digestTimeFrom(text); got != want {
			t.Errorf("digestTimeFrom(%q) = %q, want %q", text, got, want)
		}
	}
	for span, want := range map[time.Duration]string{24 * time.Hour: "сутки", 7 * 24 * time.Hour: "неделю", 12 * time.Hour: "12 ч", 72 * time.Hour: "3 дня"} {
		if got := spanLabel(span); got != want {
			t.Errorf("spanLabel(%v) = %q, want %q", span, got, want)
		}
	}
}

// writePanelEvents пишет в журнал панели падение и запуск бота, как их пишет recordPanelEvent.
func writePanelEvents(t *testing.T, s *server, crashAt time.Time) {
	t.Helper()
	s.panelLog = newPanelJournal(filepath.Join(t.TempDir(), "panel.log"))
	s.panelLog.write(crashAt, "ERROR", modulePanel, "Бот упал — ImportError: cannot import name x", nil)
	s.panelLog.write(crashAt.Add(3*time.Minute), "INFO", modulePanel, "Бот запущен — Процесс Heroku работает.", nil)
	s.panelLog.write(crashAt.Add(10*time.Minute), "WARNING", modulePanel, "Бот остановлен — Остановил @anya из Telegram в 12:00", nil)
}

// TestDigestContent: падение с причиной и простоем, ручная остановка, ошибки и самые частые из них.
func TestDigestContent(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	_ = fake
	journalFor(t, s)
	now := time.Now()
	writePanelEvents(t, s, now.Add(-2*time.Hour))
	if err := s.audit.add(auditEvent{Time: now.Add(-time.Hour).UTC(), Actor: "telegram:@anya", Action: "bot.restart", IP: "telegram"}); err != nil {
		t.Fatal(err)
	}
	text, keyboard, next := control.buildDigest(now.Add(-24*time.Hour), now, true, digestState{BotVersion: "1.0.0", PanelVersion: "v0.1.0"})
	for _, want := range []string{"Доброе утро! Сводка с", "Бот падал 1 раз, остановок вручную 1", "ImportError: cannot import name x · простоял 3 мин",
		"Остановил @anya из Telegram", "Перезапуски вручную: @anya из Telegram в", "Ошибок <b>3</b>", "×2 manifest failed 2 · updates", "×1 boom · core",
		"Сейчас бот не работает", "Панель обновлена: v0.1.0 → "} {
		if !strings.Contains(text, want) {
			t.Errorf("в сводке нет %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, leakedSecret) {
		t.Fatalf("секрет в сводке:\n%s", text)
	}
	callbackData(t, keyboard, "nx:e")
	callbackData(t, keyboard, "nx:m")
	if next.PanelVersion != currentVersion().Version {
		t.Fatalf("следующее состояние: %+v", next)
	}
	// По запросу — свой заголовок, без «доброго утра».
	text, _, _ = control.buildDigest(now.Add(-7*24*time.Hour), now, false, digestState{})
	if !strings.HasPrefix(text, "📰 <b>Сводка за неделю</b>") {
		t.Fatalf("сводка по запросу:\n%s", text)
	}
}

// TestDigestSentOncePerMorning: утренняя сводка уходит администратору один раз за срок, не уходит
// при выключенной сводке или управлении и после слишком долгого опоздания.
func TestDigestSentOncePerMorning(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	s.dataDir = t.TempDir()
	now := time.Now()
	control.now = func() time.Time { return now }
	at := now.Add(-time.Hour).Format("15:04")
	set := func(settings telegramSettings) {
		t.Helper()
		if err := s.operations.setTelegram(settings); err != nil {
			t.Fatal(err)
		}
	}
	set(telegramSettings{ControlEnabled: false, DigestAt: at})
	control.digestTick(t.Context())
	if len(fake.byMethod("sendMessage")) != 0 {
		t.Fatal("сводка без включённого управления")
	}
	set(telegramSettings{ControlEnabled: true, DigestAt: at, DigestOff: true})
	control.digestTick(t.Context())
	if len(fake.byMethod("sendMessage")) != 0 {
		t.Fatal("выключенная сводка пришла")
	}
	set(telegramSettings{ControlEnabled: true, DigestAt: at})
	control.digestTick(t.Context())
	control.digestTick(t.Context())
	sent := fake.byMethod("sendMessage")
	if len(sent) != 1 || sent[0]["chat_id"] != float64(1) || !strings.Contains(sent[0]["text"].(string), "Доброе утро") {
		t.Fatalf("утренняя сводка: %v", sent)
	}
	if state := control.loadDigestState(); !state.LastSent.Equal(now) {
		t.Fatalf("время отправки не сохранено: %+v", state)
	}
	// Следующие сутки: снова ровно одна.
	now = now.Add(24 * time.Hour)
	control.digestTick(t.Context())
	if len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("сводка следующего дня не пришла")
	}
	// Панель лежала всё утро: опоздавшую больше чем на 6 часов сводку не шлём.
	now = now.Add(24*time.Hour + 7*time.Hour)
	control.digestTick(t.Context())
	if len(fake.byMethod("sendMessage")) != 2 {
		t.Fatal("сводка пришла с опозданием в 7 часов")
	}
}

// TestDigestFromChat: сводка по запросу и настройка утренней фразами и командой.
func TestDigestFromChat(t *testing.T) {
	fake, api := newFakeBotAPI(t)
	s, control := newTestTelegram(t, api.URL, 1)
	cases := []struct{ phrase, want string }{
		{"сводка", "Сводка за сутки"},
		{"/digest", "Сводка за сутки"},
		{"что было за ночь", "Сводка за 12 ч"},
		{"сводка за неделю", "Сводка за неделю"},
		{"что было за 3 часа", "Сводка за 3 ч"},
	}
	for _, c := range cases {
		control.handleUpdate(message(1, c.phrase, 0))
		if text := lastSent(t, fake)["text"].(string); !strings.Contains(text, c.want) {
			t.Errorf("%q: %s", c.phrase, text)
		}
	}
	control.handleUpdate(message(1, "присылай сводку в 8:30", 0))
	if settings := s.operations.telegramSettings(); settings.DigestOff || settings.DigestAt != "08:30" || !strings.Contains(lastSent(t, fake)["text"].(string), "каждый день в 08:30") {
		t.Fatalf("включение: %+v / %v", settings, lastSent(t, fake)["text"])
	}
	for _, phrase := range []string{"не присылай сводку", "выключи сводку"} {
		control.handleUpdate(message(1, phrase, 0))
		if settings := s.operations.telegramSettings(); !settings.DigestOff || settings.DigestAt != "08:30" {
			t.Fatalf("%q: %+v", phrase, settings)
		}
		if !strings.Contains(lastSent(t, fake)["text"].(string), "выключена") {
			t.Fatalf("%q: %v", phrase, lastSent(t, fake)["text"])
		}
		// «Выключи сводку» не должно останавливать бота, а «не присылай» — включать тишину.
		if len(fake.byMethod("editMessageText")) != 0 {
			t.Fatalf("%q задело что-то кроме сводки", phrase)
		}
		if _, muted := control.mute.Active(); muted {
			t.Fatalf("%q включило тишину", phrase)
		}
	}
	view := control.digestView()
	if view["enabled"] != false || view["at"] != "08:30" {
		t.Fatalf("состояние для админки: %v", view)
	}
}

// TestDigestSettingsValidated: админка не сохраняет время не в виде ЧЧ:ММ и показывает состояние сводки.
func TestDigestSettingsValidated(t *testing.T) {
	f := newFlowServer(t)
	var err error
	f.operations, err = openOperationStore(f.dataDir + "/operations.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body string
		code int
	}{{`{"digestAt":"8:30"}`, http.StatusOK}, {`{"digestAt":"завтра"}`, http.StatusBadRequest}, {`{"digestOff":true}`, http.StatusOK}} {
		want(t, c.body, f.do(t, flowRequest{method: "PUT", path: "/api/admin/telegram", body: c.body, admin: true}), c.code)
	}
	if settings := f.operations.telegramSettings(); settings.DigestAt != "8:30" || !settings.DigestOff {
		t.Fatalf("сохранено: %+v", settings)
	}
	read := f.do(t, flowRequest{method: "GET", path: "/api/admin/telegram", admin: true})
	digest, ok := decode(t, read)["digest"].(map[string]any)
	if !ok || digest["enabled"] != false || digest["at"] != "08:30" {
		t.Fatalf("сводка в ответе админке: %s", read.Body.String())
	}
}
