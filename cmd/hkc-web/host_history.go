package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
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
	// Поля ниже появились в 2.5.0; в старых записях они отсутствуют и читаются нулями.
	BotCPU float64 `json:"botCpu,omitempty"`
	BotRSS uint64  `json:"botRss,omitempty"`
	RxRate float64 `json:"rx,omitempty"`
	TxRate float64 `json:"tx,omitempty"`
	TgMS   float64 `json:"tgMs,omitempty"`
	// CPUMax заполняется только при сжатии для графика: пик CPU внутри интервала.
	CPUMax float64 `json:"cpuMax,omitempty"`
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
	return bucketHostPoints(points, limit), len(points)
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
		now := time.Now()
		status := readSystemStatus(s.bot.HerokuDir)
		processes := s.procs.scan()
		pid := s.bot.PID()
		bot, _ := processTree(processes, pid)
		rx, tx := readNetDev()
		live := liveSample{bot: bot, processes: processes, network: s.netCounter.observe(now, rx, tx)}
		s.systemMu.Lock()
		s.latestSystem = status
		s.latestLive = live
		s.systemMu.Unlock()
		if !status.Supported {
			return
		}
		s.observeHostAlerts(now, status)
		_ = s.hostHistory.add(hostPoint{At: now, CPU: status.CPUPercent,
			Memory: hostPercent(status.MemoryUsed, status.MemoryTotal),
			Disk:   hostPercent(status.DiskUsed, status.DiskTotal), PID: pid,
			BotCPU: round2(bot.CPUPercent), BotRSS: bot.RSS,
			RxRate: math.Round(live.network.RxRate), TxRate: math.Round(live.network.TxRate),
			TgMS: s.prober.best()})
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

// systemHistory выбирает временной диапазон и возвращает сжатую историю, перезапуски,
// события ленты, пороги уведомлений и статистику по полным данным диапазона.
func (s *server) systemHistory(w http.ResponseWriter, r *http.Request) {
	duration := time.Hour
	rangeName := r.URL.Query().Get("range")
	switch rangeName {
	case "live":
		duration = 5 * time.Minute
	case "24h":
		duration = 24 * time.Hour
	default:
		rangeName = "1h"
	}
	now := time.Now()
	cutoff := now.Add(-duration)
	points := []hostPoint{}
	restarts := []time.Time{}
	count := 0
	stats := map[string]seriesStats{}
	if s.hostHistory != nil {
		points, count = s.hostHistory.sampledSince(cutoff, historyGraphLimit)
		restarts = s.hostHistory.restartTimes(cutoff)
		stats = s.statsCache.get(rangeName, now, func() map[string]seriesStats {
			return historyStats(s.hostHistory.since(cutoff))
		})
	}
	thresholds := map[string]int{}
	if s.operations != nil {
		settings := s.operations.alertSettings()
		thresholds = map[string]int{"cpu": settings.CPUPercent, "memory": settings.MemoryPercent, "disk": settings.DiskPercent}
	}
	interval := 1.0
	if count > len(points) && len(points) > 0 {
		interval = math.Round(duration.Seconds()/float64(len(points))*10) / 10
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": duration.String(), "rangeSeconds": duration.Seconds(),
		"sampleCount": count, "intervalSeconds": interval, "points": points, "restarts": restarts,
		"events": s.timeline.since(cutoff), "thresholds": thresholds, "stats": stats, "now": now.UTC()})
}

