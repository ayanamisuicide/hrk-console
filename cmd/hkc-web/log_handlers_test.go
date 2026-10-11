package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heroku-console/logfeed"
)

// serverWithLog создаёт сервер, читающий журнал из временного файла.
func serverWithLog(t *testing.T, content string) *server {
	t.Helper()
	s := newTestServer(t)
	s.bot.LogFile = filepath.Join(t.TempDir(), "heroku.log")
	if err := os.WriteFile(s.bot.LogFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

const sampleJournal = `2026-10-03 10:00:00 [INFO] Core: started
2026-10-03 10:00:01 [ERROR] updates: failed
Traceback (most recent call last):
2026-10-03 10:00:02 [WARNING] net: slow
`

func getJSON(t *testing.T, handler http.HandlerFunc, target string, out any) *httptest.ResponseRecorder {
	t.Helper()
	res := httptest.NewRecorder()
	handler(res, httptest.NewRequest(http.MethodGet, target, nil))
	if out != nil && res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
			t.Fatalf("ответ не JSON: %v\n%s", err, res.Body.String())
		}
	}
	return res
}

// TestLogEntriesFiltersAndPages проверяет фильтры и курсор страницы.
func TestLogEntriesFiltersAndPages(t *testing.T) {
	s := serverWithLog(t, sampleJournal)
	var all struct {
		Entries []logfeed.Entry `json:"entries"`
		HasMore bool            `json:"hasMore"`
	}
	getJSON(t, s.logEntries, "/api/logs/entries", &all)
	if len(all.Entries) != 3 || all.HasMore {
		t.Fatalf("записей %d, hasMore=%v", len(all.Entries), all.HasMore)
	}
	if len(all.Entries[1].Trace) != 1 {
		t.Errorf("traceback должен быть внутри записи: %+v", all.Entries[1])
	}

	var errs struct{ Entries []logfeed.Entry }
	getJSON(t, s.logEntries, "/api/logs/entries?level=error", &errs)
	if len(errs.Entries) != 1 || errs.Entries[0].Module != "updates" {
		t.Errorf("фильтр уровня: %+v", errs.Entries)
	}

	var page struct {
		Entries []logfeed.Entry
		HasMore bool
	}
	getJSON(t, s.logEntries, "/api/logs/entries?limit=1", &page)
	if len(page.Entries) != 1 || !page.HasMore || page.Entries[0].Module != "net" {
		t.Fatalf("первая страница: %+v", page)
	}
	older := page.Entries[0].TS
	getJSON(t, s.logEntries, "/api/logs/entries?limit=5&skip=1&before="+itoa(older), &page)
	if len(page.Entries) != 2 || page.HasMore {
		t.Errorf("вторая страница: %+v", page)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestLogEntriesFractionalBounds проверяет, что дробные миллисекунды не снимают границу времени.
func TestLogEntriesFractionalBounds(t *testing.T) {
	s := serverWithLog(t, sampleJournal)
	var all struct{ Entries []logfeed.Entry }
	getJSON(t, s.logEntries, "/api/logs/entries", &all)
	last := all.Entries[len(all.Entries)-1].TS
	var cut struct{ Entries []logfeed.Entry }
	getJSON(t, s.logEntries, "/api/logs/entries?from="+itoa(last+1)+".75", &cut)
	if len(cut.Entries) != 0 {
		t.Errorf("записи старше границы остались: %+v", cut.Entries)
	}
	if queryInt64("-5") != 0 || queryInt64("мусор") != 0 || queryInt64("12.9") != 12 {
		t.Error("разбор числа миллисекунд")
	}
}

// TestLogStatsClampsWindow проверяет разумные значения по умолчанию.
func TestLogStatsClampsWindow(t *testing.T) {
	s := serverWithLog(t, sampleJournal)
	var st struct {
		logfeed.Stats
		BucketMs int64 `json:"bucketMs"`
		Now      int64 `json:"now"`
	}
	res := getJSON(t, s.logStats, "/api/logs/stats?bucket=1", &st)
	if res.Code != http.StatusOK {
		t.Fatalf("код %d", res.Code)
	}
	if len(st.Buckets) == 0 || len(st.Buckets) > 1001 {
		t.Errorf("число корзин вне ограничения: %d", len(st.Buckets))
	}

	// Корзины лежат на сетке: два запроса в разное время дают одни и те же границы.
	var first, second struct{ Buckets []logfeed.Bucket }
	getJSON(t, s.logStats, "/api/logs/stats?bucket=300000&to=1790000100000", &first)
	getJSON(t, s.logStats, "/api/logs/stats?bucket=300000&to=1790000200000", &second)
	if len(first.Buckets) == 0 || first.Buckets[0].T%300000 != 0 {
		t.Fatalf("границы корзин не выровнены: %+v", first.Buckets[:1])
	}
	seen := map[int64]bool{}
	for _, b := range first.Buckets {
		seen[b.T] = true
	}
	for _, b := range second.Buckets[:len(second.Buckets)-1] {
		if !seen[b.T] {
			t.Errorf("граница %d есть только во втором запросе: сетка поплыла", b.T)
		}
	}
}

// TestLogContextNotFound проверяет ответ для исчезнувшей записи.
func TestLogContextNotFound(t *testing.T) {
	s := serverWithLog(t, sampleJournal)
	if res := getJSON(t, s.logContext, "/api/logs/context?ts=1&id=нет", nil); res.Code != http.StatusNotFound {
		t.Errorf("код %d, ожидали 404", res.Code)
	}
	var all struct{ Entries []logfeed.Entry }
	getJSON(t, s.logEntries, "/api/logs/entries", &all)
	target := all.Entries[1]
	var ctx struct {
		Entries []logfeed.Entry
		Index   int
	}
	getJSON(t, s.logContext, "/api/logs/context?n=1&ts="+itoa(target.TS)+"&id="+target.ID, &ctx)
	if len(ctx.Entries) != 3 || ctx.Entries[ctx.Index].ID != target.ID {
		t.Errorf("контекст: %+v", ctx)
	}
}

// TestLogStreamSendsWholeEntries проверяет, что запись с traceback уходит одним событием.
func TestLogStreamSendsWholeEntries(t *testing.T) {
	s := serverWithLog(t, "")
	srv := httptest.NewServer(http.HandlerFunc(s.logStream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := bufio.NewScanner(res.Body)
	if !lines.Scan() || !strings.HasPrefix(lines.Text(), "event: state") {
		t.Fatalf("первым должно прийти состояние, получили %q", lines.Text())
	}

	f, err := os.OpenFile(s.bot.LogFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("2026-10-03 10:00:05 [ERROR] updates: failed\nTraceback\n  File \"x.py\"\n")
	_ = f.Close()

	for lines.Scan() {
		text := lines.Text()
		if !strings.HasPrefix(text, "data: ") || strings.Contains(text, `"alive"`) {
			continue
		}
		var e logfeed.Entry
		if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		if e.Module != "updates" || len(e.Trace) != 2 {
			t.Errorf("запись пришла не целиком: %+v", e)
		}
		return
	}
	t.Fatalf("запись не пришла: %v", lines.Err())
}
