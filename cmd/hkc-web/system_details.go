package main

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"heroku-console/botproc"
)

// procSample — Один процесс в снимке /proc. CPU — доля всей машины в процентах.
type procSample struct {
	PID        int     `json:"pid"`
	PPID       int     `json:"-"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpuPercent"`
	RSS        uint64  `json:"rssBytes"`
	Threads    int     `json:"threads"`
}

// botProcess — Ресурсы процесса бота вместе с дочерними процессами.
type botProcess struct {
	Running    bool    `json:"running"`
	PID        int     `json:"pid"`
	Uptime     string  `json:"uptime"`
	CPUPercent float64 `json:"cpuPercent"`
	RSS        uint64  `json:"rssBytes"`
	Threads    int     `json:"threads"`
	Children   int     `json:"children"`
	OpenFiles  int     `json:"openFiles"`
	// RSSTrendPerHour — наклон памяти за последний час того же процесса, байт в час.
	RSSTrendPerHour float64 `json:"rssTrendPerHour"`
	RSSTrendFit     float64 `json:"rssTrendFit"`
}

type wslInfo struct {
	Detected   bool   `json:"detected"`
	ConfigPath string `json:"configPath,omitempty"`
	Memory     string `json:"memory,omitempty"`
	Swap       string `json:"swap,omitempty"`
	Processors string `json:"processors,omitempty"`
}

// processTree суммирует ресурсы корневого процесса и всех его потомков.
func processTree(samples []procSample, root int) (botProcess, bool) {
	children := map[int][]int{}
	byPID := map[int]procSample{}
	for _, sample := range samples {
		byPID[sample.PID] = sample
		children[sample.PPID] = append(children[sample.PPID], sample.PID)
	}
	if _, ok := byPID[root]; !ok {
		return botProcess{}, false
	}
	result := botProcess{Running: true, PID: root}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		sample := byPID[pid]
		result.CPUPercent += sample.CPUPercent
		result.RSS += sample.RSS
		result.Threads += sample.Threads
		if pid != root {
			result.Children++
		}
		queue = append(queue, children[pid]...)
	}
	return result, true
}

// netCounter превращает накопленные счётчики интерфейсов в скорость между замерами.
type netCounter struct {
	at     time.Time
	rx, tx uint64
}

type netRates struct {
	RxRate  float64 `json:"rxRate"`
	TxRate  float64 `json:"txRate"`
	RxTotal uint64  `json:"rxTotal"`
	TxTotal uint64  `json:"txTotal"`
}

func (counter *netCounter) observe(now time.Time, rx, tx uint64) netRates {
	rates := netRates{RxTotal: rx, TxTotal: tx}
	if elapsed := now.Sub(counter.at).Seconds(); !counter.at.IsZero() && elapsed > 0 && rx >= counter.rx && tx >= counter.tx {
		rates.RxRate = float64(rx-counter.rx) / elapsed
		rates.TxRate = float64(tx-counter.tx) / elapsed
	}
	counter.at, counter.rx, counter.tx = now, rx, tx
	return rates
}

// liveSample — Последние значения сборщика, которые не помещаются в systemStatus.
type liveSample struct {
	bot       botProcess
	processes []procSample
	network   netRates
}

// telegramTargets — точки входа дата-центров MTProto и Bot API. Бот подключается к одному
// из DC, поэтому показываем все: недоступность всех сразу означает проблему сети.
var telegramTargets = []struct{ Name, Address string }{
	{"DC1 · Майами", "149.154.175.53:443"},
	{"DC2 · Амстердам", "149.154.167.51:443"},
	{"DC3 · Майами", "149.154.175.100:443"},
	{"DC4 · Амстердам", "149.154.167.91:443"},
	{"DC5 · Сингапур", "91.108.56.130:443"},
	{"Bot API", "api.telegram.org:443"},
}

