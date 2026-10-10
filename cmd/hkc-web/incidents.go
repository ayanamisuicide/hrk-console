package main

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"heroku-console/logfeed"
)

var incidentLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[(ERROR|CRITICAL|WARNING)\] ([^:]+):\s*(.*)$`)

// incident — Группа близких событий одного модуля с временными границами и контекстом.
type incident struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Level    string `json:"level"`
	Module   string `json:"module"`
	Title    string `json:"title"`
	Count    int    `json:"count"`
	Context  string `json:"context"`
	Restarts int    `json:"restarts"`
}

// Группируем близкие ошибки по модулю; предупреждения учитываем только сериями.
func detectIncidents(lines []string) []incident {
	result := []incident{}
	for index, raw := range lines {
		match := incidentLine.FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		stamp, err := time.ParseInLocation("2006-01-02 15:04:05", match[1], time.Local)
		if err != nil {
			continue
		}
		module := strings.TrimSpace(match[3])
		level := match[2]
		warningStart := match[1]
		initialCount := 1
		if level == "WARNING" {
			warnings := 0
			for j := index; j >= 0; j-- {
				previous := incidentLine.FindStringSubmatch(lines[j])
				if previous == nil {
					continue
				}
				at, err := time.ParseInLocation("2006-01-02 15:04:05", previous[1], time.Local)
				if err == nil && stamp.Sub(at) > 5*time.Minute {
					break
				}
				if previous[2] == "WARNING" && strings.TrimSpace(previous[3]) == module {
					warnings++
					if warnings <= 3 {
						warningStart = previous[1]
					}
				}
			}
			if warnings < 3 {
				continue
			}
			if warnings == 3 {
				initialCount = 3
			}
		}
		if n := len(result); n > 0 {
			last := &result[n-1]
			end, _ := time.ParseInLocation("2006-01-02 15:04:05", last.End, time.Local)
			if last.Module == module && stamp.Sub(end) <= 5*time.Minute {
				last.End = match[1]
				last.Count++
				if level == "CRITICAL" {
					last.Level = level
				}
				continue
			}
		}
		context := ""
		if index > 0 {
			context = lines[index-1]
		}
		result = append(result, incident{Start: warningStart, End: match[1], Level: level,
			Module: module, Title: strings.TrimSpace(match[4]), Count: initialCount, Context: context})
	}
	if len(result) > 30 {
		result = result[len(result)-30:]
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// incidents анализирует ограниченный хвост журнала и возвращает найденные происшествия.
func (s *server) incidents(w http.ResponseWriter, _ *http.Request) {
	lines := logfeed.TailLines(s.bot.LogFile, 3000)
	items := detectIncidents(lines)
	if s.hostHistory != nil {
		restarts := s.hostHistory.restartTimes(time.Now().Add(-24 * time.Hour))
		for index := range items {
			start, err := time.ParseInLocation("2006-01-02 15:04:05", items[index].Start, time.Local)
			if err != nil {
				continue
			}
			end, err := time.ParseInLocation("2006-01-02 15:04:05", items[index].End, time.Local)
			if err != nil {
				continue
			}
			for _, at := range restarts {
				if at.Before(start.Add(-5*time.Minute)) || at.After(end.Add(5*time.Minute)) {
					continue
				}
				items[index].Restarts++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": items})
}
