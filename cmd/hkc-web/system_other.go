//go:build !linux

package main

import (
	"os"
	"runtime"
	"time"
)

// readSystemStatus возвращает базовую платформу с Supported=false: Linux-метрики на этой системе не
// собираются.
func readSystemStatus(_ string) systemStatus {
	hostname, _ := os.Hostname()
	return systemStatus{
		Supported: false, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Hostname: hostname, CPUCores: runtime.NumCPU(), SampledAt: time.Now().UTC(),
	}
}
