package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	terminalTimeout   = 30 * time.Second
	terminalMaxInput  = 4096
	terminalMaxOutput = 64 * 1024
)

type terminalRequest struct {
	Command string `json:"command"`
}

type terminalResponse struct {
	Output     string `json:"output"`
	ExitCode   int    `json:"exitCode"`
	TimedOut   bool   `json:"timedOut"`
	DurationMS int64  `json:"durationMs"`
	Directory  string `json:"directory"`
}

func (s *server) adminTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	if runtime.GOOS != "linux" {
		writeJSON(w, http.StatusNotImplemented, actionResponse{Message: "консоль доступна только на Linux/WSL"})
		return
	}
	var request terminalRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, terminalMaxInput+1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректная команда"})
		return
	}
	request.Command = strings.TrimSpace(request.Command)
	if request.Command == "" {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "введите команду"})
		return
	}
	if len(request.Command) > terminalMaxInput {
		writeJSON(w, http.StatusRequestEntityTooLarge, actionResponse{Message: "команда слишком длинная"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), terminalTimeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", request.Command)
	cmd.Dir = s.bot.HerokuDir
	data, err := cmd.CombinedOutput()
	duration := time.Since(started)
	output := strings.TrimSpace(strings.ToValidUTF8(string(data), "�"))
	if len(output) > terminalMaxOutput {
		output = output[:terminalMaxOutput] + "\n…вывод обрезан"
	}
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
		if output == "" {
			output = err.Error()
		}
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut {
		output = strings.TrimSpace(output + "\nКоманда остановлена по таймауту 30 секунд.")
	}
	s.record(r, "admin", "terminal.execute", "Выполнена команда в каталоге Heroku")
	writeJSON(w, http.StatusOK, terminalResponse{
		Output: output, ExitCode: exitCode, TimedOut: timedOut,
		DurationMS: duration.Milliseconds(), Directory: s.bot.HerokuDir,
	})
}
