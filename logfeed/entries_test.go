package logfeed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseEntriesGroupsContinuations проверяет, что traceback уходит внутрь записи.
func TestParseEntriesGroupsContinuations(t *testing.T) {
	got := ParseEntries([]string{
		"хвост traceback из прошлого файла",
		"2026-10-03 10:00:00 [INFO] Core: started",
		"2026-10-03 10:00:01 [ERROR] updates: failed: connection reset",
		"Traceback (most recent call last):",
		`  File "x.py", line 1`,
		"2026-10-03 10:00:02 [WARNING] net.http: slow",
	})
	if len(got) != 4 {
		t.Fatalf("записей %d, ожидали 4: %v", len(got), got)
	}
	if got[0].Level != "OTHER" || got[0].TS != 0 {
		t.Errorf("обрывок в начале: %+v", got[0])
	}
	if got[2].Module != "updates" || got[2].Message != "failed: connection reset" || len(got[2].Trace) != 2 {
		t.Errorf("запись с traceback: %+v", got[2])
	}
	if got[3].Module != "net.http" || got[3].Level != "WARNING" {
		t.Errorf("модуль с точкой: %+v", got[3])
	}
	if got[1].ID == "" || got[1].ID == got[2].ID {
		t.Errorf("ID должен быть заполнен и различать записи: %q %q", got[1].ID, got[2].ID)
	}
	want, _ := time.ParseInLocation(timeLayout, "2026-10-03 10:00:00", time.Local)
	if got[1].TS != want.UnixMilli() {
		t.Errorf("TS = %d, ожидали %d", got[1].TS, want.UnixMilli())
	}
}

// TestGrouperFlush проверяет, что запись можно получить, не дожидаясь следующей.
func TestGrouperFlush(t *testing.T) {
	g := &Grouper{}
	if _, ok := g.Feed("2026-10-03 10:00:00 [INFO] Core: a"); ok {
		t.Fatal("первая запись не может быть закрыта")
	}
	if _, ok := g.Feed("продолжение"); ok {
		t.Fatal("продолжение не закрывает запись")
	}
	e, ok := g.Flush()
	if !ok || len(e.Trace) != 1 {
		t.Fatalf("Flush: %+v %v", e, ok)
	}
	if _, ok := g.Flush(); ok {
		t.Error("повторный Flush не должен ничего отдавать")
	}
}

// TestFilesOrdersRotations проверяет порядок: от старых ротаций к основному файлу.
func TestFilesOrdersRotations(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "heroku.log")
	for _, name := range []string{"heroku.log", "heroku.log.1", "heroku.log.2", "heroku.log.bak", "heroku.log.10"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, f := range Files(base) {
		names = append(names, filepath.Base(f))
	}
	want := "heroku.log.10 heroku.log.2 heroku.log.1 heroku.log"
	if strings.Join(names, " ") != want {
		t.Errorf("порядок файлов %v, ожидали %s", names, want)
	}
}

func sampleFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "heroku.log")
	old := "2026-10-03 09:00:00 [INFO] Core: старая запись\n2026-10-03 09:00:01 [ERROR] updates: старая ошибка\nTraceback\n"
	cur := strings.Join([]string{
		"2026-10-03 10:00:00 [INFO] Core: started",
		"2026-10-03 10:00:00 [INFO] Core: started",
		"2026-10-03 10:00:00 [INFO] Core: started",
		"2026-10-03 10:00:01 [WARNING] net: slow",
		"2026-10-03 10:00:02 [ERROR] updates: failed",
		"  File \"x.py\"",
		"2026-10-03 10:00:03 [CRITICAL] Core: down",
	}, "\n") + "\n"
	if err := os.WriteFile(base+".1", []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte(cur), 0o644); err != nil {
		t.Fatal(err)
	}
	return base
}

