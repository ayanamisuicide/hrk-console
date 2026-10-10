package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	hostHistoryLimit  = 24 * 60 * 60 // секундные замеры за 24 часа
	historyGraphLimit = 1200
)

// hostPoint — Секундный замер процентов ресурсов и PID для определения смены процесса.
type hostPoint struct {
	At     time.Time `json:"at"`
	CPU    float64   `json:"cpu"`
	Memory float64   `json:"memory"`
	Disk   float64   `json:"disk"`
	PID    int       `json:"pid"`
}

// hostHistoryStore — История в хронологическом порядке и путь JSONL; читателям возвращаются копии, запись
// защищена мьютексом.
type hostHistoryStore struct {
	mu          sync.RWMutex
	path        string
	points      []hostPoint
	lastCompact time.Time
}

// newHostHistoryStore восстанавливает историю JSONL и поддерживает старый JSON-массив. Повреждённые записи
// не препятствуют чтению остальных.
func newHostHistoryStore(path string) *hostHistoryStore {
	store := &hostHistoryStore{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && strings.HasSuffix(path, ".jsonl") {
		// Подхватываем историю старого формата, записанную предыдущими версиями.
		data, err = os.ReadFile(strings.TrimSuffix(path, "l"))
	}
	if err == nil {
		if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '[' {
			if decodeErr := json.Unmarshal(data, &store.points); decodeErr != nil {
				log.Printf("история хоста: нечитаемый старый файл %s сохранён без изменений: %v", path, decodeErr)
				store.points = nil
			} else if compactErr := store.compact(); compactErr != nil {
				log.Printf("история хоста: ошибка преобразования формата: %v", compactErr)
			}
		} else {
			invalid := 0
			for _, line := range bytes.Split(data, []byte{'\n'}) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				var point hostPoint
				if json.Unmarshal(line, &point) == nil {
					store.points = append(store.points, point)
				} else {
					invalid++
				}
			}
			if invalid > 0 {
				log.Printf("история хоста: пропущено нечитаемых записей: %d, файл: %s; исходный файл сохранён", invalid, path)
			}
		}
		store.trim(time.Now())
	}
	store.lastCompact = time.Now()
	return store
}

// trim оставляет последние сутки и отсекает слишком далёкие будущие точки. Точки должны идти по времени;
// блокировка принадлежит вызывающему коду.
func (h *hostHistoryStore) trim(now time.Time) {
	cutoff := now.Add(-24 * time.Hour)
	start := sort.Search(len(h.points), func(i int) bool { return !h.points[i].At.Before(cutoff) })
	h.points = h.points[start:]
	end := sort.Search(len(h.points), func(i int) bool { return h.points[i].At.After(now.Add(time.Minute)) })
	h.points = h.points[:end]
	if len(h.points) > hostHistoryLimit {
		h.points = h.points[len(h.points)-hostHistoryLimit:]
	}
}

// add добавляет секундный замер под блокировкой и дописывает JSONL; раз в час уплотняет файл.
func (h *hostHistoryStore) add(point hostPoint) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.points = append(h.points, point)
	h.trim(point.At)
	data, err := json.Marshal(point)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(h.path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if point.At.Sub(h.lastCompact) >= time.Hour {
		if err = h.compact(); err == nil {
			h.lastCompact = point.At
		}
	}
	return err
}

// compact переписывает только сохраняемые точки через временный файл. Вызывается до публикации хранилища
// или под его блокировкой.
func (h *hostHistoryStore) compact() error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), "host-history-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		for _, point := range h.points {
			var data []byte
			data, err = json.Marshal(point)
			if err != nil {
				break
			}
			_, err = tmp.Write(append(data, '\n'))
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), h.path); err != nil && runtime.GOOS == "windows" {
		// Если Windows отклонила замену переименованием, используем прямую запись.
		// Этот запасной путь не обладает атомарностью основного пути.
		var output bytes.Buffer
		for _, point := range h.points {
			data, marshalErr := json.Marshal(point)
			if marshalErr != nil {
				return marshalErr
			}
			output.Write(data)
			output.WriteByte('\n')
		}
		return os.WriteFile(h.path, output.Bytes(), 0600)
	}
	return err
}

