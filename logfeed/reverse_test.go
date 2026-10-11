package logfeed

import (
	"bufio"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// readForward — эталон: файл целиком, запись за записью, строки без лимита длины.
func readForward(path string) []Entry {
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
		if err == io.EOF || err != nil {
			break
		}
	}
	if e, ok := g.Flush(); ok {
		out = append(out, e)
	}
	return out
}

func readBackward(path string, block int) []Entry {
	var out []Entry
	eachEntryBackwards(path, block, func(e Entry) bool {
		e.seal()
		out = append(out, e)
		return true
	})
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

// Обратное чтение даёт ровно те же записи, что и прямой разбор, при любом размере блока:
// границы блоков падают на середину строки, на перевод строки и на середину русской буквы.
func TestBackwardMatchesForward(t *testing.T) {
	trickyFiles := map[string]string{
		"обрывок в начале":     "  File \"x.py\", line 1\nValueError: плохо\n2026-10-11 10:00:00 [INFO] a: раз\n2026-10-11 10:00:01 [ERROR] b: два\nTraceback\n  x\n",
		"без перевода строки":  "2026-10-11 10:00:00 [INFO] a: раз\n2026-10-11 10:00:01 [INFO] a: два",
		"пустые строки":        "\n\n2026-10-11 10:00:00 [INFO] a: раз\n\n\n2026-10-11 10:00:01 [INFO] a: два\n\n",
		"CRLF":                 "2026-10-11 10:00:00 [INFO] a: раз\r\n  хвост\r\n2026-10-11 10:00:01 [INFO] a: два\r\n",
		"одна строка":          "2026-10-11 10:00:00 [INFO] a: раз\n",
		"только продолжения":   "один\nдва\nтри\n",
		"только перевод строк": "\n\n\n",
		"пустой файл":          "",
		"длинная строка":       "2026-10-11 10:00:00 [INFO] a: " + strings.Repeat("ж", 5000) + "\n2026-10-11 10:00:01 [INFO] b: ок\n",
	}
	random := rand.New(rand.NewSource(7))
	var big strings.Builder
	for i := 0; i < 400; i++ {
		big.WriteString("2026-10-11 10:00:00 [WARNING] mod: сообщение ")
		big.WriteString(strings.Repeat("ё", random.Intn(30)))
		big.WriteString("\n")
		for j := random.Intn(4); j > 0; j-- {
			big.WriteString(strings.Repeat(" х", random.Intn(20)) + "\n")
		}
	}
	trickyFiles["случайный"] = big.String()

	dir := t.TempDir()
	for name, content := range trickyFiles {
		path := filepath.Join(dir, "f.log")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		want := readForward(path)
		for _, block := range []int{1, 2, 3, 7, 16, 64, 255, 4096, reverseBlock} {
			got := readBackward(path, block)
			if len(want) == 0 && len(got) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s, блок %d: записей %d, ожидали %d\nполучили %v\nожидали  %v", name, block, len(got), len(want), got, want)
			}
		}
	}
}

// Чтение останавливается, когда потребитель сказал «хватит»: файл не читается дальше.
func TestBackwardStopsEarly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.log")
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("2026-10-11 10:00:00 [INFO] a: запись\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	count := 0
	eachEntryBackwards(path, 64, func(Entry) bool { count++; return count < 3 })
	if count != 3 {
		t.Fatalf("получили %d записей, ждали остановки на 3", count)
	}
}

// bigLog пишет журнал примерно в 40 МБ: запись раз в секунду, изредка ошибка с traceback.
func bigLog(tb testing.TB) string {
	path := filepath.Join(tb.TempDir(), "heroku.log")
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	w := bufio.NewWriter(f)
	for i := 0; i < 330000; i++ {
		sec := i % 60
		w.WriteString("2026-10-11 10:" + string(rune('0'+i/600%6)) + string(rune('0'+i/60%10)) + ":" + string(rune('0'+sec/10)) + string(rune('0'+sec%10)) + " [INFO] loader: " + strings.Repeat("обычная запись журнала ", 4) + "\n")
		if i%5000 == 0 {
			w.WriteString("2026-10-11 10:00:00 [ERROR] редкий: редкая ошибка\nTraceback (most recent call last):\n  File \"x.py\", line 1, in <module>\nValueError: редкое\n")
		}
	}
	w.Flush()
	f.Close()
	return path
}

