package main

import (
	"net/http"
	"time"
)

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

func (s *server) systemHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, readSystemStatus(s.bot.HerokuDir))
}
