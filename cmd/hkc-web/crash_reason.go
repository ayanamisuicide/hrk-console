package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Уведомление «бот остановлен» объясняет, почему. Сначала — не остановил ли его кто-то сам:
// действие из панели, админки, Telegram или автовосстановления уже записано в аудит. Иначе бот
// упал, и причину ищем в его выводе (.startup.log): traceback Python, «Killed», фатальная ошибка.
// Если в выводе ничего нет, так и говорим и подсказываем, на что похоже.

const (
	// stopActionWindow — насколько давнее действие из аудита ещё объясняет остановку: наблюдатель
	// проверяет процесс раз в 15 секунд.
	stopActionWindow = 2 * time.Minute
	// stopOutputFresh — вывод, изменённый позже этого срока до остановки, относится к ней.
	stopOutputFresh = 3 * time.Minute
	// stopOutputTail — сколько байт конца вывода разбирать.
	stopOutputTail = 64 << 10
	// stopMemoryHint — с такой занятостью памяти беззвучное завершение похоже на нехватку памяти.
	stopMemoryHint = 90.0
)

// stopReport — почему бот перестал работать.
type stopReport struct {
	At       time.Time
	Title    string
	Severity string
	// Message — первая строка коротко называет причину (она же попадает в журнал панели и
	// в сообщение о восстановлении), следующие — подробности.
	Message string
	// Trace — traceback из вывода процесса; по нему кнопка «Traceback» в Telegram.
	Trace []string
}

var (
	traceStart = "Traceback (most recent call last):"
	traceFrame = regexp.MustCompile(`^\s*File "([^"]+)", line (\d+), in (.+)$`)
	fatalLine  = regexp.MustCompile(`(?i)^(killed|segmentation fault.*|fatal python error.*|memoryerror.*|.*out of memory.*)$`)
)

// explainStop собирает причину остановки и запоминает её для кнопки «Traceback».
func (s *server) explainStop(now time.Time) stopReport {
	report := s.manualStop(now)
	if report.Title == "" {
		report = s.crashFromOutput(now)
	}
	report.At = now
	report.Message = maskSecrets(report.Message)
	for i, line := range report.Trace {
		report.Trace[i] = maskSecrets(line)
	}
	s.stopMu.Lock()
	s.lastStop = report
	s.stopMu.Unlock()
	return report
}

// lastStopReport — последняя объяснённая остановка.
func (s *server) lastStopReport() stopReport {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	report := s.lastStop
	report.Trace = append([]string(nil), report.Trace...)
	return report
}

// manualStop ищет в аудите недавнюю остановку или перезапуск бота.
func (s *server) manualStop(now time.Time) stopReport {
	if s.audit == nil {
		return stopReport{}
	}
	events, err := s.audit.recent(50)
	if err != nil {
		return stopReport{}
	}
	for _, event := range events {
		if now.Sub(event.Time) > stopActionWindow {
			break
		}
		if event.Time.After(now.Add(time.Minute)) {
			continue
		}
		at := event.Time.In(time.Local).Format("15:04")
		who := describeActor(event)
		switch event.Action {
		case "bot.stop":
			return stopReport{Title: "Бот остановлен", Severity: "warning", Message: fmt.Sprintf("Остановил %s в %s", who, at)}
		case "bot.restart":
			return stopReport{Title: "Бот перезапускается", Severity: "warning", Message: fmt.Sprintf("Перезапуск: %s в %s", who, at)}
		case "bot.recover":
			message := "Автовосстановление перезапускает бота в " + at
			if detail := strings.TrimSpace(event.Detail); detail != "" {
				message += "\n" + detail
			}
			return stopReport{Title: "Бот перезапускается", Severity: "warning", Message: message}
		}
	}
	return stopReport{}
}

