package logfeed

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Journal — журнал из нескольких источников, которые читаются как один поток, слитый по
// времени: файл бота с его ротациями и архивом и журнал самой панели (вывод процесса бота,
// действия и события панели).
type Journal struct {
	Sources []Source
}

// Source — один источник: основной файл, его ротации path.1, path.2 … и, если задан,
// каталог сжатых копий ротаций (см. ArchiveRotations).
type Source struct {
	Path    string
	Archive string
}

// Path — журнал из одного файла с ротациями.
func Path(path string) Journal { return Journal{Sources: []Source{{Path: path}}} }

// archiveSuffix — окончание имени сжатой копии: heroku.log-1790000000000-0a1b2c3d4e5f.log.gz.
const archiveSuffix = ".log.gz"

// archiveFile — сжатая копия ротации: время первой записи и отпечаток начала исходного файла.
type archiveFile struct {
	path    string
	firstTS int64
	hash    string
	size    int64
}

// archives перечисляет сжатые копии источника, самые новые первыми.
func (s Source) archives() []archiveFile {
	if s.Archive == "" {
		return nil
	}
	entries, err := os.ReadDir(s.Archive)
	if err != nil {
		return nil
	}
	prefix := filepath.Base(s.Path) + "-"
	var out []archiveFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, archiveSuffix) {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, prefix), archiveSuffix), "-")
		if len(parts) != 2 {
			continue
		}
		ts, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, archiveFile{path: filepath.Join(s.Archive, name), firstTS: ts, hash: parts[1], size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].firstTS > out[j].firstTS })
	return out
}

// files возвращает файлы источника от новых к старым: основной файл, ротации, затем архив.
// Копия ротации, которая ещё лежит рядом с основным файлом, пропускается — иначе её записи
// попали бы в поток дважды.
func (s Source) files() []string {
	live := Files(s.Path)
	out := make([]string, 0, len(live))
	liveHashes := map[string]bool{}
	for i := len(live) - 1; i >= 0; i-- {
		out = append(out, live[i])
		if live[i] != s.Path {
			if hash, err := headHash(live[i]); err == nil {
				liveHashes[hash] = true
			}
		}
	}
	for _, a := range s.archives() {
		if !liveHashes[a.hash] {
			out = append(out, a.path)
		}
	}
	return out
}

// headBytes — сколько байт начала файла входит в его отпечаток. Ротация после переименования
// не дописывается, поэтому начала достаточно, чтобы узнать файл в архиве.
const headBytes = 64 * 1024

func headHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyN(h, f, headBytes); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// sourceBackwards отдаёт записи одного источника от новых к старым, файл за файлом.
func (s Source) backwards(yield func(Entry) bool) {
	for _, path := range s.files() {
		stopped := false
		eachEntryBackwards(path, 0, func(e Entry) bool {
			if !yield(e) {
				stopped = true
				return false
			}
			return true
		})
		if stopped {
			return
		}
	}
}

// backwards отдаёт записи всех источников от новых к старым. Один источник читается напрямую;
// несколько сливаются по времени: на каждом шаге берётся самая свежая из голов источников.
// При равном времени раньше идёт источник, указанный раньше, — порядок постоянен, и курсор
// страниц (время + сколько записей с этим временем пропустить) работает так же, как для файла.
func (j Journal) backwards(yield func(Entry) bool) {
	if len(j.Sources) == 1 {
		j.Sources[0].backwards(yield)
		return
	}
	type head struct {
		next  func() (Entry, bool)
		stop  func()
		cur   Entry
		ok    bool
		order int64 // время для сравнения: у записи без времени — время предыдущей в источнике
	}
	heads := make([]*head, 0, len(j.Sources))
	for _, src := range j.Sources {
		next, stop := iter.Pull(iter.Seq[Entry](src.backwards))
		heads = append(heads, &head{next: next, stop: stop, order: 1<<62 - 1})
	}
	defer func() {
		for _, h := range heads {
			h.stop()
		}
	}()
	advance := func(h *head) {
		e, ok := h.next()
		h.cur, h.ok = e, ok
		if ok && e.TS > 0 {
			h.order = e.TS
		}
	}
	for _, h := range heads {
		advance(h)
	}
	for {
		var best *head
		for _, h := range heads {
			if h.ok && (best == nil || h.order > best.order) {
				best = h
			}
		}
		if best == nil {
			return
		}
		if !yield(best.cur) {
			return
		}
		advance(best)
	}
}