// bucketHostPoints сжимает точки до limit интервалов равной длительности: значения усредняются,
// для CPU дополнительно сохраняется пик. PID берётся последним в интервале; сами перезапуски
// передаются отдельным списком, поэтому сжатие их не теряет.
func bucketHostPoints(points []hostPoint, limit int) []hostPoint {
	if len(points) <= limit || limit < 2 {
		return append([]hostPoint(nil), points...)
	}
	start, end := points[0].At, points[len(points)-1].At
	span := end.Sub(start)
	if span <= 0 {
		return append([]hostPoint(nil), points[len(points)-1])
	}
	result := make([]hostPoint, 0, limit)
	index := 0
	for bucket := 0; bucket < limit; bucket++ {
		bucketEnd := start.Add(span * time.Duration(bucket+1) / time.Duration(limit))
		var sum hostPoint
		n := 0
		for index < len(points) && (!points[index].At.After(bucketEnd) || bucket == limit-1) {
			point := points[index]
			sum.CPU += point.CPU
			sum.Memory += point.Memory
			sum.Disk += point.Disk
			sum.BotCPU += point.BotCPU
			sum.BotRSS += point.BotRSS
			sum.RxRate += point.RxRate
			sum.TxRate += point.TxRate
			sum.TgMS += point.TgMS
			sum.CPUMax = max(sum.CPUMax, point.CPU)
			sum.PID = point.PID
			sum.At = point.At
			n++
			index++
		}
		if n == 0 {
			continue
		}
		f := float64(n)
		result = append(result, hostPoint{At: sum.At, PID: sum.PID, CPUMax: round2(sum.CPUMax),
			CPU: round2(sum.CPU / f), Memory: round2(sum.Memory / f), Disk: round2(sum.Disk / f),
			BotCPU: round2(sum.BotCPU / f), BotRSS: sum.BotRSS / uint64(n),
			RxRate: math.Round(sum.RxRate / f), TxRate: math.Round(sum.TxRate / f), TgMS: round2(sum.TgMS / f)})
	}
	return result
}

func round2(value float64) float64 { return math.Round(value*100) / 100 }

// seriesStats — Минимум, среднее, максимум и 95-й перцентиль серии за диапазон.
type seriesStats struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
	P95 float64 `json:"p95"`
}

// historyStats считает статистику по всем секундным точкам диапазона, а не по сжатым.
// Нулевые значения бота и сети вне его работы не учитываются, чтобы не занижать среднее.
func historyStats(points []hostPoint) map[string]seriesStats {
	series := map[string]func(hostPoint) (float64, bool){
		"cpu":    func(p hostPoint) (float64, bool) { return p.CPU, true },
		"memory": func(p hostPoint) (float64, bool) { return p.Memory, true },
		"disk":   func(p hostPoint) (float64, bool) { return p.Disk, true },
		"botCpu": func(p hostPoint) (float64, bool) { return p.BotCPU, p.PID != 0 },
		"botRss": func(p hostPoint) (float64, bool) { return float64(p.BotRSS), p.BotRSS > 0 },
		"rx":     func(p hostPoint) (float64, bool) { return p.RxRate, true },
		"tx":     func(p hostPoint) (float64, bool) { return p.TxRate, true },
		"tgMs":   func(p hostPoint) (float64, bool) { return p.TgMS, p.TgMS > 0 },
	}
	result := make(map[string]seriesStats, len(series))
	values := make([]float64, 0, len(points))
	for key, pick := range series {
		values = values[:0]
		sum := 0.0
		for _, point := range points {
			if value, ok := pick(point); ok {
				values = append(values, value)
				sum += value
			}
		}
		if len(values) == 0 {
			continue
		}
		sort.Float64s(values)
		result[key] = seriesStats{Min: round2(values[0]), Max: round2(values[len(values)-1]),
			Avg: round2(sum / float64(len(values))), P95: round2(values[int(float64(len(values)-1)*0.95)])}
	}
	return result
}

// historyStatsCache пересчитывает статистику диапазона не чаще раза в 10 секунд:
// для суток это сортировка десятков тысяч значений на каждый опрос.
type historyStatsCache struct {
	mu      sync.Mutex
	entries map[string]statsEntry
}

type statsEntry struct {
	at    time.Time
	stats map[string]seriesStats
}

func (cache *historyStatsCache) get(key string, now time.Time, compute func() map[string]seriesStats) map[string]seriesStats {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry, ok := cache.entries[key]; ok && now.Sub(entry.at) < 10*time.Second {
		return entry.stats
	}
	stats := compute()
	if cache.entries == nil {
		cache.entries = map[string]statsEntry{}
	}
	cache.entries[key] = statsEntry{at: now, stats: stats}
	return stats
}