// TestSearchAcrossRotationAndFilters проверяет чтение ротаций и фильтры.
func TestSearchAcrossRotationAndFilters(t *testing.T) {
	base := sampleFiles(t)

	all, more := Search(base, Query{Limit: 100})
	if len(all) != 8 || more {
		t.Fatalf("всего записей %d (more=%v), ожидали 8", len(all), more)
	}
	if all[0].Message != "старая запись" {
		t.Errorf("первой должна идти запись из ротации: %v", all[0])
	}

	errs, _ := Search(base, Query{Levels: []string{"ERROR"}, Limit: 100})
	if len(errs) != 3 { // ERROR из ротации, ERROR и CRITICAL
		t.Errorf("ошибок %d, ожидали 3: %v", len(errs), errs)
	}
	byModule, _ := Search(base, Query{Module: "net", Limit: 100})
	if len(byModule) != 1 {
		t.Errorf("по модулю net: %v", byModule)
	}
	// Поиск заглядывает и в traceback.
	inTrace, _ := Search(base, Query{Text: `x.py`, Limit: 100})
	if len(inTrace) != 1 || inTrace[0].Message != "failed" {
		t.Errorf("поиск по traceback: %v", inTrace)
	}
	from, _ := time.ParseInLocation(timeLayout, "2026-10-03 10:00:01", time.Local)
	ranged, _ := Search(base, Query{From: from.UnixMilli(), To: from.UnixMilli() + 1000, Limit: 100})
	if len(ranged) != 2 {
		t.Errorf("диапазон времени: %v", ranged)
	}
}

// TestSearchPagesBackwardWithSameSecond проверяет, что курсор не теряет и не
// дублирует записи, у которых время совпадает с границей страницы.
func TestSearchPagesBackwardWithSameSecond(t *testing.T) {
	base := sampleFiles(t)
	var collected []Entry
	q := Query{Limit: 2}
	for page := 0; page < 10; page++ {
		got, more := Search(base, q)
		if len(got) == 0 {
			break
		}
		collected = append(got, collected...)
		oldest := got[0].TS
		skip := 0
		for _, e := range collected {
			if e.TS == oldest {
				skip++
			}
		}
		q.BeforeTS, q.BeforeSkip = oldest, skip
		if !more {
			break
		}
	}
	all, _ := Search(base, Query{Limit: 100})
	if len(collected) != len(all) {
		t.Fatalf("собрано %d записей постранично, ожидали %d", len(collected), len(all))
	}
	for i := range all {
		if collected[i].ID != all[i].ID || collected[i].TS != all[i].TS {
			t.Errorf("запись %d: %v вместо %v", i, collected[i], all[i])
		}
	}
}

// TestCollectBucketsAndModules проверяет сводку для шкалы.
func TestCollectBucketsAndModules(t *testing.T) {
	base := sampleFiles(t)
	from, _ := time.ParseInLocation(timeLayout, "2026-10-03 10:00:00", time.Local)
	st := Collect(base, from.UnixMilli(), from.UnixMilli()+4000, 2000, Query{})
	if len(st.Buckets) != 2 {
		t.Fatalf("корзин %d", len(st.Buckets))
	}
	if st.Buckets[0].Info != 3 || st.Buckets[0].Warn != 1 || st.Buckets[1].Err != 2 {
		t.Errorf("корзины: %+v", st.Buckets)
	}
	if st.Total != 6 || st.Modules["Core"] != 4 {
		t.Errorf("итог %d, модули %v", st.Total, st.Modules)
	}
	// Фильтр по модулю сужает корзины, но не список модулей.
	narrow := Collect(base, from.UnixMilli(), from.UnixMilli()+4000, 2000, Query{Module: "net"})
	if narrow.Total != 1 || len(narrow.Modules) != 3 {
		t.Errorf("с фильтром модуля: %+v", narrow)
	}
}

// TestAround проверяет соседей записи.
func TestAround(t *testing.T) {
	base := sampleFiles(t)
	all, _ := Search(base, Query{Limit: 100})
	target := all[5] // failed
	around, idx, ok := Around(base, target.TS, target.ID, 2)
	if !ok || around[idx].ID != target.ID || len(around) != 5 {
		t.Fatalf("Around: ok=%v idx=%d len=%d", ok, idx, len(around))
	}
	if _, _, ok := Around(base, 1, "нет", 2); ok {
		t.Error("несуществующая запись не должна находиться")
	}
}

