package tui

import (
	"sort"
	"strings"

	"heroku-console/internal/logfeed"
)

// screen — накопитель отрисованных блоков экрана. Блок это одна показанная
// запись лога (со всеми её перенесёнными строками) или служебная вставка
// вроде баннера перезапуска.
//
// Собирать текст блоками, а не одной растущей строкой, нужно ради
// схлопывания повторов: повтор дописывает счётчик к УЖЕ показанной записи,
// то есть последний блок надо перерисовать на месте. В склеенной строке
// последний кусок не найти, не запоминая смещений, — а с блоками это просто
// присваивание в конец среза.
//
// Здесь же живут позиции проблемных записей (для прыжков n/N) и чётность
// зебры: и то, и другое считается по блокам, и держать это рядом с ними
// дешевле, чем синхронизировать три параллельных счётчика на стороне вьюера.
type screen struct {
	blocks []screenBlock
	width  int
	rows   int // сквозной номер записи — по его чётности красится зебра
}

type screenBlock struct {
	text    string
	lines   int
	problem bool
	banner  bool
	zebra   bool
	// rec — запись, из которой блок отрисован. nil у служебных вставок:
	// с баннером ничего не схлопывается, и он же разрывает серию повторов.
	rec *logfeed.Record
}

func (s *screen) reset(width int) {
	s.blocks = s.blocks[:0]
	s.width = width
	s.rows = 0
}

// add показывает запись: либо новым блоком, либо счётчиком у предыдущего,
// если запись — дословный повтор. Возвращает true, если изменился последний
// блок, а не добавился новый: живой вьюер по этому признаку решает,
// перерисовать хвост или дописать в конец.
func (s *screen) add(rec *logfeed.Record) (merged bool) {
	if n := len(s.blocks); n > 0 {
		if last := &s.blocks[n-1]; logfeed.SameEntry(last.rec, rec) {
			// Время берём от свежего повтора: в живом логе важнее «когда это
			// было в последний раз», чем когда началась серия.
			last.rec.Count = maxInt(last.rec.Count, 1) + 1
			last.rec.Time = rec.Time
			last.text = renderRecord(last.rec, s.width, last.zebra)
			last.lines = countLines(last.text)
			return true
		}
	}
	zebra := s.rows%2 == 1
	text := renderRecord(rec, s.width, zebra)
	s.blocks = append(s.blocks, screenBlock{
		text:    text,
		lines:   countLines(text),
		problem: rec.Level >= logfeed.LevelWarning,
		zebra:   zebra,
		rec:     rec,
	})
	s.rows++
	return false
}

// addRaw вставляет готовый текст (баннер перезапуска) отдельным блоком.
func (s *screen) addRaw(text string) {
	s.blocks = append(s.blocks, screenBlock{text: text, lines: countLines(text), banner: true})
}

// bannerLines — строки, с которых начинаются баннеры перезапуска. По ним
// прыгают r/R: «что было сразу после последнего рестарта» — второй по
// частоте вопрос к логу после «где ошибка».
func (s *screen) bannerLines() []int {
	var out []int
	line := 0
	for _, b := range s.blocks {
		if b.banner {
			out = append(out, line)
		}
		line += b.lines
	}
	return out
}

// trimTo оставляет последние n записей. Применяется к загруженной истории:
// схлопывание уменьшает число блоков, и обрезать надо после него, иначе
// «40 записей истории» превращались бы в горсть строк, стоило боту
// повториться.
func (s *screen) trimTo(n int) {
	if n <= 0 || len(s.blocks) <= n {
		return
	}
	s.blocks = s.blocks[len(s.blocks)-n:]
	// Зебра должна продолжиться с той чётности, на которой оборвалась
	// обрезка: rows считает и выброшенные блоки, и без правки следующая
	// живая запись могла лечь той же полосой, что последняя из истории.
	if s.blocks[len(s.blocks)-1].zebra {
		s.rows = 0
	} else {
		s.rows = 1
	}
}

func (s *screen) text() string {
	parts := make([]string, len(s.blocks))
	for i, b := range s.blocks {
		parts[i] = b.text
	}
	return strings.Join(parts, "\n")
}

// totalLines — высота содержимого в строках экрана.
func (s *screen) totalLines() int {
	n := 0
	for _, b := range s.blocks {
		n += b.lines
	}
	return n
}

// problemLines — номера строк, с которых начинаются записи WARNING и выше.
func (s *screen) problemLines() []int {
	var out []int
	line := 0
	for _, b := range s.blocks {
		if b.problem {
			out = append(out, line)
		}
		line += b.lines
	}
	return out
}

// modStat — сколько записей дал модуль и было ли среди них плохое.
type modStat struct {
	name  string
	count int
	warn  int
	err   int
}

// moduleStats собирает статистику по показанным записям. Считается по тем же
// блокам, из которых сложен экран, а не отдельным счётчиком: панель и лог
// физически не могут разойтись — если запись скрыта фильтром или DEBUG, её
// нет ни там, ни там. Схлопнутая серия считается своим Count, иначе панель
// показывала бы одну ошибку там, где их полсотни.
func (s *screen) moduleStats() []modStat {
	byName := make(map[string]*modStat)
	for _, b := range s.blocks {
		if b.rec == nil || b.rec.Module == "" {
			continue
		}
		m := byName[b.rec.Module]
		if m == nil {
			m = &modStat{name: b.rec.Module}
			byName[b.rec.Module] = m
		}
		n := maxInt(b.rec.Count, 1)
		m.count += n
		if b.rec.Warn {
			m.warn += n
		}
		if b.rec.Err {
			m.err += n
		}
	}

	out := make([]modStat, 0, len(byName))
	for _, m := range byName {
		out = append(out, *m)
	}
	// Сначала по числу записей, при равенстве по имени — иначе одинаково
	// шумные модули перепрыгивали бы друг друга при каждой перерисовке.
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].name < out[j].name
	})
	return out
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
