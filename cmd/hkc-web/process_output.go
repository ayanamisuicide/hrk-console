package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"heroku-console/logfeed"
)

// Вывод процесса бота (stdout и stderr) пишется в .startup.log. Почти всё в нём — копия строк
// heroku.log: обработчик консоли бота дублирует записи от INFO и выше. Ценно то, чего в heroku.log
// нет: traceback, напечатанный Python при падении, ошибки импорта до запуска логирования, print()
// модулей, «Killed» от нехватки памяти. Сторож читает новые строки вывода, отбрасывает строки,
// которые есть в heroku.log, а остальное пишет в журнал панели как записи модуля process.

const (
	processPoll = time.Second
	// processGrace — сколько ждать строку в heroku.log: бот пишет в оба места почти разом,
	// но сторож heroku.log может прочитать её позже.
	processGrace = 2 * time.Second
	// processRecent — сколько последних строк heroku.log помнить для сравнения.
	processRecent = 20000
	// processChunk ограничивает чтение за один проход: поток print() не должен съесть память.
	processChunk = 1 << 20
)

// processState — позиция чтения вывода, переживающая перезапуск панели.
type processState struct {
	Head    string `json:"head"`    // отпечаток первых HeadLen байт: новый запуск бота — новый файл
	HeadLen int64  `json:"headLen"` // сколько байт начала вошло в отпечаток (до 512)
	Offset  int64  `json:"offset"`  // сколько байт уже разобрано
}

// loggedRecords помнит недавние записи heroku.log: заголовок записи и сколько строк продолжения
// (traceback) у неё в файле. В выводе процесса запись бота повторяется с тем же заголовком; если
// за ней идёт больше строк, чем в heroku.log, лишние напечатал уже не логгер, а сам Python —
// например, traceback падения сразу после последней записи.
type loggedRecords struct {
	mu      sync.Mutex
	order   []uint64
	lengths map[uint64]int
	current uint64
	open    bool
}

func newLoggedRecords() *loggedRecords { return &loggedRecords{lengths: map[uint64]int{}} }

func lineKey(line string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimRight(line, "\r")))
	return h.Sum64()
}

// add принимает строку heroku.log по порядку: заголовок открывает запись, остальное — её продолжение.
func (r *loggedRecords) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := logfeed.ParseLine(strings.TrimRight(line, "\r")); ok {
		key := lineKey(line)
		if _, known := r.lengths[key]; !known {
			r.order = append(r.order, key)
		}
		r.lengths[key] = 0
		r.current, r.open = key, true
		if len(r.order) > processRecent {
			delete(r.lengths, r.order[0])
			r.order = r.order[1:]
		}
		return
	}
	if r.open {
		r.lengths[r.current]++
	}
}

// traceLen — число строк продолжения у записи с этим заголовком; -1, если запись не найдена.
func (r *loggedRecords) traceLen(header string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.lengths[lineKey(header)]; ok {
		return n
	}
	return -1
}

// pendingLine — строка вывода, которая ждёт проверки по heroku.log.
type pendingLine struct {
	text string
	at   time.Time
}

type processWatcher struct {
	startupLog string
	logFile    string
	statePath  string
	journal    *panelJournal
	records    *loggedRecords
	state      processState
	// skip — сколько строк продолжения текущей записи бота ещё повторяют heroku.log;
	// -1 — запись в heroku.log не найдена, её продолжения считаются копией до следующего заголовка.
	skip    int
	partial string
	pending []pendingLine
	now     func() time.Time
}

// runProcessOutput запускает сторожа вывода процесса до отмены контекста.
func (s *server) runProcessOutput(ctx context.Context) {
	if s.panelLog == nil || s.dataDir == "" {
		return
	}
	w := &processWatcher{
		startupLog: s.bot.StartupLog,
		logFile:    s.bot.LogFile,
		statePath:  filepath.Join(s.dataDir, "process-output.json"),
		journal:    s.panelLog,
		records:    newLoggedRecords(),
		now:        time.Now,
	}
	w.run(ctx)
}

func (w *processWatcher) run(ctx context.Context) {
	for _, line := range logfeed.TailLines(w.logFile, processRecent) {
		w.records.add(line)
	}
	follower, err := logfeed.Follow(w.logFile, 0)
	if err == nil {
		defer follower.Stop()
		go func() {
			for line := range follower.Lines {
				w.records.add(line)
			}
		}()
	}
	w.loadState()
	ticker := time.NewTicker(processPoll)
	defer ticker.Stop()
	for {
		w.tick()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *processWatcher) loadState() {
	data, err := os.ReadFile(w.statePath)
	if err == nil && json.Unmarshal(data, &w.state) == nil {
		return
	}
	// Первый запуск сторожа: прежний вывод не разбираем, иначе в журнал хлынула бы старая история.
	w.state = processState{Offset: fileSize(w.startupLog)}
	w.state.HeadLen, w.state.Head = fileHead(w.startupLog, w.state.Offset)
	w.saveState()
}

func (w *processWatcher) saveState() {
	data, _ := json.Marshal(w.state)
	tmp := w.statePath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, w.statePath)
	}
}