// Backwards отдаёт записи журнала от новых к старым, пока yield возвращает true. Строки записи
// ссылаются на прочитанный блок: кто хранит запись дольше вызова, копирует нужные поля.
func (j Journal) Backwards(yield func(Entry) bool) { j.backwards(yield) }

// --- Сжатые копии ---

// gzCache хранит последние распакованные копии: листание страниц и повторный поиск не
// распаковывают один и тот же файл каждый раз.
var gzCache struct {
	sync.Mutex
	items []gzItem
}

type gzItem struct {
	path  string
	mtime time.Time
	data  []byte
}

const (
	gzCacheItems = 3
	gzMaxBytes   = 128 << 20 // распакованная копия больше этого считается повреждённой
)

// readArchive распаковывает сжатую копию целиком (с учётом кеша).
func readArchive(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	gzCache.Lock()
	for _, item := range gzCache.items {
		if item.path == path && item.mtime.Equal(info.ModTime()) {
			gzCache.Unlock()
			return item.data, nil
		}
	}
	gzCache.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, gzMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > gzMaxBytes {
		return nil, fmt.Errorf("%s: распакованная копия больше %d МБ", path, gzMaxBytes>>20)
	}
	gzCache.Lock()
	gzCache.items = append([]gzItem{{path: path, mtime: info.ModTime(), data: data}}, gzCache.items...)
	if len(gzCache.items) > gzCacheItems {
		gzCache.items = gzCache.items[:gzCacheItems]
	}
	gzCache.Unlock()
	return data, nil
}

// ArchiveRotations сохраняет сжатые копии ротаций источника, которых ещё нет в архиве, и
// удаляет старые копии сверх keepBytes (суммарный размер сжатых файлов) или старше keepAge.
// Бот держит одну ротацию и при следующей перезаписывает её; копия, снятая сразу после
// появления ротации, переживает перезапись. Нулевые пределы не ограничивают архив.
func ArchiveRotations(src Source, keepBytes int64, keepAge time.Duration, now time.Time) error {
	if src.Archive == "" {
		return nil
	}
	known := map[string]bool{}
	for _, a := range src.archives() {
		known[a.hash] = true
	}
	var firstErr error
	for _, path := range Files(src.Path) {
		if path == src.Path {
			continue
		}
		hash, err := headHash(path)
		if err != nil || known[hash] {
			continue
		}
		if err := archiveOne(src, path, hash); err != nil && firstErr == nil {
			firstErr = err
		}
		known[hash] = true
	}
	// Уборка: самые новые копии остаются, пока укладываются в пределы.
	var total int64
	for i, a := range src.archives() {
		total += a.size
		tooOld := keepAge > 0 && a.firstTS > 0 && now.Sub(time.UnixMilli(a.firstTS)) > keepAge
		tooBig := keepBytes > 0 && total > keepBytes && i > 0
		if tooOld || tooBig {
			_ = os.Remove(a.path)
		}
	}
	return firstErr
}

// archiveOne сжимает один файл ротации во временный файл архива и переименовывает его, так
// что читатели никогда не видят половину копии.
func archiveOne(src Source, path, hash string) error {
	var firstTS int64
	eachEntryForward(path, func(e Entry) bool {
		if e.TS > 0 {
			firstTS = e.TS
			return false
		}
		return true
	})
	if err := os.MkdirAll(src.Archive, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%d-%s%s", filepath.Base(src.Path), firstTS, hash, archiveSuffix)
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(src.Archive, ".archive-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw, _ := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if _, err := io.Copy(zw, in); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(src.Archive, name))
}

// eachEntryForward читает записи файла по порядку; нужен только для начала файла.
func eachEntryForward(path string, yield func(Entry) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	head := make([]byte, headBytes)
	n, _ := io.ReadFull(f, head)
	g := &Grouper{}
	for _, line := range strings.Split(string(bytes.TrimRight(head[:n], "\n")), "\n") {
		if e, ok := g.Feed(line); ok && !yield(e) {
			return
		}
	}
	if e, ok := g.Flush(); ok {
		yield(e)
	}
}

// ArchiveStats — сводка архива для админки: число копий, их размер и самая ранняя запись.
type ArchiveStats struct {
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	FirstTS int64 `json:"firstTs"`
}

// Stats возвращает сводку архива источника.
func (s Source) Stats() ArchiveStats {
	var st ArchiveStats
	for _, a := range s.archives() {
		st.Files++
		st.Bytes += a.size
		if a.firstTS > 0 && (st.FirstTS == 0 || a.firstTS < st.FirstTS) {
			st.FirstTS = a.firstTS
		}
	}
	return st
}
