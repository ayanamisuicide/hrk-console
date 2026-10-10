package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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

// terminalRequest — Команда и отдельное подтверждение её произвольного выполнения.
type terminalRequest struct {
	Command   string `json:"command"`
	Confirmed bool   `json:"confirmed"`
}

// terminalResponse — Ограниченный вывод команды, код завершения, таймаут и рабочий каталог.
type terminalResponse struct {
	Output     string `json:"output"`
	Actor      string `json:"actor"`
	ExitCode   int    `json:"exitCode"`
	TimedOut   bool   `json:"timedOut"`
	DurationMS int64  `json:"durationMs"`
	Directory  string `json:"directory"`
}

// cappedOutput — Ограниченный буфер общего вывода команды; продолжает принимать запись после заполнения.
type cappedOutput struct {
	buffer bytes.Buffer
	cut    bool
}

// Write сохраняет вывод до лимита, но сообщает принятую длину полностью, чтобы обрезание не ломало
// выполняемую команду.
func (c *cappedOutput) Write(p []byte) (int, error) {
	length := len(p)
	room := terminalMaxOutput - c.buffer.Len()
	if room > 0 {
		if room > length {
			room = length
		}
		_, _ = c.buffer.Write(p[:room])
	}
	if length > room {
		c.cut = true
	}
	return length, nil
}

// adminTerminal требует административный токен, Linux, явное включение и подтверждение. Выполняет оболочку
// с таймаутом и ограниченным выводом; аудит хранит отпечаток, а не текст команды.
func (s *server) adminTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	if runtime.GOOS != "linux" {
		writeJSON(w, http.StatusNotImplemented, actionResponse{Message: "консоль доступна только на Linux/WSL"})
		return
	}
	if strings.TrimSpace(os.Getenv("HKC_TERMINAL_ENABLED")) != "1" {
		writeJSON(w, http.StatusForbidden, actionResponse{Message: "административный терминал отключён; задайте HKC_TERMINAL_ENABLED=1 для явного включения"})
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
	if !request.Confirmed {
		writeJSON(w, http.StatusPreconditionRequired, actionResponse{Message: "подтвердите выполнение команды"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), terminalTimeout)
	defer cancel()
	started := time.Now()
	// Запускается произвольная оболочка с правами службы: ограничения времени
	// и вывода не являются песочницей и не ограничивают доступ команды к файлам.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", request.Command)
	configureCommandProcess(cmd)
	cmd.Dir = s.bot.HerokuDir
	var capture cappedOutput
	cmd.Stdout, cmd.Stderr = &capture, &capture
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		killCommandProcessGroup(cmd)
	}
	duration := time.Since(started)
	output := strings.TrimSpace(strings.ToValidUTF8(capture.buffer.String(), "�"))
	if capture.cut {
		output += "\n…вывод обрезан"
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
	digest := sha256.Sum256([]byte(request.Command))
	actor := "admin-token"
	if cookie, err := r.Cookie(sessionCookie); err == nil && s.sessions != nil {
		if username, ok := s.sessions.get(cookie.Value); ok && s.auth != nil && s.auth.role(username) != "" {
			actor = username
		}
	}
	s.record(r, actor, "terminal.execute", fmt.Sprintf("Команда SHA256 %.12x · exit %d · %d мс · таймаут %t", digest, exitCode, duration.Milliseconds(), timedOut))
	writeJSON(w, http.StatusOK, terminalResponse{
		Output: output, Actor: actor, ExitCode: exitCode, TimedOut: timedOut,
		DurationMS: duration.Milliseconds(), Directory: s.bot.HerokuDir,
	})
}
