package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"heroku-console/logfeed"
)

// Уровни журнала модулей меняются без перезапуска бота: панель пишет файл .hkc-log-levels.json в
// каталог бота, мост наблюдения внутри процесса раз в секунду ставит логгерам эти уровни и снимает
// истёкшие (см. Monitor.apply_levels). Так можно включить DEBUG у одного модуля на 15 минут или
// приглушить шумный логгер до предупреждений.

const logLevelsFile = ".hkc-log-levels.json"

var (
	loggerName     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]{0,199}$`)
	logLevelValues = map[string]string{"DEBUG": "подробно", "INFO": "обычно", "WARNING": "только предупреждения", "ERROR": "только ошибки"}
	logLevelsMu    sync.Mutex
)

type logLevelSetting struct {
	Level string  `json:"level"`
	Until float64 `json:"until"` // Unix-время окончания в секундах; 0 — до отмены
}

type logLevelsFileData struct {
	Levels map[string]logLevelSetting `json:"levels"`
}

func (s *server) logLevelsPath() string { return filepath.Join(s.bot.HerokuDir, logLevelsFile) }

// readLogLevels читает файл управления; истёкшие уровни не возвращаются.
func (s *server) readLogLevels(now time.Time) map[string]logLevelSetting {
	result := map[string]logLevelSetting{}
	data, err := os.ReadFile(s.logLevelsPath())
	if err != nil {
		return result
	}
	var parsed logLevelsFileData
	if json.Unmarshal(data, &parsed) != nil {
		return result
	}
	for name, item := range parsed.Levels {
		if item.Until == 0 || item.Until > float64(now.Unix()) {
			result[name] = item
		}
	}
	return result
}

func (s *server) writeLogLevels(levels map[string]logLevelSetting) error {
	data, err := json.MarshalIndent(logLevelsFileData{Levels: levels}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.logLevelsPath() + ".tmp"
	// Файл читает процесс бота, который может работать от другого пользователя; секретов в нём нет.
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.logLevelsPath())
}

type logLevelView struct {
	Logger  string  `json:"logger"`
	Level   string  `json:"level"`
	Until   float64 `json:"until"`
	Applied bool    `json:"applied"` // мост в процессе бота подтвердил уровень
}

// listLogLevels отдаёт заданные уровни и то, может ли мост их применить: он должен быть запущен
// в процессе бота и уметь менять уровни (бот запущен панелью версии 2.21 или новее).
func (s *server) listLogLevels(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	levels := s.readLogLevels(now)
	snapshot := readModulesSnapshot(s.bot.HerokuDir, s.bot.PID(), now)
	available, reason := true, ""
	switch {
	case s.bot.PID() == 0:
		available, reason = false, "Бот остановлен: уровни применятся, когда он запустится."
	case snapshot.Status == "unavailable" || !containsString(snapshot.Features, "levels"):
		available, reason = false, "Мост в процессе бота устарел: перезапустите бота из панели, чтобы менять уровни на лету."
	}
	views := make([]logLevelView, 0, len(levels))
	for name, item := range levels {
		applied, ok := snapshot.LogLevels[name]
		views = append(views, logLevelView{Logger: name, Level: item.Level, Until: item.Until, Applied: ok && applied.Level == item.Level})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Logger < views[j].Logger })
	writeJSON(w, http.StatusOK, map[string]any{"available": available, "reason": reason, "levels": views})
}

type logLevelInput struct {
	Logger  string `json:"logger"`
	Level   string `json:"level"`   // пусто — вернуть уровень по умолчанию
	Minutes int    `json:"minutes"` // 0 — до отмены
}

// setLogLevel задаёт или снимает уровень логгера.
func (s *server) setLogLevel(w http.ResponseWriter, r *http.Request) {
	var input logLevelInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	input.Level = strings.ToUpper(strings.TrimSpace(input.Level))
	if !loggerName.MatchString(input.Logger) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректное имя модуля"})
		return
	}
	if _, ok := logLevelValues[input.Level]; input.Level != "" && !ok {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "уровень: DEBUG, INFO, WARNING или ERROR"})
		return
	}
	if input.Minutes < 0 || input.Minutes > 7*24*60 {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "срок — от 0 (до отмены) до недели"})
		return
	}
	now := time.Now()
	logLevelsMu.Lock()
	levels := s.readLogLevels(now)
	detail := input.Logger + " → по умолчанию"
	if input.Level == "" {
		delete(levels, input.Logger)
	} else {
		setting := logLevelSetting{Level: input.Level}
		detail = fmt.Sprintf("%s → %s (%s)", input.Logger, input.Level, logLevelValues[input.Level])
		if input.Minutes > 0 {
			setting.Until = float64(now.Add(time.Duration(input.Minutes) * time.Minute).Unix())
			detail += fmt.Sprintf(" на %d мин", input.Minutes)
		}
		if len(levels) >= 100 {
			logLevelsMu.Unlock()
			writeJSON(w, http.StatusBadRequest, actionResponse{Message: "переопределений не больше 100"})
			return
		}
		levels[input.Logger] = setting
	}
	err := s.writeLogLevels(levels)
	logLevelsMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось записать уровни: " + err.Error()})
		return
	}
	s.record(r, s.requestActor(r), "log.level", detail)
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: detail})
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// logArchive отдаёт сводку архива журнала: сколько копий, сколько места и с какого времени
// хранится история. Лента показывает это рядом со счётчиком записей.
func (s *server) logArchive(w http.ResponseWriter, _ *http.Request) {
	dir := s.logArchiveDir()
	limits := s.archiveLimits()
	stats := logfeed.Source{Path: s.bot.LogFile, Archive: dir}.Stats()
	writeJSON(w, http.StatusOK, map[string]any{"enabled": dir != "", "files": stats.Files, "bytes": stats.Bytes,
		"firstTs": stats.FirstTS, "limitBytes": limits.bytes, "limitDays": int(limits.age.Hours() / 24)})
}
