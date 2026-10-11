package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heroku-console/logfeed"
)

// journalServer — сервер потока проверок с журналом панели и правилами журнала.
func journalServer(t *testing.T, botLog string) *flowServer {
	t.Helper()
	f := newFlowServer(t)
	f.panelLog = newPanelJournal(filepath.Join(f.dataDir, panelJournalName))
	f.logRules = openLogRules(filepath.Join(f.dataDir, "log-rules.json"))
	f.audit.onAdd = f.recordPanelAction
	if err := os.MkdirAll(filepath.Dir(f.bot.LogFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.bot.LogFile, []byte(botLog), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func journalMessages(t *testing.T, f *flowServer, cookie *http.Cookie, query string) []logfeed.Entry {
	t.Helper()
	res := f.do(t, flowRequest{method: "GET", path: "/api/logs/entries" + query, cookie: cookie})
	want(t, "лента", res, http.StatusOK)
	var body struct{ Entries []logfeed.Entry }
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Entries
}

// Действия с ботом и события панели попадают в ленту рядом с записями бота, вход — нет.
func TestPanelJournalRecordsActionsAndEvents(t *testing.T) {
	f := journalServer(t, "2026-10-03 10:00:00 [INFO] Core: started\n")
	operator := f.login(t, "olga")
	f.recordPanelAction(auditEvent{Time: time.Now(), Actor: "olga", Action: "bot.restart", Detail: "бот перезапущен"})
	f.recordPanelAction(auditEvent{Time: time.Now(), Actor: "olga", Action: "user.delete", Detail: "victor"})
	f.recordPanelEvent(alertEvent{Kind: "bot.stopped", Severity: "critical", Title: "Бот остановился", Message: "PID 42\nподробности"})
	f.recordPanelEvent(alertEvent{Kind: "test", Severity: "info", Title: "Проверка"})

	entries := journalMessages(t, f, operator, "?module=panel")
	var got []string
	for _, e := range entries {
		got = append(got, e.Level+" "+e.Message)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "INFO Перезапуск бота · olga: бот перезапущен") || !strings.Contains(joined, "ERROR Бот остановился — PID 42") {
		t.Fatalf("записи панели: %s", joined)
	}
	if strings.Contains(joined, "victor") || strings.Contains(joined, "Вход") || strings.Contains(joined, "Проверка") {
		t.Fatalf("лишние записи в журнале: %s", joined)
	}
	// Без фильтра лента сливает бота и панель по времени.
	all := journalMessages(t, f, operator, "")
	if len(all) < 3 || all[0].Message != "started" {
		t.Fatalf("общая лента: %+v", all)
	}
}

// Журнал панели переименовывается в .1 по размеру и читается вместе с ротацией.
func TestPanelJournalRotates(t *testing.T) {
	dir := t.TempDir()
	p := newPanelJournal(filepath.Join(dir, panelJournalName))
	p.max = 200
	for i := 0; i < 10; i++ {
		p.write(time.Date(2026, 10, 3, 10, 0, i, 0, time.Local), "INFO", modulePanel, fmt.Sprintf("событие номер %d", i), nil)
	}
	if _, err := os.Stat(p.path + ".1"); err != nil {
		t.Fatalf("ротация не создана: %v", err)
	}
	entries, _ := logfeed.Search(logfeed.Path(p.path), logfeed.Query{Limit: 100})
	if len(entries) < 2 || entries[len(entries)-1].Message != "событие номер 9" {
		t.Fatalf("после ротации: %+v", entries)
	}
}

// Сторож вывода: копии строк heroku.log отбрасываются, падение Python становится одной записью
// ERROR с исключением в заголовке, новый запуск бота читается с начала файла.
func TestProcessOutputKeepsOnlyWhatLogMissed(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "heroku.log")
	startup := filepath.Join(dir, ".startup.log")
	logged := []string{
		"2026-10-03 10:00:00 [ERROR] Core: handled failure",
		"Traceback (most recent call last):",
		`  File "core.py", line 1, in run`,
		"ValueError: handled",
	}
	if err := os.WriteFile(logFile, []byte(strings.Join(logged, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(startup, []byte("старый вывод до сторожа\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := newPanelJournal(filepath.Join(dir, panelJournalName))
	clock := time.Date(2026, 10, 3, 10, 0, 0, 0, time.Local)
	w := &processWatcher{startupLog: startup, logFile: logFile, statePath: filepath.Join(dir, "state.json"),
		journal: journal, records: newLoggedRecords(), now: func() time.Time { return clock }}
	for _, line := range logged {
		w.records.add(line)
	}
	w.loadState()
	appendFile := func(lines ...string) {
		f, err := os.OpenFile(startup, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(strings.Join(lines, "\n") + "\n")
		f.Close()
	}
	// Бот продублировал запись в консоль, затем упал с traceback, которого в heroku.log нет.
	appendFile(append(append([]string{}, logged...),
		"Traceback (most recent call last):",
		`  File "/opt/heroku/heroku/main.py", line 9, in main`,
		"RuntimeError: event loop is closed",
		"",
		"Killed")...)
	w.tick()
	clock = clock.Add(3 * time.Second)
	w.tick()
	entries, _ := logfeed.Search(logfeed.Path(journal.path), logfeed.Query{Limit: 10})
	if len(entries) != 2 {
		t.Fatalf("записи процесса: %+v", entries)
	}
	if entries[0].Level != "ERROR" || entries[0].Module != moduleProcess || entries[0].Message != "RuntimeError: event loop is closed" || len(entries[0].Trace) != 3 {
		t.Errorf("падение: %+v", entries[0])
	}
	if entries[1].Message != "Killed" || entries[1].Level != "ERROR" {
		t.Errorf("Killed: %+v", entries[1])
	}
	for _, e := range entries {
		if strings.Contains(e.Message, "старый вывод") {
			t.Fatal("вывод до запуска сторожа попал в журнал")
		}
	}
	// Позиция переживает перезапуск панели: повторно ничего не пишется.
	w2 := &processWatcher{startupLog: startup, logFile: logFile, statePath: w.statePath, journal: journal, records: newLoggedRecords(), now: w.now}
	w2.loadState()
	clock = clock.Add(5 * time.Second)
	w2.tick()
	if again, _ := logfeed.Search(logfeed.Path(journal.path), logfeed.Query{Limit: 10}); len(again) != 2 {
		t.Fatalf("повтор после перезапуска панели: %+v", again)
	}
	// Новый запуск бота пересоздаёт файл — он читается с начала.
	if err := os.WriteFile(startup, []byte("ModuleNotFoundError: No module named 'herokutl'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w2.tick()
	clock = clock.Add(3 * time.Second)
	w2.tick()
	last, _ := logfeed.Search(logfeed.Path(journal.path), logfeed.Query{Limit: 10})
	if len(last) != 3 || last[2].Message != "ModuleNotFoundError: No module named 'herokutl'" {
		t.Fatalf("новый запуск: %+v", last)
	}
}

// botEntries — записи ленты без журнала панели: действия с правилами тоже пишутся в ленту.
func botEntries(entries []logfeed.Entry) []logfeed.Entry {
	var out []logfeed.Entry
	for _, e := range entries {
		if e.Module != modulePanel {
			out = append(out, e)
		}
	}
	return out
}

// Правило «скрыть» убирает группу из ленты и сводки (hidden=1 показывает всё), «Частые ошибки»
// продолжают её показывать, а наблюдатель правило создать не может.
func TestLogRulesHideGroup(t *testing.T) {
	f := journalServer(t, `2026-10-03 10:00:00 [WARNING] net: медленный ответ 4.2 с
2026-10-03 10:00:01 [WARNING] net: медленный ответ 3.9 с
2026-10-03 10:00:02 [ERROR] updates: сбой
`)
	operator := f.login(t, "olga")
	viewer := f.login(t, "victor")
	want(t, "роль", f.do(t, flowRequest{method: "PATCH", path: "/api/admin/users/victor/role", body: `{"role":"viewer"}`, admin: true}), http.StatusOK)

	all := journalMessages(t, f, operator, "")
	want(t, "наблюдателю нельзя", f.do(t, flowRequest{method: "POST", path: "/api/logs/rules", cookie: viewer,
		body: fmt.Sprintf(`{"ts":%d,"id":%q,"hide":true}`, all[0].TS, all[0].ID)}), http.StatusForbidden)
	res := f.do(t, flowRequest{method: "POST", path: "/api/logs/rules", cookie: operator,
		body: fmt.Sprintf(`{"ts":%d,"id":%q,"hide":true}`, all[0].TS, all[0].ID)})
	want(t, "правило", res, http.StatusOK)

	if visible := botEntries(journalMessages(t, f, operator, "")); len(visible) != 1 || visible[0].Module != "updates" {
		t.Fatalf("скрытая группа осталась: %+v", visible)
	}
	if shown := botEntries(journalMessages(t, f, operator, "?hidden=1")); len(shown) != 3 {
		t.Fatalf("показ скрытых: %+v", shown)
	}
	stats := f.do(t, flowRequest{method: "GET", path: "/api/logs/stats?from=1", cookie: operator})
	var st struct{ Total int }
	_ = json.Unmarshal(stats.Body.Bytes(), &st)
	if st.Total != 2 { // ошибка updates и запись панели о правиле
		t.Errorf("сводка учитывает скрытые: %d", st.Total)
	}
	problems := f.do(t, flowRequest{method: "GET", path: "/api/logs/problems?from=1&level=warning", cookie: operator})
	var pr struct{ Problems []logfeed.Problem }
	_ = json.Unmarshal(problems.Body.Bytes(), &pr)
	if len(pr.Problems) != 1 || pr.Problems[0].Count != 2 || pr.Problems[0].Key == "" {
		t.Fatalf("частые ошибки должны показывать скрытую группу: %+v", pr.Problems)
	}
	// То же правило по подписи: снять скрытие — правило удаляется.
	body, _ := json.Marshal(map[string]any{"key": pr.Problems[0].Key, "hide": false})
	want(t, "снять", f.do(t, flowRequest{method: "POST", path: "/api/logs/rules", cookie: operator, body: string(body)}), http.StatusOK)
	if rules := f.logRules.list(); len(rules) != 0 {
		t.Fatalf("правило без действий должно удаляться: %+v", rules)
	}
	if visible := botEntries(journalMessages(t, f, operator, "")); len(visible) != 3 {
		t.Fatalf("после снятия: %+v", visible)
	}
	// Правило попадает в журнал действий и в ленту панели.
	if panel := journalMessages(t, f, operator, "?module=panel"); len(panel) != 2 || !strings.Contains(panel[0].Message, "Изменено правило журнала") {
		t.Fatalf("действия с правилами в журнале: %+v", panel)
	}
}

// Уведомление по правилу уходит, когда группа повторилась N раз за окно, и не повторяется
// раньше, чем через окно.
func TestLogRuleNotifyThreshold(t *testing.T) {
	now := time.Now()
	stamp := func(ago time.Duration) string { return now.Add(-ago).Local().Format("2006-01-02 15:04:05") }
	f := journalServer(t, strings.Join([]string{
		stamp(20*time.Minute) + " [ERROR] net: таймаут через 10 с",
		stamp(4*time.Minute) + " [ERROR] net: таймаут через 11 с",
		stamp(3*time.Minute) + " [ERROR] net: таймаут через 12 с",
		stamp(2*time.Minute) + " [ERROR] net: таймаут через 13 с",
	}, "\n")+"\n")
	var sent []alertEvent
	f.alerts = &alertDispatcher{record: func(e alertEvent) { sent = append(sent, e) }}
	operator := f.login(t, "olga")
	entries := journalMessages(t, f, operator, "")
	body := fmt.Sprintf(`{"ts":%d,"id":%q,"notify":{"count":3,"minutes":10}}`, entries[0].TS, entries[0].ID)
	want(t, "уведомлять", f.do(t, flowRequest{method: "POST", path: "/api/logs/rules", cookie: operator, body: body}), http.StatusOK)

	f.checkLogRuleAlerts(now)
	if len(sent) != 1 || sent[0].Kind != "log.rule" || !strings.Contains(sent[0].Message, "3 раз за 10 мин") || sent[0].Severity != "critical" {
		t.Fatalf("уведомление: %+v", sent)
	}
	f.checkLogRuleAlerts(now.Add(time.Minute))
	if len(sent) != 1 {
		t.Fatalf("повтор раньше окна: %+v", sent)
	}
	// Порог выше числа записей — уведомления нет.
	body = fmt.Sprintf(`{"ts":%d,"id":%q,"notify":{"count":9,"minutes":10}}`, entries[0].TS, entries[0].ID)
	want(t, "порог", f.do(t, flowRequest{method: "POST", path: "/api/logs/rules", cookie: operator, body: body}), http.StatusOK)
	f.checkLogRuleAlerts(now.Add(30 * time.Minute))
	if len(sent) != 1 {
		t.Fatalf("уведомление ниже порога: %+v", sent)
	}
	want(t, "неверный порог", f.do(t, flowRequest{method: "POST", path: "/api/logs/rules", cookie: operator,
		body: `{"key":"x","notify":{"count":0,"minutes":10}}`}), http.StatusBadRequest)
}

// Уровни логгеров: запись файла управления, снятие, проверка ввода и признак доступности.
func TestLogLevelsControlFile(t *testing.T) {
	f := journalServer(t, "")
	operator := f.login(t, "olga")
	want(t, "уровень", f.do(t, flowRequest{method: "POST", path: "/api/logs/levels", cookie: operator,
		body: `{"logger":"git.cmd","level":"warning","minutes":15}`}), http.StatusOK)
	want(t, "второй", f.do(t, flowRequest{method: "POST", path: "/api/logs/levels", cookie: operator,
		body: `{"logger":"heroku.modules.loader","level":"DEBUG"}`}), http.StatusOK)
	data, err := os.ReadFile(f.logLevelsPath())
	if err != nil {
		t.Fatal(err)
	}
	var file logLevelsFileData
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Levels["git.cmd"].Level != "WARNING" || file.Levels["git.cmd"].Until == 0 || file.Levels["heroku.modules.loader"].Until != 0 {
		t.Fatalf("файл управления: %s", data)
	}
	res := f.do(t, flowRequest{method: "GET", path: "/api/logs/levels", cookie: operator})
	got := decode(t, res)
	if got["available"] != false || len(got["levels"].([]any)) != 2 {
		t.Fatalf("список уровней без бота: %s", res.Body.String())
	}
	want(t, "снять", f.do(t, flowRequest{method: "POST", path: "/api/logs/levels", cookie: operator,
		body: `{"logger":"git.cmd","level":""}`}), http.StatusOK)
	if levels := f.readLogLevels(time.Now()); len(levels) != 1 {
		t.Fatalf("после снятия: %+v", levels)
	}
	for _, bad := range []string{`{"logger":"../x","level":"DEBUG"}`, `{"logger":"a","level":"LOUD"}`, `{"logger":"a","level":"INFO","minutes":-1}`} {
		want(t, bad, f.do(t, flowRequest{method: "POST", path: "/api/logs/levels", cookie: operator, body: bad}), http.StatusBadRequest)
	}
	// Истёкший уровень не возвращается и не мешает.
	expired := logLevelsFileData{Levels: map[string]logLevelSetting{"old": {Level: "DEBUG", Until: float64(time.Now().Add(-time.Minute).Unix())}}}
	data, _ = json.Marshal(expired)
	_ = os.WriteFile(f.logLevelsPath(), data, 0o644)
	if levels := f.readLogLevels(time.Now()); len(levels) != 0 {
		t.Fatalf("истёкший уровень: %+v", levels)
	}
}

// Поток журнала отдаёт записи панели сразу и помечает записи, скрытые правилом.
func TestLogStreamPanelEntriesAndHiddenFlag(t *testing.T) {
	f := journalServer(t, "2026-10-03 10:00:00 [WARNING] net: медленный ответ 4.2 с\n")
	entries, _ := logfeed.Search(f.journal(), logfeed.Query{Limit: 1})
	rule := logRule{Key: logfeed.ProblemKey(entries[0]), Module: "net", Hide: true}
	if _, err := f.logRules.upsert(rule); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(f.logStream))
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
		t.Fatalf("первым должно прийти состояние: %q", lines.Text())
	}
	f.panelLog.write(time.Now(), "INFO", modulePanel, "Перезапуск бота · olga", nil)
	appendLog, _ := os.OpenFile(f.bot.LogFile, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = appendLog.WriteString("2026-10-03 10:00:05 [WARNING] net: медленный ответ 5.1 с\n")
	_ = appendLog.Close()
	seen := map[string]bool{}
	for lines.Scan() && len(seen) < 2 {
		text := lines.Text()
		if !strings.HasPrefix(text, "data: ") || strings.Contains(text, `"alive"`) {
			continue
		}
		var e struct {
			logfeed.Entry
			Hidden bool `json:"hidden"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		switch e.Module {
		case modulePanel:
			if e.Hidden || e.Message != "Перезапуск бота · olga" || e.ID == "" {
				t.Errorf("запись панели: %+v", e)
			}
		case "net":
			if !e.Hidden {
				t.Errorf("скрытая правилом запись без пометки: %+v", e)
			}
		}
		seen[e.Module] = true
	}
	if !seen[modulePanel] || !seen["net"] {
		t.Fatalf("пришли не все записи: %v (%v)", seen, lines.Err())
	}
}
