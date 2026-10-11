package logfeed

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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

var entryPrefix = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[([A-Z]+)\] ([^:]+):\s?(.*)$`)

const timeLayout = "2006-01-02 15:04:05"

// ParseLine разбирает строку с префиксом журнала. ok=false — это продолжение
// предыдущей записи.
func ParseLine(raw string) (e Entry, ok bool) {
	m := entryPrefix.FindStringSubmatch(raw)
	if m == nil {
		return Entry{}, false
	}
	e = Entry{Time: m[1], Level: m[2], Module: m[3], Message: m[4]}
	if t, err := time.ParseInLocation(timeLayout, m[1], time.Local); err == nil {
		e.TS = t.UnixMilli()
	}
	return e, true
}

// seal заполняет ID, когда запись собрана целиком.
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

// readEntries читает один файл целиком. Строки читаются без лимита длины:
// traceback с огромным значением не должен обрывать чтение остального файла.
func readEntries(path string) []Entry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	g := &Grouper{}
	var out []Entry
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			if e, ok := g.Feed(strings.TrimSuffix(line, "\n")); ok {
				out = append(out, e)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	if e, ok := g.Flush(); ok {
		out = append(out, e)
	}
	return out
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
		needle := strings.ToLower(q.Text)
		if !strings.Contains(strings.ToLower(e.Message), needle) &&
			!strings.Contains(strings.ToLower(e.Module), needle) &&
			!strings.Contains(strings.ToLower(strings.Join(e.Trace, "\n")), needle) {
			return false
		}
	}
	return true
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
	skip := q.BeforeSkip
	files := Files(path)
	for i := len(files) - 1; i >= 0 && len(entries) <= q.Limit; i-- {
		all := readEntries(files[i])
		for j := len(all) - 1; j >= 0; j-- {
			e := all[j]
			if q.BeforeTS > 0 {
				if e.TS > q.BeforeTS {
					continue
				}
				if e.TS == q.BeforeTS && skip > 0 && q.Match(e) {
					skip--
					continue
				}
			}
			if !q.Match(e) {
				continue
			}
			entries = append(entries, e)
			if len(entries) > q.Limit {
				break
			}
		}
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
	files := Files(path)
	for i := len(files) - 1; i >= 0; i-- {
		all := readEntries(files[i])
		older := false
		for j := len(all) - 1; j >= 0; j-- {
			e := all[j]
			if e.TS < from && e.TS > 0 {
				older = true
				break
			}
			if e.TS >= to || e.TS < from || !q.matchSoft(e, false) {
				continue
			}
			if e.Module != "" {
				st.Modules[e.Module]++
			}
			if q.Module != "" && e.Module != q.Module {
				continue
			}
			b := &st.Buckets[(e.TS-from)/bucketMs]
			switch {
			case e.Level == "ERROR" || e.Level == "CRITICAL":
				b.Err++
			case e.Level == "WARNING":
				b.Warn++
			default:
				b.Info++
			}
			st.Total++
		}
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
	for i := len(files) - 1; i >= 0; i-- {
		all := readEntries(files[i])
		for j := len(all) - 1; j >= 0; j-- {
			if all[j].ID != id || all[j].TS != ts {
				continue
			}
			lo, hi := j-n, j+n+1
			if lo < 0 {
				lo = 0
			}
			if hi > len(all) {
				hi = len(all)
			}
			return all[lo:hi], j - lo, true
		}
	}
	return nil, 0, false
}

// String нужен отладочным сообщениям тестов.
func (e Entry) String() string {
	return fmt.Sprintf("%s [%s] %s: %s", e.Time, e.Level, e.Module, e.Message)
}