// describeActor — кто и откуда: «@anya из Telegram», «администратор из админки», «admin из панели».
func describeActor(event auditEvent) string {
	actor := strings.TrimSpace(event.Actor)
	switch {
	case actor == "watchdog":
		return "автовосстановление"
	case actor == "admin":
		return "администратор из админки"
	case strings.HasPrefix(actor, "telegram:"):
		name := strings.TrimPrefix(actor, "telegram:")
		if event.IP == "telegram" {
			return name + " из Telegram"
		}
		return name + " из мини-приложения Telegram"
	case actor == "" || actor == "user":
		return "пользователь панели"
	}
	return actor + " из панели"
}

// crashFromOutput ищет причину падения в конце вывода процесса.
func (s *server) crashFromOutput(now time.Time) stopReport {
	report := stopReport{Title: "Бот упал", Severity: "critical"}
	path := s.bot.StartupLog
	info, err := os.Stat(path)
	if err != nil {
		report.Message = "Причину панель не видит: бот запущен не из неё, и его вывод ей недоступен."
		return report
	}
	if now.Sub(info.ModTime()) <= stopOutputFresh {
		start := info.Size() - stopOutputTail
		if start < 0 {
			start = 0
		}
		lines := strings.Split(strings.ReplaceAll(string(readRange(path, start, stopOutputTail)), "\r", ""), "\n")
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // первая строка обрезана посередине
		}
		if summary, details, trace := crashSummary(lines); summary != "" {
			report.Message = summary
			if details != "" {
				report.Message += "\n" + details
			}
			report.Trace = trace
			return report
		}
	}
	report.Message = "Процесс завершился, не написав причину."
	s.systemMu.RLock()
	system := s.latestSystem
	s.systemMu.RUnlock()
	if used := hostPercent(system.MemoryUsed, system.MemoryTotal); system.Supported && used >= stopMemoryHint {
		report.Message += fmt.Sprintf("\nПамять сервера занята на %.0f%% — похоже на нехватку памяти.", used)
	} else {
		report.Message += "\nТак бывает при нехватке памяти или принудительном kill -9."
	}
	return report
}

// crashSummary разбирает конец вывода: последний traceback (строка исключения и место в коде) или
// строку фатальной ошибки. Пустой summary — причины в выводе нет.
func crashSummary(lines []string) (summary, details string, trace []string) {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], traceStart) {
			start = i
			break
		}
	}
	if start >= 0 {
		trace = lines[start:]
		if len(trace) > 200 {
			trace = append(trace[:100:100], trace[len(trace)-100:]...)
		}
		for i := len(trace) - 1; i > 0; i-- {
			if line := strings.TrimSpace(trace[i]); processErrorLine.MatchString(line) {
				summary = line
				break
			}
		}
		if summary == "" {
			summary = strings.TrimSpace(trace[len(trace)-1])
		}
		for i := len(trace) - 1; i >= 0; i-- {
			if m := traceFrame.FindStringSubmatch(trace[i]); m != nil {
				details = describeFrame(m[1], m[2], m[3])
				break
			}
		}
		return truncateRunes(summary, 300), details, trace
	}
	from := max(0, len(lines)-30)
	for i := len(lines) - 1; i >= from; i-- {
		line := strings.TrimSpace(lines[i])
		if !fatalLine.MatchString(line) {
			continue
		}
		if strings.EqualFold(line, "killed") {
			return "Killed — процесс убит системой, обычно из-за нехватки памяти", "", nil
		}
		return truncateRunes(line, 300), "", nil
	}
	return "", "", nil
}

// describeFrame — «в модуле weather · weather.py:12, в load» по последнему кадру traceback.
func describeFrame(path, line, function string) string {
	file := path
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		file = path[slash+1:]
	}
	where := fmt.Sprintf("%s:%s, в %s", file, line, strings.TrimSpace(function))
	if strings.Contains(path, "/modules/") || strings.Contains(path, "loaded_modules") {
		return "в модуле " + strings.TrimSuffix(file, ".py") + " · " + where
	}
	return where
}
