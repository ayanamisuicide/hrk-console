package logfeed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func messages(entries []Entry) string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Message
	}
	return strings.Join(out, ",")
}

// TestJournalMergesSourcesByTime проверяет слияние источников по времени и курсор страниц,
// когда записи разных источников приходятся на одну секунду.
func TestJournalMergesSourcesByTime(t *testing.T) {
	dir := t.TempDir()
	bot, panel := filepath.Join(dir, "heroku.log"), filepath.Join(dir, "panel.log")
	writeLines(t, bot,
		"2026-10-03 10:00:00 [INFO] Core: b1",
		"2026-10-03 10:00:02 [INFO] Core: b2",
		"2026-10-03 10:00:04 [ERROR] Core: b3",
		"Traceback (most recent call last):",
	)
	writeLines(t, panel,
		"2026-10-03 10:00:01 [INFO] panel: p1",
		"2026-10-03 10:00:02 [INFO] panel: p2",
		"2026-10-03 10:00:05 [ERROR] process: p3",
	)
	j := Journal{Sources: []Source{{Path: bot}, {Path: panel}}}
	all, more := Search(j, Query{Limit: 100})
	if got := messages(all); got != "b1,p1,p2,b2,b3,p3" || more {
		t.Fatalf("слияние: %s (more=%v)", got, more)
	}
	if len(all[4].Trace) != 1 {
		t.Errorf("traceback потерялся при слиянии: %+v", all[4])
	}
	// Постранично по одной записи — тот же порядок без потерь и повторов.
	var paged []Entry
	q := Query{Limit: 1}
	for {
		page, more := Search(j, q)
		paged = append(page, paged...)
		if !more {
			break
		}
		q.BeforeTS = page[0].TS
		q.BeforeSkip = 0
		for _, e := range paged {
			if e.TS == q.BeforeTS {
				q.BeforeSkip++
			}
		}
	}
	if got := messages(paged); got != messages(all) {
		t.Fatalf("постранично: %s", got)
	}
	errs, _ := Search(j, Query{Levels: []string{"ERROR"}, Limit: 10})
	if messages(errs) != "b3,p3" {
		t.Errorf("фильтр по уровню через источники: %s", messages(errs))
	}
	from, _ := time.ParseInLocation(timeLayout, "2026-10-03 10:00:00", time.Local)
	st := Collect(j, from.UnixMilli(), from.UnixMilli()+10000, 10000, Query{})
	if st.Total != 6 || st.Buckets[0].Err != 2 || st.Modules["panel"] != 2 {
		t.Errorf("сводка: %+v", st)
	}
	around, idx, ok := Around(j, all[2].TS, all[2].ID, 1)
	if !ok || messages(around) != "p1,p2,b2" || idx != 1 {
		t.Errorf("окружение через источники: %s idx=%d ok=%v", messages(around), idx, ok)
	}
}

// TestArchiveKeepsRotationsWithoutDuplicates проверяет архив ротаций: копия ротации, лежащей
// рядом, не дублирует записи, а после перезаписи ротации старые записи остаются доступны.
func TestArchiveKeepsRotationsWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	src := Source{Path: filepath.Join(dir, "heroku.log"), Archive: filepath.Join(dir, "archive")}
	j := Journal{Sources: []Source{src}}
	writeLines(t, src.Path+".1", "2026-10-03 09:00:00 [INFO] Core: old1", "2026-10-03 09:00:01 [INFO] Core: old2")
	writeLines(t, src.Path, "2026-10-03 10:00:00 [INFO] Core: cur1")
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	if err := ArchiveRotations(src, 0, 0, now); err != nil {
		t.Fatal(err)
	}
	if st := src.Stats(); st.Files != 1 || st.Bytes == 0 {
		t.Fatalf("копия не создана: %+v", st)
	}
	// Повторный проход не плодит копии одного и того же файла.
	if err := ArchiveRotations(src, 0, 0, now); err != nil || src.Stats().Files != 1 {
		t.Fatalf("повторная копия: %+v %v", src.Stats(), err)
	}
	all, _ := Search(j, Query{Limit: 100})
	if got := messages(all); got != "old1,old2,cur1" {
		t.Fatalf("пока ротация на месте, копия не читается: %s", got)
	}
	// Бот ротирует журнал: прежняя ротация перезаписана, но её записи — в архиве.
	writeLines(t, src.Path+".1", "2026-10-03 10:00:00 [INFO] Core: cur1", "2026-10-03 11:00:00 [INFO] Core: cur2")
	writeLines(t, src.Path, "2026-10-03 11:30:00 [WARNING] Core: new1")
	all, _ = Search(j, Query{Limit: 100})
	if got := messages(all); got != "old1,old2,cur1,cur2,new1" {
		t.Fatalf("после ротации: %s", got)
	}
	if err := ArchiveRotations(src, 0, 0, now); err != nil || src.Stats().Files != 2 {
		t.Fatalf("новая ротация не скопирована: %+v %v", src.Stats(), err)
	}
	all, _ = Search(j, Query{Limit: 100})
	if got := messages(all); got != "old1,old2,cur1,cur2,new1" {
		t.Fatalf("после второй копии: %s", got)
	}
	// Уборка по возрасту оставляет только свежую копию, по размеру — самую новую.
	if err := ArchiveRotations(src, 0, 150*time.Minute, now); err != nil || src.Stats().Files != 1 {
		t.Fatalf("уборка по возрасту: %+v %v", src.Stats(), err)
	}
	writeLines(t, src.Path+".1", "2026-10-03 11:30:00 [WARNING] Core: new1")
	if err := ArchiveRotations(src, 1, 0, now); err != nil || src.Stats().Files != 1 {
		t.Fatalf("уборка по размеру: %+v %v", src.Stats(), err)
	}
}
