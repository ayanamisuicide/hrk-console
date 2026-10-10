package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type moduleEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

type modulesSnapshot struct {
	Schema    int           `json:"schema"`
	PID       int           `json:"pid"`
	Session   string        `json:"session"`
	SampledAt float64       `json:"sampledAt"`
	LoopAt    float64       `json:"loopAt"`
	Modules   []moduleEntry `json:"modules"`
	Status    string        `json:"status"`
	Message   string        `json:"message"`
}

// Снимок принадлежит конкретному процессу: старые зелёные статусы после рестарта
// не считаются актуальными. Чтение ограничено, чтобы повреждённый файл не съел память.
func readModulesSnapshot(root string, pid int, now time.Time) modulesSnapshot {
	result := modulesSnapshot{Modules: []moduleEntry{}, Status: "unavailable", Message: "Мониторинг ещё не подключён. Данные появятся после запуска Heroku из обновлённой панели."}
	if pid == 0 {
		result.Status = "stopped"
		result.Message = "Бот остановлен. Состояние модулей недоступно."
		return result
	}
	f, err := os.Open(filepath.Join(root, ".hkc-modules.json"))
	if err != nil {
		return result
	}
	defer f.Close()
	if err := json.NewDecoder(io.LimitReader(f, 2<<20)).Decode(&result); err != nil || result.Schema != 1 || len(result.Modules) > 5000 {
		return modulesSnapshot{Modules: []moduleEntry{}, Status: "unavailable", Message: "Не удалось прочитать состояние модулей."}
	}
	if result.Modules == nil {
		result.Modules = []moduleEntry{}
	}
	if result.PID != pid {
		result.Modules = []moduleEntry{}
		result.Status = "waiting"
		result.Message = "Новый запуск бота. Ожидаем первый снимок модулей."
	} else if age := float64(now.UnixMilli())/1000 - result.SampledAt; age > 5 || age < -2 {
		result.Status = "stale"
		result.Message = "Связь с мониторингом потеряна. Показан последний снимок."
	} else if age := float64(now.UnixMilli())/1000 - result.LoopAt; result.LoopAt > 0 && (age > 5 || age < -2) {
		result.Status = "stale"
		result.Message = "Основной цикл Python не отвечает. Показано последнее состояние модулей."
	} else {
		result.Status = "live"
		result.Message = "Состояние загрузки обновляется каждую секунду."
	}
	return result
}

func (s *server) modulesStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, readModulesSnapshot(s.bot.HerokuDir, s.bot.PID(), time.Now()))
}