// since возвращает копию точек после границы времени, не отдавая наружу внутренний срез.
func (h *hostHistoryStore) since(cutoff time.Time) []hostPoint {
	h.mu.RLock()
	defer h.mu.RUnlock()
	index := sort.Search(len(h.points), func(i int) bool { return !h.points[i].At.Before(cutoff) })
	return append([]hostPoint(nil), h.points[index:]...)
}

// sampledSince возвращает сокращённые точки графика и исходное число замеров под одной блокировкой чтения.
func (h *hostHistoryStore) sampledSince(cutoff time.Time, limit int) ([]hostPoint, int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	index := sort.Search(len(h.points), func(i int) bool { return !h.points[i].At.Before(cutoff) })
	points := h.points[index:]
	return downsampleHostPoints(points, limit), len(points)
}

// restartTimes находит времена смены PID, включая границу выбранного интервала.
func (h *hostHistoryStore) restartTimes(cutoff time.Time) []time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	index := sort.Search(len(h.points), func(i int) bool { return !h.points[i].At.Before(cutoff) })
	result := []time.Time{}
	for i := max(1, index); i < len(h.points); i++ {
		if h.points[i].PID != h.points[i-1].PID {
			result = append(result, h.points[i].At)
		}
	}
	return result
}

// hostPercent переводит объём в проценты, избегая деления на ноль.
func hostPercent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// collectHostHistory раз в секунду обновляет системную сводку и сохраняет замер с PID. Контекст
// останавливает фоновый цикл.
func (s *server) collectHostHistory(ctx context.Context) {
	collect := func() {
		status := readSystemStatus(s.bot.HerokuDir)
		s.systemMu.Lock()
		s.latestSystem = status
		s.systemMu.Unlock()
		if !status.Supported {
			return
		}
		_ = s.hostHistory.add(hostPoint{At: time.Now(), CPU: status.CPUPercent,
			Memory: hostPercent(status.MemoryUsed, status.MemoryTotal),
			Disk:   hostPercent(status.DiskUsed, status.DiskTotal), PID: s.bot.PID()})
	}
	collect()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

// systemHistory выбирает временной диапазон и возвращает ограниченную по плотности историю для графика.
func (s *server) systemHistory(w http.ResponseWriter, r *http.Request) {
	duration := time.Hour
	switch r.URL.Query().Get("range") {
	case "live":
		duration = 5 * time.Minute
	case "24h":
		duration = 24 * time.Hour
	}
	points := []hostPoint{}
	count := 0
	if s.hostHistory != nil {
		points, count = s.hostHistory.sampledSince(time.Now().Add(-duration), historyGraphLimit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": duration.String(), "sampleCount": count, "intervalSeconds": 1, "points": points})
}

// downsampleHostPoints выбирает равномерные точки, дополнительно сохраняя соседей смены PID. Поэтому число
// точек может немного превышать лимит графика.
func downsampleHostPoints(points []hostPoint, limit int) []hostPoint {
	if len(points) <= limit {
		return append([]hostPoint(nil), points...)
	}
	result := make([]hostPoint, 0, limit+16)
	// Оставляем начало и конец и равномерно выбираем промежуточные индексы.
	// Границы смены процесса добавим отдельно, чтобы сокращение их не потеряло.
	step := float64(len(points)-1) / float64(limit-1)
	last := -1
	for i := 0; i < limit; i++ {
		index := int(float64(i)*step + 0.5)
		if index <= last {
			continue
		}
		result = append(result, points[index])
		last = index
	}
	// Сохраняем границы смены PID, даже если они оказались между выбранными точками.
	for i := 1; i < len(points); i++ {
		if points[i].PID != points[i-1].PID {
			result = append(result, points[i-1], points[i])
		}
	}
	// Добавленные маркеры возвращаем в хронологический порядок графика.
	sort.Slice(result, func(i, j int) bool { return result[i].At.Before(result[j].At) })
	return result
}
