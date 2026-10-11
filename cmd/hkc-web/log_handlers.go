package main

import (
	"encoding/json"
	"fmt"
	"heroku-console/logfeed"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// logs возвращает хвост журнала с ограничением числа строк из параметра limit.
func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 5000 {
			limit = parsed
		}
	}
	lines := logfeed.TailLines(s.bot.LogFile, limit)
	if mask := s.logMask(r); mask != nil {
		for i := range lines {
			lines[i] = mask(lines[i])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// logMask возвращает функцию, скрывающую секреты в журнале, либо nil, если читатель вправе видеть
// его как есть. Сырой журнал видит только оператор панели: он управляет ботом и без того. Наблюдатели,
// API-токены чтения и мини-приложение Telegram (данные уходят на серверы Telegram) получают текст
// с замаскированными токенами и ключами — так же, как ответы бота в чате.
func (s *server) logMask(r *http.Request) func(string) string {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if username, ok := s.sessions.get(cookie.Value); ok && s.auth.role(username) == "operator" {
			return nil
		}
	}
	return maskSecrets
}

// maskEntries скрывает секреты в сообщении и строках traceback записей.
func maskEntries(entries []logfeed.Entry, mask func(string) string) {
	if mask == nil {
		return
	}
	for i := range entries {
		entries[i].Message = mask(entries[i].Message)
		for j := range entries[i].Trace {
			entries[i].Trace[j] = mask(entries[i].Trace[j])
		}
	}
}

// events отправляет новые завершённые строки через SSE и поддерживает соединение служебными сообщениями.
// Отмена запроса останавливает читатель файла.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "потоковая передача недоступна", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	follower, err := logfeed.Follow(s.bot.LogFile, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Закрытие вкладки отменяет запрос; defer освобождает файл и горутину.
	defer follower.Stop()
	mask := s.logMask(r)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, open := <-follower.Lines:
			if !open {
				return
			}
			if mask != nil {
				line = mask(line)
			}
			payload, _ := json.Marshal(line)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-ticker.C:
			// Комментарий SSE поддерживает соединение, но не добавляет строку в журнал.
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// logQuery читает условия отбора записей из параметров запроса. Некорректные
// числа игнорируются: журнал важнее строгой проверки адресной строки.
func logQuery(r *http.Request) logfeed.Query {
	v := r.URL.Query()
	q := logfeed.Query{Module: v.Get("module"), Text: strings.TrimSpace(v.Get("q"))}
	for _, level := range strings.Split(v.Get("level"), ",") {
		if level = strings.ToUpper(strings.TrimSpace(level)); level != "" && level != "ALL" {
			q.Levels = append(q.Levels, level)
		}
	}
	q.From = queryInt64(v.Get("from"))
	q.To = queryInt64(v.Get("to"))
	return q
}

// queryInt64 читает неотрицательное число миллисекунд. Дробная часть отбрасывается: часы
// браузера, подстроенные под сервер, дают значения вроде 1790996401234.5, и молчаливый
// ноль вместо них снимал бы нижнюю границу времени.
func queryInt64(raw string) int64 {
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n < 0 {
			return 0
		}
		return n
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < 0 || f > 1<<62 {
		return 0
	}
	return int64(f)
}

// logEntries отдаёт записи журнала постранично: от конца к началу, с поиском по
// всем ротированным файлам. before и skip — курсор предыдущей страницы.
func (s *server) logEntries(w http.ResponseWriter, r *http.Request) {
	q := logQuery(r)
	mask := s.logMask(r)
	q.Mask = mask
	q.Limit = 200
	if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && parsed > 0 && parsed <= 500 {
		q.Limit = parsed
	}
	q.BeforeTS = queryInt64(r.URL.Query().Get("before"))
	if skip, err := strconv.Atoi(r.URL.Query().Get("skip")); err == nil && skip > 0 {
		q.BeforeSkip = skip
	}
	entries, more := logfeed.Search(s.bot.LogFile, q)
	if entries == nil {
		entries = []logfeed.Entry{}
	}
	maskEntries(entries, mask)
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "hasMore": more})
}

