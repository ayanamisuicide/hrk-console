package logfeed

import "testing"

func TestParseBasicRecord(t *testing.T) {
	p := NewParser()
	rec, complete := p.Feed("2026-07-27 00:12:03 [DEBUG] git.util: sys.platform='linux'")
	if !complete || rec == nil {
		t.Fatal("запись должна быть готова сразу")
	}
	if rec.Time != "00:12:03" {
		t.Errorf("время: got %q", rec.Time)
	}
	if rec.Level != LevelDebug {
		t.Errorf("уровень: got %v", rec.Level)
	}
	if rec.Module != "git.util" {
		t.Errorf("модуль: got %q", rec.Module)
	}
	if rec.Lines[0] != "sys.platform='linux'" {
		t.Errorf("сообщение: got %q", rec.Lines[0])
	}
}

// Стартовый баннер бота приходит как "root: " с пустым сообщением плюс
// отдельная физическая строка с текстом. Он должен собраться в ОДНУ запись,
// а не в пустую шапку плюс висящее продолжение.
func TestParseMultilineBanner(t *testing.T) {
	p := NewParser()
	if rec, complete := p.Feed("2026-07-27 00:12:10 [DEBUG] root: "); complete || rec != nil {
		t.Fatal("запись с пустым сообщением должна придерживаться до продолжения")
	}
	rec, complete := p.Feed("🪐 Heroku 2.2.2 #b9f2cb6 started")
	if !complete || rec == nil {
		t.Fatal("продолжение должно завершить придержанную запись")
	}
	if rec.Time != "00:12:10" {
		t.Errorf("продолжение должно унаследовать время заголовка, got %q", rec.Time)
	}
	if rec.Module != "root" {
		t.Errorf("модуль: got %q", rec.Module)
	}
	if rec.Lines[0] != "🪐 Heroku 2.2.2 #b9f2cb6 started" {
		t.Errorf("сообщение: got %q", rec.Lines[0])
	}
}

func TestShortModule(t *testing.T) {
	cases := map[string]string{
		"urllib3.connectionpool":         "urllib3",
		"heroku.modules.api_protection":  "api_protection",
		"heroku.tl_cache":                "tl_cache",
		"git.cmd":                        "git.cmd",
		"telethon.network.mtprotosender": "telethon",
	}
	for in, want := range cases {
		if got := shortModule(in); got != want {
			t.Errorf("shortModule(%q) = %q, want %q", in, got, want)
		}
	}
}

// Скрытие DEBUG — главное правило вьюера: 96% живого лога это шум от
// urllib3/git/tl_cache, но собственный голос бота (root) и баннер версии
// должны оставаться видимыми даже при скрытом DEBUG.
func TestVisibleDebugRules(t *testing.T) {
	feed := func(line string) *Record {
		p := NewParser()
		rec, _ := p.Feed(line)
		return rec
	}

	noise := feed("2026-07-27 00:12:03 [DEBUG] urllib3.connectionpool: GET /v1/me/player 204")
	if Visible(noise, false, "", LevelDebug) {
		t.Error("шум urllib3 не должен быть виден при скрытом DEBUG")
	}
	if !Visible(noise, true, "", LevelDebug) {
		t.Error("при показанном DEBUG шум должен быть виден")
	}

	root := feed("2026-07-27 00:12:10 [DEBUG] root: Got DB")
	if !Visible(root, false, "", LevelDebug) {
		t.Error("голос бота (root) должен быть виден даже при скрытом DEBUG")
	}

	// heroku.loader НЕ должен считаться "громким": на каждом старте он
	// печатает по две строки на каждый из полусотни модулей — это ровно тот
	// шум, ради скрытия которого прячут DEBUG.
	loader := feed("2026-07-27 00:12:11 [DEBUG] heroku.loader: Loading heroku.modules.eval from filesystem")
	if Visible(loader, false, "", LevelDebug) {
		t.Error("heroku.loader на уровне DEBUG не должен быть виден при скрытом DEBUG")
	}

	warn := feed("2026-07-27 00:13:04 [WARNING] heroku.modules.spotify: retry")
	if !Visible(warn, false, "", LevelDebug) {
		t.Error("WARNING виден всегда")
	}
}

func TestVisibleFilter(t *testing.T) {
	p := NewParser()
	rec, _ := p.Feed("2026-07-27 00:13:04 [WARNING] heroku.modules.spotify: Token refresh")

	if !Visible(rec, false, "spotify", LevelDebug) {
		t.Error("фильтр должен находить по имени модуля")
	}
	if !Visible(rec, false, "TOKEN", LevelDebug) {
		t.Error("фильтр должен быть нечувствителен к регистру")
	}
	if !Visible(rec, false, "refresh", LevelDebug) {
		t.Error("фильтр должен находить по тексту сообщения")
	}
	if Visible(rec, false, "urllib3", LevelDebug) {
		t.Error("несовпадающий фильтр должен скрывать запись")
	}
	if !Visible(rec, false, "", LevelDebug) {
		t.Error("пустой фильтр показывает всё")
	}
}

