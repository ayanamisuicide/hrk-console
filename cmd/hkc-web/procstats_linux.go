//go:build linux

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// procScanner обходит /proc и считает CPU каждого процесса по разнице тиков между обходами.
// Проценты выражены в доле всей машины, как и общий CPU хоста, поэтому их можно сравнивать.
type procScanner struct {
	mu        sync.Mutex
	prevTicks map[int]uint64
	prevTotal uint64
}

var procPageSize = uint64(os.Getpagesize())

// scan возвращает снимок процессов. Первый обход даёт нулевой CPU: не с чем сравнить.
func (scanner *procScanner) scan() []procSample {
	total := totalJiffies()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	samples := make([]procSample, 0, len(entries))
	ticks := make(map[int]uint64, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		sample, used, ok := readProcStat(pid)
		if !ok {
			continue
		}
		ticks[pid] = used
		samples = append(samples, sample)
	}
	scanner.mu.Lock()
	defer scanner.mu.Unlock()
	delta := total - scanner.prevTotal
	if scanner.prevTotal != 0 && total > scanner.prevTotal {
		for index := range samples {
			previous, seen := scanner.prevTicks[samples[index].PID]
			current := ticks[samples[index].PID]
			if seen && current >= previous {
				samples[index].CPUPercent = float64(current-previous) * 100 / float64(delta)
			}
		}
	}
	scanner.prevTicks, scanner.prevTotal = ticks, total
	return samples
}

// readProcStat разбирает /proc/PID/stat. Имя процесса в скобках может содержать пробелы,
// поэтому поля отсчитываются от последней закрывающей скобки.
func readProcStat(pid int) (procSample, uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procSample{}, 0, false
	}
	text := string(data)
	open, closing := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || closing < open {
		return procSample{}, 0, false
	}
	fields := strings.Fields(text[closing+1:])
	// После имени: state(0) ppid(1) … utime(11) stime(12) … num_threads(17) … rss(21).
	if len(fields) < 22 {
		return procSample{}, 0, false
	}
	parse := func(index int) uint64 {
		value, _ := strconv.ParseUint(fields[index], 10, 64)
		return value
	}
	ppid, _ := strconv.Atoi(fields[1])
	threads, _ := strconv.Atoi(fields[17])
	return procSample{PID: pid, PPID: ppid, Name: text[open+1 : closing], Threads: threads,
		RSS: parse(21) * procPageSize}, parse(11) + parse(12), true
}

// totalJiffies суммирует все счётчики строки cpu из /proc/stat.
func totalJiffies() uint64 {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer file.Close()
	line, _ := bufio.NewReader(file).ReadString('\n')
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "cpu" {
		return 0
	}
	var total uint64
	for _, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		total += value
	}
	return total
}

// openFiles считает дескрипторы процесса; чужой процесс без прав даёт -1.
func openFiles(pid int) int {
	entries, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// readNetDev суммирует принятые и отправленные байты всех интерфейсов, кроме loopback.
func readNetDev() (rx, tx uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		received, _ := strconv.ParseUint(fields[0], 10, 64)
		sent, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += received
		tx += sent
	}
	return rx, tx
}

// diskInodes возвращает число индексных дескрипторов файловой системы и свободных из них.
func diskInodes(path string) (total, free uint64) {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return 0, 0
	}
	return stat.Files, stat.Ffree
}

// detectWSL распознаёт ядро WSL и ищет .wslconfig в профилях Windows, смонтированных в /mnt/c.
// Лимиты из файла применяются ко всей виртуальной машине WSL, а не к отдельному дистрибутиву.
func detectWSL() wslInfo {
	release, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	lower := strings.ToLower(string(release))
	if !strings.Contains(lower, "microsoft") && !strings.Contains(lower, "wsl") {
		return wslInfo{}
	}
	info := wslInfo{Detected: true}
	matches, _ := filepath.Glob("/mnt/c/Users/*/.wslconfig")
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 64*1024 {
			continue
		}
		info.ConfigPath = path
		section := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = strings.ToLower(strings.Trim(line, "[] "))
				continue
			}
			key, value, found := strings.Cut(line, "=")
			if !found || section != "wsl2" {
				continue
			}
			value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "memory":
				info.Memory = value
			case "swap":
				info.Swap = value
			case "processors":
				info.Processors = value
			}
		}
		break
	}
	return info
}
