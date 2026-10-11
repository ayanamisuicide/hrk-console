package logfeed

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Entry — запись журнала целиком: строка с префиксом и все продолжения
// (traceback, многострочные сообщения) под ней.
type Entry struct {
	// ID — отпечаток текста записи. Одинаковые записи в одну секунду имеют один
	// ID, поэтому для постраничного чтения он не используется (см. Query.BeforeTS).
	ID string `json:"id"`
	// TS — время записи в миллисекундах Unix, в часовом поясе сервера: бот пишет
	// время без пояса. У записей без префикса (обрывок в начале файла) он равен 0.
	TS      int64    `json:"ts"`
	Time    string   `json:"time"`
	Level   string   `json:"level"`
	Module  string   `json:"module"`
	Message string   `json:"message"`
	Trace   []string `json:"trace,omitempty"`
}

const timeLayout = "2006-01-02 15:04:05"

// ParseLine разбирает строку с префиксом журнала. ok=false — это продолжение
// предыдущей записи.
func ParseLine(raw string) (e Entry, ok bool) {
	// Дешёвый отсев: большинство строк — продолжения traceback, у них нет даты в начале.
	if len(raw) < 26 || raw[4] != '-' || raw[7] != '-' || raw[10] != ' ' {
		return Entry{}, false
	}
	// «ГГГГ-ММ-ДД ЧЧ:ММ:СС [УРОВЕНЬ] модуль: сообщение» разбирается вручную: так в разы
	// быстрее регулярного выражения entryPrefix, с которым совпадает на любых строках (тест).
	if raw[13] != ':' || raw[16] != ':' || raw[19] != ' ' || raw[20] != '[' {
		return Entry{}, false
	}
	var d [14]int
	for i, at := range [14]int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		c := raw[at]
		if c < '0' || c > '9' {
			return Entry{}, false
		}
		d[i] = int(c - '0')
	}
	rest := raw[21:]
	end := 0
	for end < len(rest) && rest[end] >= 'A' && rest[end] <= 'Z' {
		end++
	}
	if end == 0 || len(rest) < end+3 || rest[end] != ']' || rest[end+1] != ' ' {
		return Entry{}, false
	}
	level := rest[:end]
	rest = rest[end+2:]
	colon := strings.IndexByte(rest, ':')
	if colon < 1 {
		return Entry{}, false
	}
	module, message := rest[:colon], rest[colon+1:]
	if message != "" {
		switch message[0] {
		case ' ', '\t', '\n', '\f', '\r':
			message = message[1:]
		}
	}
	e = Entry{Time: raw[:19], Level: level, Module: module, Message: message}
	year := d[0]*1000 + d[1]*100 + d[2]*10 + d[3]
	month, day := d[4]*10+d[5], d[6]*10+d[7]
	hour, minute, sec := d[8]*10+d[9], d[10]*10+d[11], d[12]*10+d[13]
	if month >= 1 && month <= 12 && day >= 1 && day <= daysIn(time.Month(month), year) && hour < 24 && minute < 60 && sec < 60 {
		e.TS = time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.Local).UnixMilli()
	}
	return e, true
}

func daysIn(month time.Month, year int) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// seal заполняет ID, когда запись собрана целиком.
// detach копирует строки записи, чтобы она не удерживала блок файла, из которого прочитана.
func (e *Entry) detach() {
	e.Time, e.Level, e.Module, e.Message = strings.Clone(e.Time), strings.Clone(e.Level), strings.Clone(e.Module), strings.Clone(e.Message)
	if len(e.Trace) > 0 {
		trace := make([]string, len(e.Trace))
		for i, line := range e.Trace {
			trace[i] = strings.Clone(line)
		}
		e.Trace = trace
	}
}

func (e *Entry) seal() {
	h := fnv.New64a()
	_, _ = h.Write([]byte(e.Time + "\x00" + e.Level + "\x00" + e.Module + "\x00" + e.Message))
	for _, line := range e.Trace {
		_, _ = h.Write([]byte("\n" + line))
	}
	e.ID = strconv.FormatUint(h.Sum64(), 36)
}

// ParseEntries собирает записи из сырых строк файла по порядку. Строки до
// первого префикса образуют одну запись уровня OTHER: так выглядит начало
// ротированного файла, оборванное посреди traceback.
func ParseEntries(lines []string) []Entry {
	g := &Grouper{}
	var out []Entry
	for _, line := range lines {
		if e, ok := g.Feed(line); ok {
			out = append(out, e)
		}
	}
	if e, ok := g.Flush(); ok {
		out = append(out, e)
	}
	return out
}

// Grouper собирает записи из потока строк. Запись считается законченной,
// когда приходит следующая строка с префиксом, либо по Flush — его вызывают,
// когда поток затих, и продолжений ждать уже не стоит.
type Grouper struct {
	cur  Entry
	open bool
}

// Feed принимает строку. Если она закрыла предыдущую запись, возвращает её.
func (g *Grouper) Feed(raw string) (Entry, bool) {
	raw = strings.TrimRight(raw, "\r")
	if next, ok := ParseLine(raw); ok {
		prev, had := g.take()
		g.cur, g.open = next, true
		return prev, had
	}
	if !g.open {
		g.cur, g.open = Entry{Level: "OTHER", Message: raw}, true
		return Entry{}, false
	}
	g.cur.Trace = append(g.cur.Trace, raw)
	return Entry{}, false
}