func TestVisibleFilterRegex(t *testing.T) {
	p := NewParser()
	rec, _ := p.Feed("2026-07-27 00:13:04 [WARNING] heroku.modules.spotify: Connection reset by peer")

	if !Visible(rec, false, "re:reset by peer$", LevelDebug) {
		t.Error("regex-фильтр должен находить по концу строки")
	}
	if Visible(rec, false, "re:^refused", LevelDebug) {
		t.Error("regex-фильтр не должен находить непойманное")
	}
	if !Visible(rec, false, "re:CONNECTION", LevelDebug) {
		t.Error("regex-фильтр должен быть нечувствителен к регистру")
	}
	if Visible(rec, false, "re:(", LevelDebug) {
		t.Error("битый regex должен трактоваться как не совпало, а не падать")
	}
}

func TestCounts(t *testing.T) {
	p := NewParser()
	p.Feed("2026-07-27 00:13:04 [WARNING] a.b: w1")
	p.Feed("2026-07-27 00:13:05 [ERROR] a.b: e1")
	p.Feed("2026-07-27 00:13:06 [CRITICAL] a.b: e2")
	p.Feed("2026-07-27 00:13:07 [INFO] a.b: ok")
	w, e := p.Counts()
	if w != 1 {
		t.Errorf("warn = %d, want 1", w)
	}
	if e != 2 {
		t.Errorf("err = %d, want 2 (ERROR + CRITICAL)", e)
	}
}

func TestSameEntryIgnoresTime(t *testing.T) {
	p := NewParser()
	a, _ := p.Feed("2026-07-27 00:12:03 [ERROR] urllib3: Connection refused")
	b, _ := p.Feed("2026-07-27 00:12:31 [ERROR] urllib3: Connection refused")
	if !SameEntry(a, b) {
		t.Error("одинаковые записи с разным временем должны считаться повтором")
	}
}

func TestSameEntryDistinguishes(t *testing.T) {
	p := NewParser()
	base, _ := p.Feed("2026-07-27 00:12:03 [ERROR] urllib3: Connection refused")

	cases := map[string]string{
		"другой текст":   "2026-07-27 00:12:04 [ERROR] urllib3: Connection reset",
		"другой уровень": "2026-07-27 00:12:04 [WARNING] urllib3: Connection refused",
		"другой модуль":  "2026-07-27 00:12:04 [ERROR] telethon: Connection refused",
	}
	for name, line := range cases {
		q := NewParser()
		other, _ := q.Feed(line)
		if SameEntry(base, other) {
			t.Errorf("%s: записи не должны схлопываться", name)
		}
	}
}

// Кадры трейсбека приходят без времени и в рекурсии совпадают дословно.
// Схлопнуть их значило бы соврать про глубину стека.
func TestSameEntryNeverMergesContinuations(t *testing.T) {
	p := NewParser()
	p.Feed("2026-07-27 00:12:03 [ERROR] heroku: Traceback (most recent call last):")
	a, _ := p.Feed(`  File "main.py", line 42, in tick`)
	b, _ := p.Feed(`  File "main.py", line 42, in tick`)
	if a.Time != "" || b.Time != "" {
		t.Fatal("продолжения должны быть без времени")
	}
	if SameEntry(a, b) {
		t.Error("продолжения не должны схлопываться")
	}
}

// minLevel — отдельная от DEBUG ручка: "оставить только проблемы". Нулевое
// значение (LevelDebug) не должно отсекать ничего, иначе фильтр включался
// бы сам собой у любого, кто не задал его явно.
func TestVisibleMinLevel(t *testing.T) {
	p := NewParser()
	info, _ := p.Feed("2026-07-27 00:12:01 [INFO] root: обычная жизнь")
	warn, _ := p.Feed("2026-07-27 00:12:02 [WARNING] spotify: медленно")
	errRec, _ := p.Feed("2026-07-27 00:12:03 [ERROR] tl_cache: не нашёл")

	for _, rec := range []*Record{info, warn, errRec} {
		if !Visible(rec, true, "", LevelDebug) {
			t.Errorf("нулевая граница не должна ничего отсекать, скрылась %v", rec.Level)
		}
	}
	if Visible(info, true, "", LevelWarning) {
		t.Error("на границе WARNING запись INFO должна скрываться")
	}
	if !Visible(warn, true, "", LevelWarning) {
		t.Error("на границе WARNING сам warning должен остаться")
	}
	if Visible(warn, true, "", LevelError) {
		t.Error("на границе ERROR warning должен скрываться")
	}
	if !Visible(errRec, true, "", LevelError) {
		t.Error("на границе ERROR ошибка должна остаться")
	}
}

// Уровни идут по возрастанию важности — на этом стоит и фильтр по уровню,
// и признак «проблемная запись» (>= LevelWarning) во вьюере.
func TestLevelOrder(t *testing.T) {
	if LevelDebug >= LevelInfo || LevelInfo >= LevelWarning ||
		LevelWarning >= LevelError || LevelError >= LevelCritical {
		t.Error("порядок уровней нарушен")
	}
}
