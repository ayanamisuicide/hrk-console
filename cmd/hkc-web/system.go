package main

import (
	"net/http"
	"time"
)

// systemStatus — Системный замер; Supported показывает доступность Linux-метрик, объёмы передаются в
// байтах.
type systemStatus struct {
	Supported       bool      `json:"supported"`
	OS              string    `json:"os"`
	Arch            string    `json:"arch"`
	Hostname        string    `json:"hostname"`
	Kernel          string    `json:"kernel"`
	CPUCores        int       `json:"cpuCores"`
	CPUPercent      float64   `json:"cpuPercent"`
	Load1           float64   `json:"load1"`
	Load5           float64   `json:"load5"`
	Load15          float64   `json:"load15"`
	MemoryTotal     uint64    `json:"memoryTotalBytes"`
	MemoryUsed      uint64    `json:"memoryUsedBytes"`
	MemoryAvailable uint64    `json:"memoryAvailableBytes"`
	DiskTotal       uint64    `json:"diskTotalBytes"`
	DiskUsed        uint64    `json:"diskUsedBytes"`
	DiskFree        uint64    `json:"diskFreeBytes"`
	UptimeSeconds   float64   `json:"uptimeSeconds"`
	SampledAt       time.Time `json:"sampledAt"`
}

// systemHealth выдаёт последний фоновый замер; если замеров ещё нет, читает состояние напрямую.
func (s *server) systemHealth(w http.ResponseWriter, _ *http.Request) {
	s.systemMu.RLock()
	status := s.latestSystem
	s.systemMu.RUnlock()
	if status.SampledAt.IsZero() {
		status = readSystemStatus(s.bot.HerokuDir)
	}
	writeJSON(w, http.StatusOK, status)
}
