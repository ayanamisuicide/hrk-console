package tui

import (
	"strings"
	"testing"

	"heroku-console/internal/logfeed"
)

// feed разбирает строки лога и складывает готовые записи на экран — ровно
// тем же путём, каким это делает вьюер.
func feed(s *screen, lines ...string) {
	p := logfeed.NewParser()
	for _, l := range lines {
		if rec, complete := p.Feed(l); complete {
			s.add(rec)
		}
	}
}

func TestScreenCollapsesRepeats(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s,
		"2026-07-27 00:12:03 [ERROR] urllib3: Connection refused",
		"2026-07-27 00:12:05 [ERROR] urllib3: Connection refused",
		"2026-07-27 00:12:07 [ERROR] urllib3: Connection refused",
	)

	if len(s.blocks) != 1 {
		t.Fatalf("три повтора должны стать одним блоком, got %d", len(s.blocks))
	}
	if got := s.blocks[0].rec.Count; got != 3 {
		t.Errorf("счётчик: got %d, want 3", got)
	}
	if !strings.Contains(s.text(), "×3") {
		t.Error("в тексте должен быть счётчик ×3")
	}
	// Время — от последнего повтора: в живом логе важно, когда это было
	// в последний раз, а не когда серия началась.
	if got := s.blocks[0].rec.Time; got != "00:12:07" {
		t.Errorf("время: got %q, want 00:12:07", got)
	}
}

func TestScreenKeepsDifferentEntriesApart(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s,
		"2026-07-27 00:12:03 [ERROR] urllib3: Connection refused",
		"2026-07-27 00:12:05 [ERROR] urllib3: Connection reset",
		"2026-07-27 00:12:07 [ERROR] urllib3: Connection refused",
	)
	if len(s.blocks) != 3 {
		t.Fatalf("разные записи не должны схлопываться, got %d блоков", len(s.blocks))
	}
	if strings.Contains(s.text(), "×") {
		t.Error("счётчика быть не должно")
	}
}

// Повтор, разорванный другой записью, начинает серию заново — иначе счётчик
// врал бы про то, что ошибки шли подряд.
func TestScreenRepeatBrokenByOtherEntry(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s,
		"2026-07-27 00:12:03 [ERROR] urllib3: Connection refused",
		"2026-07-27 00:12:04 [INFO] root: всё хорошо",
		"2026-07-27 00:12:05 [ERROR] urllib3: Connection refused",
	)
	if len(s.blocks) != 3 {
		t.Fatalf("серия должна была прерваться, got %d блоков", len(s.blocks))
	}
}

// Баннер перезапуска разрывает серию: одинаковые ошибки до и после
// перезапуска — разные события, и склеивать их нельзя.
func TestScreenBannerBreaksRun(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s, "2026-07-27 00:12:03 [ERROR] urllib3: Connection refused")
	s.addRaw("⟳ ПЕРЕЗАПУСК")
	feed(&s, "2026-07-27 00:13:03 [ERROR] urllib3: Connection refused")

	if len(s.blocks) != 3 {
		t.Fatalf("баннер должен разрывать серию, got %d блоков", len(s.blocks))
	}
}

func TestScreenProblemLinesFollowCollapse(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s,
		"2026-07-27 00:12:01 [INFO] root: первая",
		"2026-07-27 00:12:02 [INFO] root: вторая",
		"2026-07-27 00:12:03 [ERROR] urllib3: Connection refused",
		"2026-07-27 00:12:04 [ERROR] urllib3: Connection refused",
	)
	problems := s.problemLines()
	if len(problems) != 1 {
		t.Fatalf("схлопнутая серия — одна проблема, got %d", len(problems))
	}
	if problems[0] != 2 {
		t.Errorf("позиция проблемы: got %d, want 2", problems[0])
	}
	if got := s.totalLines(); got != 3 {
		t.Errorf("высота содержимого: got %d, want 3", got)
	}
}

// Обрезка истории считает показанные записи, а не сырые строки: серия из
// сотни повторов занимает на экране одну запись и один слот истории.
func TestScreenTrimCountsCollapsedEntries(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s, "2026-07-27 00:12:00 [INFO] root: старая")
	for i := 0; i < 50; i++ {
		feed(&s, "2026-07-27 00:12:03 [ERROR] urllib3: Connection refused")
	}
	feed(&s, "2026-07-27 00:12:59 [INFO] root: свежая")

	if len(s.blocks) != 3 {
		t.Fatalf("ожидалось 3 блока до обрезки, got %d", len(s.blocks))
	}
	s.trimTo(2)
	if len(s.blocks) != 2 {
		t.Fatalf("после обрезки: got %d блоков", len(s.blocks))
	}
	if !strings.Contains(s.text(), "×50") {
		t.Error("схлопнутая серия должна пережить обрезку целиком")
	}
}

// Зебра красит запись целиком и должна чередоваться по записям — в том
// числе через обрезку истории, иначе первая живая строка ложится той же
// полосой, что последняя строка истории.
func TestScreenZebraAlternatesAcrossTrim(t *testing.T) {
	var s screen
	s.reset(100)
	for _, msg := range []string{"первая", "вторая", "третья"} {
		feed(&s, "2026-07-27 00:12:00 [INFO] root: "+msg)
	}
	s.trimTo(1)
	last := s.blocks[len(s.blocks)-1].zebra
	feed(&s, "2026-07-27 00:12:10 [INFO] root: живая")
	if s.blocks[len(s.blocks)-1].zebra == last {
		t.Error("после обрезки зебра должна продолжить чередование")
	}
}

// Баннеры перезапуска должны быть находимы отдельно от проблем: r/R прыгают
// по ним, и позиции считаются по тем же блокам, что и всё остальное.
func TestScreenBannerLines(t *testing.T) {
	var s screen
	s.reset(100)
	feed(&s, "2026-07-27 00:12:01 [INFO] root: до")
	s.addRaw("⟳ ПЕРЕЗАПУСК")
	feed(&s, "2026-07-27 00:12:02 [INFO] root: после")

	lines := s.bannerLines()
	if len(lines) != 1 {
		t.Fatalf("баннеров: got %d, want 1", len(lines))
	}
	if lines[0] != 1 {
		t.Errorf("позиция баннера: got %d, want 1", lines[0])
	}
	// Баннер не запись — в статистику модулей он попадать не должен.
	for _, m := range s.moduleStats() {
		if m.name == "" {
			t.Error("баннер просочился в статистику модулей")
		}
	}
}