// Flush отдаёт накопленную запись, не дожидаясь следующей.
func (g *Grouper) Flush() (Entry, bool) { return g.take() }

func (g *Grouper) take() (Entry, bool) {
	if !g.open {
		return Entry{}, false
	}
	e := g.cur
	g.cur, g.open = Entry{}, false
	e.seal()
	return e, true
}

// Files перечисляет файл журнала и его ротации от старой к новой:
// heroku.log.3, heroku.log.2, heroku.log.1, heroku.log. Отсутствующий основной
// файл не мешает читать ротации.
func Files(path string) []string {
	type rotated struct {
		n    int
		path string
	}
	var found []rotated
	matches, _ := filepath.Glob(escapeGlob(path) + ".*")
	for _, m := range matches {
		n, err := strconv.Atoi(strings.TrimPrefix(m, path+"."))
		if err != nil || n < 1 {
			continue
		}
		found = append(found, rotated{n, m})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n > found[j].n })
	files := make([]string, 0, len(found)+1)
	for _, f := range found {
		files = append(files, f.path)
	}
	if _, err := os.Stat(path); err == nil {
		files = append(files, path)
	}
	return files
}

// escapeGlob экранирует служебные символы шаблона в пути, например скобки в
// имени каталога.
func escapeGlob(path string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(path)
}

// Query — условия отбора записей.
type Query struct {
	// Levels — допустимые уровни. Пусто — любые. ERROR включает CRITICAL.
	Levels []string
	Module string
	// Text ищется без учёта регистра в сообщении, модуле и traceback.
	Text     string
	From, To int64 // границы времени, мс; 0 — без границы
	// BeforeTS и BeforeSkip задают постраничное чтение назад. Записи в одну
	// секунду неразличимы по времени, поэтому курсор — это время самой старой
	// уже загруженной записи и число загруженных записей с этим временем.
	// BeforeTS == 0 — читать с самого конца.
	BeforeTS   int64
	BeforeSkip int
	Limit      int

	// Mask, если задан, прячет секреты в тексте записи. Поиск по Text тогда видит только то,
	// что видит читатель: иначе подбор подстроки по «есть ли совпадение» раскрыл бы скрытое.
	Mask func(string) string

	// needle — Text в нижнем регистре; считается один раз на запрос, а не на запись.
	needle string
}

// prepare готовит условия к проходу по файлу.
func (q Query) prepare() Query {
	q.needle = strings.ToLower(q.Text)
	return q
}

// Match проверяет запись по всем условиям, кроме курсора.
func (q Query) Match(e Entry) bool {
	if q.From > 0 && e.TS < q.From {
		return false
	}
	if q.To > 0 && e.TS > q.To {
		return false
	}
	if !q.matchSoft(e, true) {
		return false
	}
	return true
}

// matchSoft проверяет уровень, модуль и текст; withModule=false пропускает
// модуль — так считается список модулей для выбора.
func (q Query) matchSoft(e Entry, withModule bool) bool {
	if len(q.Levels) > 0 && !levelIn(q.Levels, e.Level) {
		return false
	}
	if withModule && q.Module != "" && e.Module != q.Module {
		return false
	}
	if q.Text != "" {
		needle := q.needle
		if needle == "" {
			needle = strings.ToLower(q.Text)
		}
		if !containsFold(e.Message, needle) && !containsFold(e.Module, needle) && !traceContains(e.Trace, needle) {
			return false
		}
		if q.Mask != nil && !q.matchesMasked(e, needle) {
			return false
		}
	}
	return true
}

// matchesMasked повторяет поиск по тексту, каким его видит читатель с маской. Дорогая маска
// запускается только для записей, уже прошедших дешёвую проверку.
func (q Query) matchesMasked(e Entry, needle string) bool {
	if containsFold(q.Mask(e.Message), needle) || containsFold(e.Module, needle) {
		return true
	}
	for _, line := range e.Trace {
		if containsFold(q.Mask(line), needle) {
			return true
		}
	}
	return false
}

// traceContains ищет строку в traceback; совпадение не бывает на стыке строк.
func traceContains(trace []string, needle string) bool {
	for _, line := range trace {
		if containsFold(line, needle) {
			return true
		}
	}
	return false
}

// containsFold — strings.Contains(strings.ToLower(hay), needle) без выделения памяти;
// needle уже в нижнем регистре.
func containsFold(hay, needle string) bool {
	if needle == "" {
		return true
	}
	first, size := utf8.DecodeRuneInString(needle)
	for i, r := range hay {
		if unicode.ToLower(r) != first {
			continue
		}
		h, n := hay[i+utf8.RuneLen(r):], needle[size:]
		for n != "" {
			if h == "" {
				return false
			}
			hr, hs := utf8.DecodeRuneInString(h)
			nr, ns := utf8.DecodeRuneInString(n)
			if unicode.ToLower(hr) != nr {
				break
			}
			h, n = h[hs:], n[ns:]
		}
		if n == "" {
			return true
		}
	}
	return false
}

