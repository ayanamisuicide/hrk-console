package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"heroku-console/botproc"
)

const hostHistoryLimit = 24 * 60 * 2 // 30-second samples for 24 hours

type hostPoint struct {
	At     time.Time `json:"at"`
	CPU    float64   `json:"cpu"`
	Memory float64   `json:"memory"`
	Disk   float64   `json:"disk"`
	PID    int       `json:"pid"`
}

type hostHistoryStore struct {
	mu     sync.RWMutex
	path   string
	points []hostPoint
}

func newHostHistoryStore(path string) *hostHistoryStore {
	store := &hostHistoryStore{path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &store.points)
		store.trim(time.Now())
	}
	return store
}

func (h *hostHistoryStore) trim(now time.Time) {
	cutoff := now.Add(-24 * time.Hour)
	kept := h.points[:0]
	for _, point := range h.points {
		if !point.At.Before(cutoff) && !point.At.After(now.Add(time.Minute)) {
			kept = append(kept, point)
		}
	}
	h.points = kept
	if len(h.points) > hostHistoryLimit {
		h.points = h.points[len(h.points)-hostHistoryLimit:]
	}
}

func (h *hostHistoryStore) add(point hostPoint) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.points = append(h.points, point)
	h.trim(point.At)
	data, err := json.Marshal(h.points)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(h.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), "host-history-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
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
		// Windows cannot atomically rename over an existing destination.
		return os.WriteFile(h.path, data, 0600)
	}
	return err
}

func (h *hostHistoryStore) since(cutoff time.Time) []hostPoint {
	h.mu.RLock()
	defer h.mu.RUnlock()
	points := make([]hostPoint, 0, len(h.points))
	for _, point := range h.points {
		if !point.At.Before(cutoff) {
			points = append(points, point)
		}
	}
	return points
}

func hostPercent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func (s *server) collectHostHistory(ctx context.Context) {
	collect := func() {
		status := readSystemStatus(s.bot.HerokuDir)
		if !status.Supported {
			return
		}
		_ = s.hostHistory.add(hostPoint{At: time.Now(), CPU: status.CPUPercent,
			Memory: hostPercent(status.MemoryUsed, status.MemoryTotal),
			Disk:   hostPercent(status.DiskUsed, status.DiskTotal), PID: botproc.PID()})
	}
	collect()
	ticker := time.NewTicker(30 * time.Second)
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

func (s *server) systemHistory(w http.ResponseWriter, r *http.Request) {
	duration := time.Hour
	if r.URL.Query().Get("range") == "24h" {
		duration = 24 * time.Hour
	}
	points := []hostPoint{}
	if s.hostHistory != nil {
		points = s.hostHistory.since(time.Now().Add(-duration))
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": duration.String(), "points": points})
}
