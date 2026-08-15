// Package logfeed разбирает сырые строки heroku.log в записи и решает,
// что из этого показывать. Правила видимости перенесены из прежней
// bash-версии (render.sh) без изменений по смыслу.
package logfeed

import (
	"regexp"
	"strings"
)

// Level — уровень записи лога.
type Level int

// Порядок значим: уровни идут по возрастанию важности, и сравнение
// уровней — это сравнение важности. Отсюда же берётся "нижняя граница
// показа" в Visible, у которой нулевое значение (LevelDebug) означает
// "показывать всё".
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarning
	LevelError
	LevelCritical
)

func parseLevel(s string) Level {
	switch s {
	case "DEBUG":
		return LevelDebug
	case "WARNING":
		return LevelWarning
	case "ERROR":
		return LevelError
	case "CRITICAL":
		return LevelCritical
	default:
		return LevelInfo
	}
}

// Record — одна логическая запись лога, уже собранная из всех своих
// физических строк (заголовок + возможные переносы/трейсбек).
type Record struct {
	Date       string // "2026-08-15", то же условие пустоты, что у Time
	Time       string // "15:04:05", пусто у мягкого переноса без заголовка
	Level      Level
	Module     string   // сокращённое имя, уже прошедшее shortModule — для показа
	fullModule string   // исходное имя из лога — для проверки loudModules
	Lines      []string // первая — само сообщение, остальные — трейсбек/дамп
	Hard       []bool   // Hard[i] — перенос i-й строки физический (был в файле)
	Warn       bool
	Err        bool

	// Count — сколько одинаковых записей подряд схлопнуто в эту. 0 и 1
	// значат одно и то же: запись пришла один раз. Считает не парсер, а
	// тот, кто собирает экран, — схлопывание касается только показа.
	Count int
}

// SameEntry отвечает, можно ли считать две записи повторами одной и той же:
// уровень, модуль и текст совпадают, время игнорируется — оно у повтора
// всегда своё, ради него схлопывание и делается.
//
// Продолжения (Time == "") не схлопываются никогда. В трейсбеке рекурсии
// соседние кадры совпадают дословно, и склеить их значило бы соврать про
// глубину стека — а это ровно то, ради чего трейсбек читают.
func SameEntry(a, b *Record) bool {
	if a == nil || b == nil || a.Time == "" || b.Time == "" {
		return false
	}
	if a.Level != b.Level || a.fullModule != b.fullModule || len(a.Lines) != len(b.Lines) {
		return false
	}
	for i := range a.Lines {
		if a.Lines[i] != b.Lines[i] {
			return false
		}
	}
	return true
}