type probeResult struct {
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	OK        bool      `json:"ok"`
	LatencyMS float64   `json:"latencyMs"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// networkProber раз в 30 секунд измеряет время TCP-соединения с Telegram.
// Соединение сразу закрывается: данные не передаются, учётные данные бота не нужны.
type networkProber struct {
	mu      sync.RWMutex
	results []probeResult
	dial    func(ctx context.Context, address string) error
}

func (prober *networkProber) snapshot() []probeResult {
	prober.mu.RLock()
	defer prober.mu.RUnlock()
	return append([]probeResult(nil), prober.results...)
}

// best возвращает наименьшую задержку среди доступных точек, или 0, если доступных нет.
func (prober *networkProber) best() float64 {
	best := 0.0
	for _, result := range prober.snapshot() {
		if result.OK && (best == 0 || result.LatencyMS < best) {
			best = result.LatencyMS
		}
	}
	return best
}

func (prober *networkProber) probe(ctx context.Context) {
	dial := prober.dial
	if dial == nil {
		dial = func(ctx context.Context, address string) error {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
			if err == nil {
				conn.Close()
			}
			return err
		}
	}
	results := make([]probeResult, len(telegramTargets))
	var wg sync.WaitGroup
	for index, target := range telegramTargets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attemptCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			started := time.Now()
			err := dial(attemptCtx, target.Address)
			result := probeResult{Name: target.Name, Address: target.Address, CheckedAt: time.Now().UTC()}
			if err == nil {
				result.OK = true
				result.LatencyMS = math.Round(float64(time.Since(started).Microseconds())/100) / 10
			} else if attemptCtx.Err() != nil {
				result.Error = "нет ответа за 4 с"
			} else {
				result.Error = "соединение отклонено или адрес недоступен"
			}
			results[index] = result
		}()
	}
	wg.Wait()
	prober.mu.Lock()
	prober.results = results
	prober.mu.Unlock()
}

func (prober *networkProber) run(ctx context.Context) {
	if os.Getenv("HKC_NETWORK_PROBE") == "0" {
		return
	}
	prober.probe(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prober.probe(ctx)
		}
	}
}

// diskEntry — Размер одного элемента верхнего уровня каталога.
type diskEntry struct {
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
	Dir   bool   `json:"dir"`
}

type diskUsage struct {
	Root      string      `json:"root"`
	Entries   []diskEntry `json:"entries"`
	Total     uint64      `json:"totalBytes"`
	Partial   bool        `json:"partial"`
	ScannedAt time.Time   `json:"scannedAt"`
}

const (
	diskScanFiles = 400000
	diskScanTime  = 15 * time.Second
	diskScanEvery = 5 * time.Minute
)

// scanDiskUsage считает размеры элементов верхнего уровня. Обход ограничен числом файлов
// и временем, символические ссылки не раскрываются; при достижении лимита итог неполный.
func scanDiskUsage(root string, extra map[string]string) diskUsage {
	usage := diskUsage{Root: root, ScannedAt: time.Now().UTC(), Entries: []diskEntry{}}
	deadline := time.Now().Add(diskScanTime)
	files := 0
	measure := func(path string) uint64 {
		var size uint64
		_ = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			files++
			if files > diskScanFiles || time.Now().After(deadline) {
				usage.Partial = true
				return fs.SkipAll
			}
			if entry.Type().IsRegular() {
				if info, err := entry.Info(); err == nil {
					size += uint64(info.Size())
				}
			}
			return nil
		})
		return size
	}
	if entries, err := os.ReadDir(root); err == nil {
		for _, entry := range entries {
			if entry.Type()&fs.ModeSymlink != 0 {
				continue
			}
			size := measure(filepath.Join(root, entry.Name()))
			usage.Entries = append(usage.Entries, diskEntry{Name: entry.Name(), Bytes: size, Dir: entry.IsDir()})
			usage.Total += size
		}
	}
	// Данные панели могут лежать вне каталога бота; показываем их отдельной строкой.
	for name, path := range extra {
		if path == "" || strings.HasPrefix(filepath.Clean(path)+string(filepath.Separator), filepath.Clean(root)+string(filepath.Separator)) {
			continue
		}
		size := measure(path)
		usage.Entries = append(usage.Entries, diskEntry{Name: name, Bytes: size, Dir: true})
	}
	sort.Slice(usage.Entries, func(i, j int) bool { return usage.Entries[i].Bytes > usage.Entries[j].Bytes })
	return usage
}

// diskScanner хранит последний результат и запускает новый обход не чаще раза в пять минут.
type diskScanner struct {
	mu      sync.Mutex
	result  diskUsage
	running bool
}

func (scanner *diskScanner) get(root string, extra map[string]string) diskUsage {
	scanner.mu.Lock()
	defer scanner.mu.Unlock()
	if scanner.result.Entries == nil {
		scanner.result.Entries = []diskEntry{}
	}
	if !scanner.running && time.Since(scanner.result.ScannedAt) > diskScanEvery {
		scanner.running = true
		go func() {
			usage := scanDiskUsage(root, extra)
			scanner.mu.Lock()
			scanner.result, scanner.running = usage, false
			scanner.mu.Unlock()
		}()
	}
	return scanner.result
}

// linearTrend возвращает наклон (единиц в секунду) и коэффициент детерминации R².
func linearTrend(xs, ys []float64) (slope, fit float64) {
	n := float64(len(xs))
	if len(xs) < 2 || len(xs) != len(ys) {
		return 0, 0
	}
	var sumX, sumY float64
	for index := range xs {
		sumX += xs[index]
		sumY += ys[index]
	}
	meanX, meanY := sumX/n, sumY/n
	var sxx, sxy, syy float64
	for index := range xs {
		dx, dy := xs[index]-meanX, ys[index]-meanY
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	if sxx == 0 {
		return 0, 0
	}
	slope = sxy / sxx
	if syy > 0 {
		fit = sxy * sxy / (sxx * syy)
	}
	return slope, fit
}

type diskForecast struct {
	GrowthPerDay float64 `json:"growthPerDay"`
	DaysToFull   float64 `json:"daysToFull"`
	BasisHours   float64 `json:"basisHours"`
	Fit          float64 `json:"fit"`
}

// forecastDisk оценивает рост занятого места по истории за сутки. Прогноз даётся, только
// если данных не меньше часа, рост устойчив и заметен (больше 1 МБ в сутки).
func forecastDisk(points []hostPoint, total, free uint64) diskForecast {
	if len(points) < 2 || total == 0 {
		return diskForecast{}
	}
	start := points[0].At
	xs := make([]float64, 0, len(points))
	ys := make([]float64, 0, len(points))
	step := max(1, len(points)/2000)
	for index := 0; index < len(points); index += step {
		xs = append(xs, points[index].At.Sub(start).Seconds())
		ys = append(ys, points[index].Disk/100*float64(total))
	}
	basis := points[len(points)-1].At.Sub(start).Hours()
	slope, fit := linearTrend(xs, ys)
	result := diskForecast{BasisHours: math.Round(basis*10) / 10}
	if basis < 1 {
		return result
	}
	result.GrowthPerDay, result.Fit = slope*86400, math.Round(fit*100)/100
	if result.GrowthPerDay > 1<<20 && fit >= 0.5 {
		result.DaysToFull = math.Round(float64(free)/result.GrowthPerDay*10) / 10
	}
	return result
}

// rssTrend считает наклон памяти бота за последний час, только для текущего PID.
func rssTrend(points []hostPoint, pid int) (perHour, fit float64) {
	xs, ys := []float64{}, []float64{}
	for _, point := range points {
		if point.PID == pid && point.BotRSS > 0 {
			xs = append(xs, float64(point.At.Unix()))
			ys = append(ys, float64(point.BotRSS))
		}
	}
	if len(xs) < 600 {
		return 0, 0
	}
	slope, fit := linearTrend(xs, ys)
	return slope * 3600, math.Round(fit*100) / 100
}

// eventTimeline хранит события за последние сутки для ленты под графиком.
// Записываются все события, даже если каналы уведомлений не настроены.
type eventTimeline struct {
	mu     sync.Mutex
	events []alertEvent
}

const timelineLimit = 1000

func (timeline *eventTimeline) add(event alertEvent) {
	if timeline == nil {
		return
	}
	timeline.mu.Lock()
	defer timeline.mu.Unlock()
	timeline.events = append(timeline.events, event)
	cutoff := time.Now().Add(-24 * time.Hour)
	start := 0
	for start < len(timeline.events) && (timeline.events[start].Time.Before(cutoff) || len(timeline.events)-start > timelineLimit) {
		start++
	}
	timeline.events = append([]alertEvent(nil), timeline.events[start:]...)
}

func (timeline *eventTimeline) since(cutoff time.Time) []alertEvent {
	result := []alertEvent{}
	if timeline == nil {
		return result
	}
	timeline.mu.Lock()
	defer timeline.mu.Unlock()
	for _, event := range timeline.events {
		if !event.Time.Before(cutoff) {
			result = append(result, event)
		}
	}
	return result
}

// summaryItem — Одна причина в сводке над карточками.
type summaryItem struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

type systemSummary struct {
	Level string        `json:"level"`
	Title string        `json:"title"`
	Items []summaryItem `json:"items"`
}

// buildSummary сводит состояние в одну фразу. Порядок важности: плохо, внимание, норма.
func buildSummary(status systemStatus, bot botProcess, watchdog watchdogStatus, probes []probeResult, forecast diskForecast, firing map[string]time.Duration) systemSummary {
	items := []summaryItem{}
	add := func(level, format string, args ...any) {
		items = append(items, summaryItem{Level: level, Text: fmt.Sprintf(format, args...)})
	}
	if !bot.Running {
		add("bad", "Бот остановлен")
	}
	if watchdog.State == "suspended" {
		add("bad", "Автовосстановление приостановлено после серии падений")
	}
	labels := map[string]string{"cpu": "CPU", "memory": "Память", "disk": "Диск"}
	for _, key := range []string{"cpu", "memory", "disk"} {
		if duration, ok := firing[key]; ok {
			add("bad", "%s выше порога уже %s", labels[key], formatAlertDuration(duration))
		}
	}
	if status.Supported {
		memory := hostPercent(status.MemoryUsed, status.MemoryTotal)
		disk := hostPercent(status.DiskUsed, status.DiskTotal)
		if _, ok := firing["memory"]; !ok && memory >= 85 {
			add("warn", "Память занята на %.0f%%", memory)
		}
		if _, ok := firing["disk"]; !ok && disk >= 85 {
			add("warn", "Диск занят на %.0f%%", disk)
		}
		if status.SwapTotal > 0 && hostPercent(status.SwapUsed, status.SwapTotal) >= 50 {
			add("warn", "Используется %.0f%% swap — системе не хватает памяти", hostPercent(status.SwapUsed, status.SwapTotal))
		}
	}
	if forecast.DaysToFull > 0 && forecast.DaysToFull < 7 {
		add("warn", "При текущем темпе диск заполнится через %s", formatDays(forecast.DaysToFull))
	}
	if len(probes) > 0 {
		reachable := 0
		for _, probe := range probes {
			if probe.OK {
				reachable++
			}
		}
		if reachable == 0 {
			add("bad", "Серверы Telegram недоступны")
		}
	}
	if bot.Running && bot.RSSTrendFit >= 0.8 && bot.RSS > 0 && bot.RSSTrendPerHour > float64(bot.RSS)*0.05 {
		add("warn", "Память бота растёт на %s в час — возможна утечка", formatBytesShort(bot.RSSTrendPerHour))
	}
	summary := systemSummary{Level: "ok", Title: "Всё в порядке", Items: items}
	for _, item := range items {
		if item.Level == "bad" {
			summary.Level = "bad"
			break
		}
		summary.Level = "warn"
	}
	switch summary.Level {
	case "bad":
		summary.Title = "Нужно вмешательство"
	case "warn":
		summary.Title = "Есть на что посмотреть"
	}
	return summary
}

func formatDays(days float64) string {
	if days < 1 {
		return fmt.Sprintf("%.0f ч", days*24)
	}
	return fmt.Sprintf("%.1f дн", days)
}

func formatBytesShort(value float64) string {
	units := []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

// firingThresholds сообщает, какие ресурсы сейчас выше порога и сколько.
func (m *hostAlertMonitor) firingThresholds(now time.Time) map[string]time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := map[string]time.Duration{}
	for key, state := range m.states {
		if state.firing {
			result[key] = now.Sub(state.aboveSince)
		}
	}
	return result
}

type systemDetailsResponse struct {
	Summary         systemSummary `json:"summary"`
	Bot             botProcess    `json:"bot"`
	Network         netRates      `json:"network"`
	Probes          []probeResult `json:"probes"`
	Disk            diskUsage     `json:"disk"`
	InodesTotal     uint64        `json:"inodesTotal"`
	InodesFree      uint64        `json:"inodesFree"`
	Forecast        diskForecast  `json:"forecast"`
	WSL             wslInfo       `json:"wsl"`
	Processes       []procSample  `json:"processes"`
	ProcessesHidden bool          `json:"processesHidden"`
}

// canSeeProcesses разрешает список процессов хоста только операторам.
func (s *server) canSeeProcesses(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	username, ok := s.sessions.get(cookie.Value)
	return ok && s.auth.role(username) == "operator"
}

// systemDetails собирает подробности для вкладки «Система». Список процессов хоста видят
// только операторы: в именах процессов бывают сведения о других службах машины.
func (s *server) systemDetails(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.systemMu.RLock()
	status, live := s.latestSystem, s.latestLive
	s.systemMu.RUnlock()
	if status.SampledAt.IsZero() {
		status = readSystemStatus(s.bot.HerokuDir)
	}
	bot := live.bot
	if pid := s.bot.PID(); pid != 0 {
		bot.Running, bot.PID = true, pid
		bot.Uptime = botproc.Uptime(pid)
		bot.OpenFiles = openFiles(pid)
	} else {
		bot = botProcess{}
	}
	var day []hostPoint
	if s.hostHistory != nil {
		day = s.hostHistory.since(now.Add(-24 * time.Hour))
		if bot.Running {
			hour := sort.Search(len(day), func(i int) bool { return !day[i].At.Before(now.Add(-time.Hour)) })
			bot.RSSTrendPerHour, bot.RSSTrendFit = rssTrend(day[hour:], bot.PID)
		}
	}
	response := systemDetailsResponse{Bot: bot, Network: live.network, Probes: s.prober.snapshot(),
		Processes: []procSample{}, WSL: s.wsl}
	response.Disk = s.diskScanner.get(s.bot.HerokuDir, map[string]string{"Данные панели": s.dataDir})
	// Файловые системы Windows в WSL (drvfs, 9p) отдают условные числа inode — их не показываем.
	if total, free := diskInodes(s.bot.HerokuDir); total >= 10000 && free <= total {
		response.InodesTotal, response.InodesFree = total, free
	}
	response.Forecast = forecastDisk(day, status.DiskTotal, status.DiskFree)
	if s.canSeeProcesses(r) {
		processes := append([]procSample(nil), live.processes...)
		sort.Slice(processes, func(i, j int) bool {
			if processes[i].CPUPercent != processes[j].CPUPercent {
				return processes[i].CPUPercent > processes[j].CPUPercent
			}
			return processes[i].RSS > processes[j].RSS
		})
		response.Processes = processes[:min(10, len(processes))]
	} else {
		response.ProcessesHidden = true
	}
	response.Summary = buildSummary(status, bot, s.watchdog.snapshot(), response.Probes, response.Forecast, s.hostAlerts.firingThresholds(now))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}