func levelIn(levels []string, level string) bool {
	for _, l := range levels {
		if l == level || (l == "ERROR" && level == "CRITICAL") {
			return true
		}
	}
	return false
}

// Search возвращает до Limit записей, подходящих под условия, в хронологическом
// порядке, и признак того, что более старые тоже есть. Читает файлы от новых к
// старым и останавливается, набрав нужное число.
func Search(path string, q Query) (entries []Entry, hasMore bool) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	q = q.prepare()
	skip := q.BeforeSkip
	files := Files(path)
	for i := len(files) - 1; i >= 0 && len(entries) <= q.Limit; i-- {
		eachEntryBackwards(files[i], 0, func(e Entry) bool {
			if q.BeforeTS > 0 {
				if e.TS > q.BeforeTS {
					return true
				}
				if e.TS == q.BeforeTS && skip > 0 && q.Match(e) {
					skip--
					return true
				}
			}
			if !q.Match(e) {
				return true
			}
			e.detach()
			e.seal()
			entries = append(entries, e)
			return len(entries) <= q.Limit
		})
	}
	if len(entries) > q.Limit {
		hasMore = true
		entries = entries[:q.Limit]
	}
	for l, r := 0, len(entries)-1; l < r; l, r = l+1, r-1 {
		entries[l], entries[r] = entries[r], entries[l]
	}
	return entries, hasMore
}

// Bucket — число записей в одном интервале шкалы времени.
type Bucket struct {
	T    int64 `json:"t"` // начало интервала, мс
	Info int   `json:"info"`
	Warn int   `json:"warn"`
	Err  int   `json:"err"`
}

// Stats — сводка за окно [from, to) для шкалы времени.
type Stats struct {
	Buckets []Bucket       `json:"buckets"`
	Modules map[string]int `json:"modules"`
	Total   int            `json:"total"`
}

// Collect считает записи по интервалам bucketMs. Уровень, модуль и текст из q
// учитываются, границы времени задаются from и to. Список модулей строится без
// фильтра по модулю, чтобы выбор не сужался после первого клика.
func Collect(path string, from, to, bucketMs int64, q Query) Stats {
	if bucketMs <= 0 || to <= from {
		return Stats{Buckets: []Bucket{}, Modules: map[string]int{}}
	}
	n := int((to - from + bucketMs - 1) / bucketMs)
	st := Stats{Buckets: make([]Bucket, n), Modules: map[string]int{}}
	for i := range st.Buckets {
		st.Buckets[i].T = from + int64(i)*bucketMs
	}
	q = q.prepare()
	files := Files(path)
	for i := len(files) - 1; i >= 0; i-- {
		older := false
		eachEntryBackwards(files[i], 0, func(e Entry) bool {
			if e.TS < from && e.TS > 0 {
				older = true
				return false
			}
			if e.TS >= to || e.TS < from || !q.matchSoft(e, false) {
				return true
			}
			if e.Module != "" {
				if _, known := st.Modules[e.Module]; !known {
					e.Module = strings.Clone(e.Module)
				}
				st.Modules[e.Module]++
			}
			if q.Module != "" && e.Module != q.Module {
				return true
			}
			b := &st.Buckets[(e.TS-from)/bucketMs]
			switch e.Level {
			case "ERROR", "CRITICAL":
				b.Err++
			case "WARNING":
				b.Warn++
			default:
				b.Info++
			}
			st.Total++
			return true
		})
		if older {
			break
		}
	}
	return st
}

// Around возвращает запись и до n соседей с каждой стороны из того же файла.
// Запись ищется по времени и ID. index — позиция записи в результате.
func Around(path string, ts int64, id string, n int) (entries []Entry, index int, ok bool) {
	files := Files(path)
	for i := len(files) - 1; i >= 0 && !ok; i-- {
		var newer, older []Entry // newer — окно из n записей новее найденной, в порядке чтения
		var target Entry
		found := false
		eachEntryBackwards(files[i], 0, func(e Entry) bool {
			if found {
				older = append(older, e)
				return len(older) < n
			}
			if e.TS == ts {
				e.seal()
				if e.ID == id {
					target, found = e, true
					return n > 0
				}
			}
			if n > 0 {
				newer = append(newer, e)
				if len(newer) > n {
					newer = newer[1:]
				}
			}
			return true
		})
		if !found {
			continue
		}
		out := make([]Entry, 0, len(older)+1+len(newer))
		for j := len(older) - 1; j >= 0; j-- {
			older[j].detach()
			older[j].seal()
			out = append(out, older[j])
		}
		index = len(out)
		target.detach()
		out = append(out, target)
		for j := len(newer) - 1; j >= 0; j-- {
			newer[j].detach()
			newer[j].seal()
			out = append(out, newer[j])
		}
		return out, index, true
	}
	return nil, 0, false
}

// String нужен отладочным сообщениям тестов.
func (e Entry) String() string {
	return fmt.Sprintf("%s [%s] %s: %s", e.Time, e.Level, e.Module, e.Message)
}
