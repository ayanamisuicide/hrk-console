//go:build linux

package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var systemCPUState struct {
	sync.Mutex
	total uint64
	idle  uint64
}

func readSystemStatus(diskPath string) systemStatus {
	status := systemStatus{Supported: true, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(), SampledAt: time.Now().UTC()}
	status.Hostname, _ = os.Hostname()
	status.Kernel = kernelRelease()
	status.CPUPercent = cpuPercent()
	status.Load1, status.Load5, status.Load15 = loadAverage()
	status.MemoryTotal, status.MemoryAvailable = memoryInfo()
	status.MemoryUsed = status.MemoryTotal - status.MemoryAvailable
	status.UptimeSeconds = uptimeSeconds()
	status.DiskTotal, status.DiskFree = diskInfo(diskPath)
	status.DiskUsed = status.DiskTotal - status.DiskFree
	return status
}

func cpuPercent() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	fields := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var total uint64
	for _, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		total += value
	}
	idle, _ := strconv.ParseUint(fields[4], 10, 64)
	if len(fields) > 5 {
		iowait, _ := strconv.ParseUint(fields[5], 10, 64)
		idle += iowait
	}
	systemCPUState.Lock()
	defer systemCPUState.Unlock()
	previousTotal, previousIdle := systemCPUState.total, systemCPUState.idle
	systemCPUState.total, systemCPUState.idle = total, idle
	if previousTotal == 0 || total <= previousTotal {
		return 0
	}
	deltaTotal, deltaIdle := total-previousTotal, idle-previousIdle
	return float64(deltaTotal-deltaIdle) * 100 / float64(deltaTotal)
}

func memoryInfo() (total, available uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	return total, available
}

func loadAverage() (one, five, fifteen float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		one, _ = strconv.ParseFloat(fields[0], 64)
		five, _ = strconv.ParseFloat(fields[1], 64)
		fifteen, _ = strconv.ParseFloat(fields[2], 64)
	}
	return
}

func uptimeSeconds() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return value
}

func diskInfo(path string) (total, free uint64) {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return 0, 0
	}
	return stat.Blocks * uint64(stat.Bsize), stat.Bavail * uint64(stat.Bsize)
}

func kernelRelease() string {
	var info unix.Utsname
	if unix.Uname(&info) != nil {
		return ""
	}
	bytes := make([]byte, 0, len(info.Release))
	for _, value := range info.Release {
		if value == 0 {
			break
		}
		bytes = append(bytes, byte(value))
	}
	return string(bytes)
}