// logStats отдаёт число записей по интервалам для шкалы времени и список модулей.
func (s *server) logStats(w http.ResponseWriter, r *http.Request) {
	q := logQuery(r)
	q.Mask = s.logMask(r)
	to, from := q.To, q.From
	if to == 0 {
		to = time.Now().UnixMilli()
	}
	bucket := queryInt64(r.URL.Query().Get("bucket"))
	if bucket < 1000 {
		bucket = 5 * 60 * 1000
	}
	// Окно и размер интервала ограничены: запрос не должен строить миллионы корзин.
	if from == 0 || from >= to {
		from = to - 6*time.Hour.Milliseconds()
	}
	if (to-from)/bucket > 1000 {
		bucket = (to - from) / 1000
	}
	// Границы корзин лежат на сетке, привязанной к эпохе, а не к моменту запроса:
	// иначе при каждом опросе все столбики шкалы сдвигались бы на доли интервала
	// и шкала дёргалась. Окно расширено до целых корзин, чтобы текущая вошла целиком.
	now := time.Now().UnixMilli()
	from = from / bucket * bucket
	to = (to + bucket - 1) / bucket * bucket
	stats := logfeed.Collect(s.bot.LogFile, from, to, bucket, q)
	writeJSON(w, http.StatusOK, map[string]any{
		"buckets": stats.Buckets, "modules": stats.Modules, "total": stats.Total,
		"bucketMs": bucket, "now": now,
	})
}

// logContext отдаёт соседние записи вокруг выбранной: n с каждой стороны, а before и after
// задают число более ранних и более поздних отдельно — так окружение листается кнопками.
func (s *server) logContext(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	count := func(raw string, fallback int) int {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 200 {
			return parsed
		}
		return fallback
	}
	n := 20
	if parsed, err := strconv.Atoi(v.Get("n")); err == nil && parsed > 0 && parsed <= 50 {
		n = parsed
	}
	older, newer := count(v.Get("before"), n), count(v.Get("after"), n)
	entries, index, ok := logfeed.AroundN(s.bot.LogFile, queryInt64(v.Get("ts")), v.Get("id"), older, newer)
	if !ok {
		http.Error(w, "запись не найдена: возможно, файл уже ротирован", http.StatusNotFound)
		return
	}
	maskEntries(entries, s.logMask(r))
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "index": index})
}

// logProblems отдаёт частые ошибки за окно: одинаковые записи собраны в группы со счётчиком,
// временем первой и последней. По умолчанию — ошибки за последние 6 часов.
func (s *server) logProblems(w http.ResponseWriter, r *http.Request) {
	q := logQuery(r)
	mask := s.logMask(r)
	q.Mask = mask
	from, to := q.From, q.To
	if from == 0 {
		end := to
		if end == 0 {
			end = time.Now().UnixMilli()
		}
		from = end - 6*time.Hour.Milliseconds()
	}
	q.From, q.To = 0, 0
	limit := 20
	if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && parsed > 0 && parsed <= 50 {
		limit = parsed
	}
	var to1 int64
	if to > 0 {
		to1 = to + 1 // to в запросе включительно, как у /api/logs/entries
	}
	problems := logfeed.Problems(s.bot.LogFile, from, to1, q, limit)
	for i := range problems {
		last := []logfeed.Entry{problems[i].Last}
		maskEntries(last, mask)
		problems[i].Last = last[0]
		problems[i].Query = logfeed.SearchHint(problems[i].Last.Message)
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": problems, "from": from, "to": to})
}

// streamQuiet — сколько ждать продолжения записи, прежде чем отдать её. Строки
// traceback приходят сразу вслед за заголовком записи, поэтому полсекунды хватает,
// чтобы запись ушла целиком, а не по кусочкам.
const streamQuiet = 400 * time.Millisecond

// logStream отправляет новые записи целиком через SSE. Служебное событие state
// сообщает, читается ли файл: замерший журнал должен объяснять себя сам.
func (s *server) logStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "потоковая передача недоступна", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	follower, err := logfeed.Follow(s.bot.LogFile, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer follower.Stop()

	sendState := func() {
		alive, reason, _ := follower.Alive()
		payload, _ := json.Marshal(map[string]any{"alive": alive, "reason": reason})
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", payload)
		flusher.Flush()
	}
	mask := s.logMask(r)
	sendEntry := func(e logfeed.Entry) {
		maskEntries([]logfeed.Entry{e}, mask)
		payload, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	sendState()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	quiet := time.NewTimer(streamQuiet)
	quiet.Stop()
	defer quiet.Stop()
	var group logfeed.Grouper

	for {
		select {
		case <-r.Context().Done():
			return
		case line, open := <-follower.Lines:
			if !open {
				return
			}
			if e, done := group.Feed(line); done {
				sendEntry(e)
			}
			quiet.Reset(streamQuiet)
		case <-quiet.C:
			if e, done := group.Flush(); done {
				sendEntry(e)
			}
		case <-ticker.C:
			sendState()
		}
	}
}

// tailText соединяет последние строки файла в текст для диагностики.
func tailText(path string, maxLines int) string {
	return strings.Join(logfeed.TailLines(path, maxLines), "\n")
}