// Дата — своя группа (была частью незахваченного префикса): нужна для
// полной метки времени в подсказке GUI над короткими "15:04:05" в строке —
// heroku.log живёт неделями, и без даты не отличить "сегодня" от "три дня
// назад" на глаз.
var logRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}) \[([A-Z]+)\] ([^ :]+): ?(.*)$`)

// Что видно всегда, даже когда DEBUG скрыт. Формально эти записи пишутся
// уровнем DEBUG, но по смыслу они и есть то, ради чего открывают лог.
//
// По модулю — только root: голос самого бота, за сессию он выдаёт несколько
// строк (Got DB, баннер версии, префикс). heroku.loader сюда нельзя: он на
// каждом старте печатает по две строки на каждый из полусотни модулей
// ("Loading ... from filesystem"), и это ровно тот шум, от которого прячут
// DEBUG.
var loudModules = regexp.MustCompile(`^(root|heroku\.main|heroku\.__main__)$`)

// По тексту — баннер версии, приезжает продолжением строки от root.
// "Reloaded N commands" сюда не годится, хотя и просится: бот пересобирает
// модули каждые 30 секунд, это heartbeat, а не событие.
var loudMessages = regexp.MustCompile(`^(Heroku |🪐)`)

var moduleAbbrev = []struct {
	prefix string
	repl   string
}{
	{"urllib3.", "urllib3"},
	{"telethon.", "telethon"},
	{"asyncio.", "asyncio"},
	{"herokutl.", "herokutl"},
}

// shortModule сокращает длинные точечные пути: важно "какая подсистема",
// а не полный питоновский путь до неё.
func shortModule(m string) string {
	for _, a := range moduleAbbrev {
		if strings.HasPrefix(m, a.prefix) {
			m = a.repl
			break
		}
	}
	m = strings.TrimPrefix(m, "heroku.modules.")
	m = strings.TrimPrefix(m, "heroku.")
	return m
}

// Parser хранит состояние построчного разбора между вызовами Feed —
// многострочные записи (трейсбек, стартовый баннер) приезжают отдельными
// физическими строками, и парсер должен помнить, к какой записи они
// относятся.
type Parser struct {
	pending *Record // запись с пустым сообщением ждёт своего продолжения
	lastLvl Level
	lastMod string
	warn    int
	err     int
}

func NewParser() *Parser { return &Parser{} }

func (p *Parser) Counts() (warn, err int) { return p.warn, p.err }

// SetCounts переносит уже накопленные счётчики (например, при пересборке
// экрана из буфера — строки проигрываются повторно, и без переноса каждая
// перерисовка удваивала бы warn/err).
func (p *Parser) SetCounts(warn, err int) { p.warn, p.err = warn, err }

// Feed разбирает одну сырую строку. Возвращает готовую запись и true, если
// строка завершает запись (готова к отображению) — иначе запись ещё
// копится (ждёт продолжения) и rec равен nil.
func (p *Parser) Feed(raw string) (rec *Record, complete bool) {
	if m := logRe.FindStringSubmatch(raw); m != nil {
		date, t, lvlStr, mod, msg := m[1], m[2], m[3], m[4], m[5]
		lvl := parseLevel(lvlStr)
		p.lastLvl = lvl
		p.lastMod = mod
		switch lvl {
		case LevelWarning:
			p.warn++
		case LevelError, LevelCritical:
			p.err++
		}

		if msg == "" {
			// Пустое сообщение — заголовок многострочной записи (тот самый
			// "root: " перед баннером). Придерживаем: текст приедет следующей
			// строкой и встанет в ту же запись, а не разложится на пустую
			// шапку плюс висящее продолжение.
			p.pending = &Record{Date: date, Time: t, Level: lvl, Module: shortModule(mod), fullModule: mod}
			return nil, false
		}
		rec = &Record{Date: date, Time: t, Level: lvl, Module: shortModule(mod), fullModule: mod, Lines: []string{msg}, Hard: []bool{false}}
		p.pending = nil
		rec.Warn = lvl == LevelWarning
		rec.Err = lvl == LevelError || lvl == LevelCritical
		return rec, true
	}

	// Продолжение предыдущей записи.
	if p.pending != nil {
		rec := p.pending
		p.pending = nil
		rec.Lines = []string{raw}
		rec.Hard = []bool{false}
		return rec, true
	}
	// Физический перенос внутри уже показанной записи (трейсбек, дамп).
	// Level/fullModule наследуются от родителя — так DEBUG-видимость
	// совпадает с решением, принятым для самой записи, а не
	// пересчитывается по одной строке трейсбека отдельно.
	return &Record{Time: "", Level: p.lastLvl, Module: "", fullModule: p.lastMod, Lines: []string{raw}, Hard: []bool{true}}, true
}

// Visible решает, должна ли запись показываться при текущих правилах.
//
// minLevel отсекает всё тише заданного уровня — это отдельная от showDebug
// ручка: showDebug отвечает на "показывать ли шум", minLevel — на "оставить
// только проблемы". LevelDebug (нулевое значение) не отсекает ничего.
func Visible(rec *Record, showDebug bool, filter string, minLevel Level) bool {
	if rec.Level < minLevel {
		return false
	}
	if rec.Level == LevelDebug && !showDebug {
		loud := loudModules.MatchString(rec.fullModule) ||
			(len(rec.Lines) > 0 && loudMessages.MatchString(rec.Lines[0]))
		if !loud {
			return false
		}
	}
	if filter != "" {
		hay := rec.Module + " " + strings.Join(rec.Lines, " ")
		if !matchFilter(hay, filter) {
			return false
		}
	}
	return true
}

// matchFilter сверяет текст записи с фильтром. Обычно — подстрока без учёта
// регистра, как и раньше. Префикс "re:" переключает в режим регулярного
// выражения (тоже без учёта регистра) — нужен, когда подстроки не хватает:
// например, чтобы отличить "Connection reset" от "Connection refused" одним
// запросом или поймать сообщение по границе слова.
//
// Битый регэксп трактуется как "не совпало", а не паника: опечатка в
// скобках не должна ронять вьюер или превращать его в пустой экран без
// объяснения.
func matchFilter(hay, filter string) bool {
	if rest, ok := strings.CutPrefix(filter, "re:"); ok {
		re, err := regexp.Compile("(?i)" + rest)
		if err != nil {
			return false
		}
		return re.MatchString(hay)
	}
	return strings.Contains(strings.ToLower(hay), strings.ToLower(filter))
}
