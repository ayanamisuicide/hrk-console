package logfeed

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
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

	// Hide, если задан, исключает записи, скрытые правилами журнала.
	Hide func(Entry) bool

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
	if q.Hide != nil && q.Hide(e) {
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
// порядке, и признак того, что более старые тоже есть. Читает журнал от новых записей к
// старым и останавливается, набрав нужное число.
func Search(j Journal, q Query) (entries []Entry, hasMore bool) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	q = q.prepare()
	skip := q.BeforeSkip
	j.backwards(func(e Entry) bool {
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
func Collect(j Journal, from, to, bucketMs int64, q Query) Stats {
	if bucketMs <= 0 || to <= from {
		return Stats{Buckets: []Bucket{}, Modules: map[string]int{}}
	}
	n := int((to - from + bucketMs - 1) / bucketMs)
	st := Stats{Buckets: make([]Bucket, n), Modules: map[string]int{}}
	for i := range st.Buckets {
		st.Buckets[i].T = from + int64(i)*bucketMs
	}
	q = q.prepare()
	j.backwards(func(e Entry) bool {
		if e.TS < from && e.TS > 0 {
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
	return st
}

// Around возвращает запись и до n соседей с каждой стороны. Запись ищется по времени и ID.
// index — позиция записи в результате.
func Around(j Journal, ts int64, id string, n int) (entries []Entry, index int, ok bool) {
	return AroundN(j, ts, id, n, n)
}

// AroundN — то же, что Around, но число соседей задаётся отдельно: older записей до найденной
// и newer после неё. Так окружение листается в одну сторону.
func AroundN(j Journal, ts int64, id string, older, newer int) (entries []Entry, index int, ok bool) {
	var after, before []Entry // after — окно из newer записей новее найденной, в порядке чтения
	var target Entry
	found := false
	j.backwards(func(e Entry) bool {
		if found {
			e.detach()
			e.seal()
			before = append(before, e)
			return len(before) < older
		}
		if e.TS == ts {
			e.seal()
			if e.ID == id {
				e.detach()
				target, found = e, true
				return older > 0
			}
		}
		// Запись старше искомой: дальше её уже не найти.
		if e.TS > 0 && ts > 0 && e.TS < ts {
			return false
		}
		if newer > 0 {
			after = append(after, e)
			if len(after) > newer {
				after = after[1:]
			}
		}
		return true
	})
	if !found {
		return nil, 0, false
	}
	out := make([]Entry, 0, len(before)+1+len(after))
	for i := len(before) - 1; i >= 0; i-- {
		out = append(out, before[i])
	}
	index = len(out)
	out = append(out, target)
	for i := len(after) - 1; i >= 0; i-- {
		after[i].detach()
		after[i].seal()
		out = append(out, after[i])
	}
	return out, index, true
}

// Problem — повторяющаяся ошибка: записи одного модуля с одинаковым сообщением (числа
// не в счёт) и одним типом исключения в traceback.
type Problem struct {
	Count int   `json:"count"`
	First int64 `json:"first"` // время самой ранней записи группы в окне, мс
	Last  Entry `json:"last"`  // самая свежая запись группы
	// Query — устойчивая часть сообщения: по ней строка поиска находит записи группы.
	Query string `json:"query"`
	// Key — подпись группы (ProblemKey): по ней правила журнала скрывают такие записи или
	// уведомляют о них.
	Key string `json:"key"`
}

// maxProblemGroups ограничивает память при потоке разных ошибок: новые группы сверх
// предела не заводятся, уже найденные продолжают считаться.
const maxProblemGroups = 2000

// Problems собирает повторяющиеся записи за окно [from, to) и возвращает до limit
// групп, самые частые первыми (при равенстве — более свежие). Уровень, модуль и текст
// берутся из q; без уровня считаются ошибки (ERROR и CRITICAL).
func Problems(j Journal, from, to int64, q Query, limit int) []Problem {
	if len(q.Levels) == 0 {
		q.Levels = []string{"ERROR"}
	}
	q = q.prepare()
	groups := map[string]*Problem{}
	var order []*Problem
	j.backwards(func(e Entry) bool {
		if e.TS < from && e.TS > 0 {
			return false
		}
		if (to > 0 && e.TS >= to) || e.TS < from || !q.matchSoft(e, true) {
			return true
		}
		key := ProblemKey(e)
		if p, known := groups[key]; known {
			p.Count++
			p.First = e.TS
			return true
		}
		if len(groups) >= maxProblemGroups {
			return true
		}
		e.detach()
		e.seal()
		p := &Problem{Count: 1, First: e.TS, Last: e, Key: key}
		groups[key] = p
		order = append(order, p)
		return true
	})
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].Count != order[b].Count {
			return order[a].Count > order[b].Count
		}
		return order[a].Last.TS > order[b].Last.TS
	})
	if limit > 0 && len(order) > limit {
		order = order[:limit]
	}
	out := make([]Problem, len(order))
	for i, p := range order {
		out[i] = *p
	}
	return out
}

// volatile — части сообщения, которые меняются от раза к разу: адреса, хэши, числа.
var volatile = regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b|\b[0-9a-f]*[0-9][0-9a-f]*[a-f][0-9a-f]*\b|\d+(?:[.,:]\d+)*`)

// ProblemKey — подпись записи для группировки: модуль, сообщение без изменчивых частей
// и тип исключения из последней строки traceback.
func ProblemKey(e Entry) string {
	return e.Module + "\x00" + volatile.ReplaceAllString(e.Message, "#") + "\x00" + exceptionType(e.Trace)
}

// exceptionType возвращает имя исключения из последней непустой строки traceback
// («aiohttp.ClientError: …» → «aiohttp.ClientError»), если строка на него похожа.
func exceptionType(trace []string) string {
	for i := len(trace) - 1; i >= 0; i-- {
		line := strings.TrimSpace(trace[i])
		if line == "" {
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		if name == "" || strings.ContainsAny(name, " \t()[]'\"") {
			return ""
		}
		return name
	}
	return ""
}

// SearchHint выбирает из сообщения самый длинный кусок без изменчивых частей: строка
// поиска с ним находит записи той же группы. Слишком короткий кусок не годится — пусто.
func SearchHint(message string) string {
	best := ""
	for _, part := range volatile.Split(message, -1) {
		part = strings.TrimFunc(part, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
		if utf8.RuneCountInString(part) > utf8.RuneCountInString(best) {
			best = part
		}
	}
	if utf8.RuneCountInString(best) < 4 {
		return ""
	}
	return best
}

// String нужен отладочным сообщениям тестов.
func (e Entry) String() string {
	return fmt.Sprintf("%s [%s] %s: %s", e.Time, e.Level, e.Module, e.Message)
}