func BenchmarkSearchLatest(b *testing.B) {
	path := bigLog(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Search(Path(path), Query{Limit: 200})
	}
}

func BenchmarkSearchRareText(b *testing.B) {
	path := bigLog(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Search(Path(path), Query{Text: "редкое", Limit: 200})
	}
}

var referencePrefix = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[([A-Z]+)\] ([^:]+):\s?(.*)$`)

// referenceParse — прежний разбор регулярным выражением, эталон для ручного ParseLine.
func referenceParse(raw string) (Entry, bool) {
	m := referencePrefix.FindStringSubmatch(raw)
	if m == nil {
		return Entry{}, false
	}
	e := Entry{Time: m[1], Level: m[2], Module: m[3], Message: m[4]}
	if t, err := time.ParseInLocation(timeLayout, m[1], time.Local); err == nil {
		e.TS = t.UnixMilli()
	}
	return e, true
}

// Ручной разбор строки обязан совпадать с регулярным выражением на корректных и на
// испорченных строках: лишние и пропущенные символы, неверные даты, пустые поля.
func TestParseLineMatchesReference(t *testing.T) {
	base := "2026-10-11 10:20:30 [WARNING] loader.core: сообщение: с двоеточием"
	samples := []string{
		base,
		"2026-10-11 10:20:30 [INFO] a:",
		"2026-10-11 10:20:30 [INFO] a:x",
		"2026-10-11 10:20:30 [INFO] a:\tx",
		"2026-10-11 10:20:30 [INFO] a:  двойной пробел",
		"2026-10-11 10:20:30 [INFO] :пустой модуль",
		"2026-10-11 10:20:30 [INFO]  a: два пробела",
		"2026-10-11 10:20:30 [info] a: нижний регистр",
		"2026-10-11 10:20:30 [] a: пусто",
		"2026-02-30 10:20:30 [INFO] a: нет такой даты",
		"2026-13-01 10:20:30 [INFO] a: нет такого месяца",
		"2028-02-29 10:20:30 [INFO] a: високосный",
		"2027-02-29 10:20:30 [INFO] a: не високосный",
		"2026-10-11 24:00:00 [INFO] a: час",
		"2026-10-11 10:60:00 [INFO] a: минута",
		"2026-10-11 10:20:60 [INFO] a: секунда",
		"2026-10-11T10:20:30 [INFO] a: T",
		"  File \"x.py\", line 1, in <module>",
		"Traceback (most recent call last):",
		"2026-10-11 10:20:30",
		"2026-10-11 10:20:30 [INFO] a",
		"",
	}
	random := rand.New(rand.NewSource(11))
	for i := 0; i < 20000; i++ {
		b := []byte(base)
		for j := random.Intn(3); j >= 0; j-- {
			switch random.Intn(3) {
			case 0:
				b[random.Intn(len(b))] = "0 9:[]-aZ\t"[random.Intn(10)]
			case 1:
				at := random.Intn(len(b))
				b = append(b[:at], b[at+1:]...)
			default:
				at := random.Intn(len(b))
				b = append(b[:at], append([]byte{":9 ]"[random.Intn(4)]}, b[at:]...)...)
			}
		}
		samples = append(samples, string(b))
	}
	for _, raw := range samples {
		got, gotOK := ParseLine(raw)
		want, wantOK := referenceParse(raw)
		if gotOK != wantOK || !reflect.DeepEqual(got, want) {
			t.Fatalf("строка %q\nполучили %+v %v\nожидали  %+v %v", raw, got, gotOK, want, wantOK)
		}
	}
}

func TestContainsFoldMatchesToLower(t *testing.T) {
	hays := []string{"Ошибка соединения", "ЗАГРУЗКА модуля", "plain ASCII Text", "Ёлка и ёж", "", "İstanbul", "ǅ"}
	needles := []string{"ошибка", "модуля", "text", "ёлка", "ёж", "", "соединения", "я", "istanbul", "ǆ"}
	for _, h := range hays {
		for _, n := range needles {
			want := strings.Contains(strings.ToLower(h), n)
			if got := containsFold(h, n); got != want {
				t.Errorf("containsFold(%q, %q) = %v, ожидали %v", h, n, got, want)
			}
		}
	}
}
