package telegram

import (
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
	if n := HTMLVisibleLen(src); n != 16 {
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
		if got := HTMLPrefix(src, c.visible, c.toWord); got != c.want {
			t.Errorf("htmlPrefix(%d, %t) = %q, want %q", c.visible, c.toWord, got, c.want)
		}
	}
	if got := HTMLPrefix("<pre>abc</pre>", 2, false); got != "<pre>ab</pre>" {
		t.Errorf("незакрытый тег: %q", got)
	}
	// Для любой длины открытые и закрытые теги сходятся, а сущность целая.
	long := "<b>Заголовок</b>\n<pre>строка &lt;1&gt; &amp; <i>вторая</i> строка</pre> конец"
	for n := 0; n <= HTMLVisibleLen(long)+2; n++ {
		got := HTMLPrefix(long, n, n%2 == 0)
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
		want   IntentKind
	}{
		{"меню", IntentMenu},
		{"главное меню", IntentMenu},
		{"открой меню", IntentMenu},
		{"дашборд", IntentMenu},
		{"статус", IntentStatus},
		{"как дела?", IntentStatus},
		{"бот, как ты там", IntentStatus},
		{"что с ботом", IntentStatus},
		{"обнови статус", IntentStatus},
		{"покажи логи", IntentLogs},
		{"логи 30", IntentLogs},
		{"логи updates", IntentLogs},
		{"последние предупреждения", IntentLogs},
		{"покажи ошибки", IntentErrors},
		{"что упало", IntentErrors},
		{"что случилось", IntentErrors},
		{"какие были ошибки за час", IntentErrors},
		{"найди таймаут", IntentSearch},
		{"поищи connection reset", IntentSearch},
		{"перезапусти", IntentRestart},
		{"перезапусти бота пожалуйста", IntentRestart},
		{"останови бота", IntentStop},
		{"выключи бота", IntentStop},
		{"запусти", IntentRun},
		{"включи бота", IntentRun},
		{"обнови", IntentUpdate},
		{"есть обновления?", IntentUpdate},
		{"какая версия", IntentUpdate},
		{"тишина на час", IntentMute},
		{"помолчи до утра", IntentMute},
		{"не беспокой меня 2 часа", IntentMute},
		{"выключи уведомления на 30 минут", IntentMute},
		{"включи уведомления", IntentUnmute},
		{"размуть", IntentUnmute},
		{"помощь", IntentHelp},
		{"что ты умеешь", IntentHelp},
		{"привет", IntentHello},
		{"спасибо", IntentThanks},
		{"да", IntentYes},
		{"ага", IntentYes},
		{"нет", IntentNo},
		{"отмена", IntentNo},
		{"стоп", IntentNo},
		{"не перезапускай", IntentCancel},
		{"не надо останавливать", IntentCancel},
		{"открой панель", IntentApp},
		{"абракадабра", IntentFallback},
	}
	for _, c := range cases {
		got, ok := ParseIntent(c.phrase)
		if !ok || got.Kind != c.want {
			t.Errorf("%q → %q, ожидали %q", c.phrase, got.Kind, c.want)
		}
	}

	// Параметры из фразы.
	it, _ := ParseIntent("логи 30")
	if it.Limit != 30 {
		t.Errorf("логи 30: limit=%d", it.Limit)
	}
	it, _ = ParseIntent("какие были ошибки за 2 часа")
	if it.Kind != IntentErrors || it.Since != 2*time.Hour {
		t.Errorf("ошибки за 2 часа: %+v", it)
	}
	it, _ = ParseIntent("последние предупреждения")
	if it.Level != "WARNING" {
		t.Errorf("предупреждения: level=%q", it.Level)
	}
	it, _ = ParseIntent("найди connection reset")
	if it.Text != "connection reset" {
		t.Errorf("поиск: text=%q", it.Text)
	}
	it, _ = ParseIntent("выключи уведомления совсем")
	if it.Kind != IntentMute || !it.MuteAll {
		t.Errorf("совсем: %+v", it)
	}
	it, _ = ParseIntent("не беспокой меня 2 часа")
	if !it.MuteSet || it.Mute != 2*time.Hour {
		t.Errorf("2 часа: %+v", it)
	}
	if name := ModuleFrom([]string{"логи", "updates"}, []string{"heroku.core", "heroku.updates"}); name != "heroku.updates" {
		t.Errorf("модуль по слову: %q", name)
	}
	if name := ModuleFrom([]string{"логи"}, []string{"heroku.core"}); name != "" {
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
		got, ok := ParseMuteDuration(phrase, now)
		if !ok || got != want {
			t.Errorf("%q → %v (%t), ожидали %v", phrase, got, ok, want)
		}
	}
	if _, ok := ParseMuteDuration("помолчи", now); ok {
		t.Error("длительности нет, но она нашлась")
	}
}

// TestMuteStore проверяет срок тишины, серьёзность событий и сохранение между запусками.
func TestMuteStore(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "mute.json")
	m := NewMuteStore(path, clock)
	if _, ok := m.Active(); ok || m.Silences("warning") {
		t.Fatal("тишина включена без просьбы")
	}
	if err := m.Set(now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if !m.Silences("warning") || !m.Silences("ok") {
		t.Error("обычные события должны молчать")
	}
	if m.Silences("critical") {
		t.Error("критичное событие должно пробиваться")
	}
	// После перезапуска панели тишина сохраняется.
	if _, ok := NewMuteStore(path, clock).Active(); !ok {
		t.Error("тишина не пережила перезапуск")
	}
	// По истечении срока тишина снимается сама.
	now = now.Add(2 * time.Hour)
	if _, ok := m.Active(); ok {
		t.Error("тишина не истекла")
	}
	if err := m.Set(now.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if !m.Silences("critical") {
		t.Error("«совсем» должно глушить и критичное")
	}
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewMuteStore(path, clock).Active(); ok {
		t.Error("снятая тишина вернулась")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл состояния: %v", err)
	}
}