// tick читает новые байты вывода, откладывает строки на проверку и выпускает проверенные.
func (w *processWatcher) tick() {
	size := fileSize(w.startupLog)
	if _, head := fileHead(w.startupLog, w.state.HeadLen); size < w.state.Offset || head != w.state.Head {
		// Бот запущен заново: файл пересоздан. Недописанная строка прежнего запуска ушла вместе с ним.
		w.state = processState{}
		w.partial = ""
		w.skip = 0
	}
	// Пока файл короче 512 байт, отпечаток растёт вместе с ним: начало уже проверено выше.
	if w.state.HeadLen < 512 && size > w.state.HeadLen {
		w.state.HeadLen, w.state.Head = fileHead(w.startupLog, size)
	}
	if size > w.state.Offset {
		if chunk := readRange(w.startupLog, w.state.Offset, processChunk); len(chunk) > 0 {
			w.state.Offset += int64(len(chunk))
			text := w.partial + string(chunk)
			lines := strings.Split(text, "\n")
			w.partial = lines[len(lines)-1]
			now := w.now()
			for _, line := range lines[:len(lines)-1] {
				w.pending = append(w.pending, pendingLine{text: strings.TrimRight(line, "\r"), at: now})
			}
			w.saveState()
		}
	}
	w.release()
}

// release выпускает строки, ждавшие processGrace (за это время запись успевает появиться в
// heroku.log): копии записей бота отбрасываются, остальное собирается в записи — соседние строки
// одного всплеска вывода становятся одной записью.
func (w *processWatcher) release() {
	cutoff := w.now().Add(-processGrace)
	var ready []pendingLine
	n := 0
	for n < len(w.pending) && !w.pending[n].at.After(cutoff) {
		ready = append(ready, w.pending[n])
		n++
	}
	w.pending = w.pending[n:]
	var group []string
	var groupAt time.Time
	flush := func() {
		if len(group) > 0 {
			level, message, trace := describeProcessOutput(group)
			w.journal.write(groupAt, level, moduleProcess, message, trace)
		}
		group = nil
	}
	for _, line := range ready {
		if w.duplicate(line.text) {
			flush()
			continue
		}
		if strings.TrimSpace(line.text) == "" {
			flush()
			continue
		}
		if len(group) == 0 {
			groupAt = line.at
		}
		group = append(group, line.text)
		if len(group) >= 200 {
			flush()
		}
	}
	flush()
}

// duplicate — строка повторяет heroku.log: заголовок записи бота или одна из строк её продолжения,
// которые есть и в файле. Заголовок отбрасывается всегда, даже если heroku.log недоступен.
func (w *processWatcher) duplicate(line string) bool {
	if _, ok := logfeed.ParseLine(line); ok {
		w.skip = w.records.traceLen(line)
		return true
	}
	switch {
	case w.skip > 0:
		w.skip--
		return true
	case w.skip < 0:
		return true
	}
	return false
}

var (
	processErrorLine = regexp.MustCompile(`^[A-Za-z_][\w.]*(Error|Exception|Interrupt|Exit|Warning)\b`)
	processFatal     = regexp.MustCompile(`(?i)\b(killed|segmentation fault|fatal|panic|traceback|core dumped|out of memory|error|exception)\b`)
)

// describeProcessOutput выбирает уровень и заголовок записи по строкам вывода. Для traceback
// заголовок — строка исключения («ValueError: …»), для «Killed» — она сама, иначе первая строка.
func describeProcessOutput(lines []string) (level, message string, trace []string) {
	level = "INFO"
	message = strings.TrimSpace(lines[0])
	for _, line := range lines {
		if processFatal.MatchString(line) {
			level = "ERROR"
			break
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); processErrorLine.MatchString(line) {
			message = line
			level = "ERROR"
			break
		}
	}
	if len(lines) > 1 {
		trace = lines
	}
	return level, truncateRunes(message, 500), trace
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// fileHead — отпечаток первых limit байт файла (не больше 512) и их число; у пересозданного
// файла начало другое: в первой строке вывода стоит время запуска.
func fileHead(path string, limit int64) (int64, string) {
	if limit > 512 {
		limit = 512
	}
	if limit <= 0 {
		return 0, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer f.Close()
	head := make([]byte, limit)
	n, _ := io.ReadFull(f, head)
	sum := sha256.Sum256(head[:n])
	return int64(n), hex.EncodeToString(sum[:8])
}

func readRange(path string, offset, limit int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	_, _ = io.CopyN(&buf, f, limit)
	return buf.Bytes()
}
