package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"heroku-console/botproc"
)

func (s *server) adminDiagnosticCommand(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	command := r.PathValue("command")
	var output string
	switch command {
	case "process":
		pid := s.bot.PID()
		output = fmt.Sprintf("Система: %s/%s\nHeroku: %s\nPID: %d\nВремя работы: %s\nПамять: %.1f МБ", runtime.GOOS, runtime.GOARCH, map[bool]string{true: "работает", false: "остановлен"}[pid != 0], pid, botproc.Uptime(pid), float64(processRSS(pid))/1048576)
	case "startup-log":
		output = tailText(s.bot.StartupLog, 60)
		if output == "" {
			output = "Стартовый лог пока пуст."
		}
	case "git-status", "python-version":
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var cmd *exec.Cmd
		if command == "git-status" {
			cmd = exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "status", "--short", "--branch")
		} else {
			venv := s.bot.VirtualEnv()
			if venv == "" {
				writeJSON(w, http.StatusConflict, actionResponse{Message: "виртуальное окружение не найдено"})
				return
			}
			cmd = exec.CommandContext(ctx, venv+string(os.PathSeparator)+"bin"+string(os.PathSeparator)+"python3", "--version")
		}
		cmd.Dir = s.bot.HerokuDir
		data, err := cmd.CombinedOutput()
		output = strings.TrimSpace(string(data))
		if err != nil {
			writeJSON(w, http.StatusConflict, actionResponse{Message: fmt.Sprintf("команда завершилась с ошибкой: %v\n%s", err, output)})
			return
		}
	default:
		writeJSON(w, http.StatusNotFound, actionResponse{Message: "неизвестная команда"})
		return
	}
	if len(output) > 16*1024 {
		output = output[:16*1024] + "\n…вывод обрезан"
	}
	s.record(r, "admin", "diagnostic."+command, "Запущена диагностика")
	writeJSON(w, http.StatusOK, map[string]string{"output": output})
}