// TestAroundNPagesOneSide проверяет, что соседей до и после можно запросить отдельно.
func TestAroundNPagesOneSide(t *testing.T) {
	base := sampleFiles(t)
	all, _ := Search(base, Query{Limit: 100})
	target := all[5]
	around, idx, ok := AroundN(base, target.TS, target.ID, 3, 0)
	if !ok || idx != 3 || len(around) != 4 || around[idx].ID != target.ID {
		t.Fatalf("только ранние: ok=%v idx=%d len=%d", ok, idx, len(around))
	}
	around, idx, ok = AroundN(base, target.TS, target.ID, 0, 1)
	if !ok || idx != 0 || len(around) != 2 {
		t.Fatalf("только поздние: ok=%v idx=%d len=%d", ok, idx, len(around))
	}
}

// TestProblemsGroupsRepeats проверяет группировку повторяющихся ошибок: числа в сообщении
// не разделяют группу, другой тип исключения — разделяет, частые идут первыми.
func TestProblemsGroupsRepeats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	lines := []string{
		"2026-10-03 10:00:00 [ERROR] updates: timeout after 10 s",
		"Traceback (most recent call last):",
		"aiohttp.ServerTimeoutError: Timeout on reading data",
		"2026-10-03 10:00:05 [INFO] Core: ok",
		"2026-10-03 10:00:10 [ERROR] updates: timeout after 12 s",
		"Traceback (most recent call last):",
		"aiohttp.ServerTimeoutError: Timeout on reading data",
		"2026-10-03 10:00:20 [ERROR] updates: timeout after 30 s",
		"Traceback (most recent call last):",
		"ValueError: bad manifest",
		"2026-10-03 10:00:30 [CRITICAL] loader: module 0x7f3a crashed",
		"2026-10-03 10:00:40 [ERROR] updates: timeout after 14 s",
		"Traceback (most recent call last):",
		"aiohttp.ServerTimeoutError: Timeout on reading data",
		"2026-10-03 10:00:50 [WARNING] net: slow 4.2 s",
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	from, _ := time.ParseInLocation(timeLayout, "2026-10-03 10:00:00", time.Local)
	got := Problems(path, from.UnixMilli(), 0, Query{}, 10)
	if len(got) != 3 {
		t.Fatalf("групп %d, ожидали 3: %+v", len(got), got)
	}
	top := got[0]
	if top.Count != 3 || top.Last.Message != "timeout after 14 s" || top.First != from.UnixMilli() {
		t.Errorf("частая ошибка: %+v", top)
	}
	if got[1].Last.Level != "CRITICAL" || got[2].Last.Message != "timeout after 30 s" {
		t.Errorf("при равном счёте свежие первыми: %+v", got[1:])
	}
	// Окно отсекает старые записи, уровень WARNING выбирается явно.
	late := Problems(path, from.UnixMilli()+15000, 0, Query{}, 10)
	if len(late) != 3 || late[0].Count != 1 {
		t.Errorf("окно: %+v", late)
	}
	warn := Problems(path, from.UnixMilli(), 0, Query{Levels: []string{"WARNING"}}, 10)
	if len(warn) != 1 || warn[0].Last.Module != "net" {
		t.Errorf("предупреждения: %+v", warn)
	}
	if one := Problems(path, from.UnixMilli(), 0, Query{}, 1); len(one) != 1 {
		t.Errorf("limit: %d", len(one))
	}
}

// TestSearchHint проверяет устойчивую часть сообщения для поиска группы.
func TestSearchHint(t *testing.T) {
	cases := map[string]string{
		"timeout after 10 s": "timeout after",
		"не удалось получить манифест":      "не удалось получить манифест",
		"module 0x7f3a crashed at 12:00:01": "crashed at",
		"42":                                "",
		"id 5f2c9a1b: ok":                   "",
		"Telegram ответил 429, ждём 3 с":    "Telegram ответил",
	}
	for in, want := range cases {
		if got := SearchHint(in); got != want {
			t.Errorf("SearchHint(%q) = %q, ожидали %q", in, got, want)
		}
	}
}
