package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// exitProcess завершает панель; systemd поднимает её снова по Restart=always. Тесты подменяют.
var exitProcess = os.Exit

// underSystemd сообщает, запущена ли панель службой: без супервизора выход означал бы остановку.
var underSystemd = func() bool { return os.Getenv("INVOCATION_ID") != "" }

// serviceKillMode читает KillMode службы панели. При KillMode=process бот, дочерний процесс
// панели, переживает её перезапуск; у старых установок юнит ещё с control-group.
var serviceKillMode = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "hkc-web.service", "--property=KillMode", "--value").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// panelInfo отдаёт время запуска процесса (по его смене страница понимает, что панель
// перезапустилась) и то, переживёт ли бот перезапуск.
func (s *server) panelInfo(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"startedAt": processStarted.UTC(), "pid": os.Getpid(),
		"restartable": underSystemd(), "keepsBot": underSystemd() && serviceKillMode() == "process"})
}

// restartPanel завершает процесс панели после ответа клиенту.
func (s *server) restartPanel(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}
	if !underSystemd() {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "панель запущена не службой systemd — перезапустите её вручную"})
		return
	}
	if jobRunning(readUpdateJob()) {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "идёт обновление — панель перезапустится сама"})
		return
	}
	s.record(r, "admin", "panel.restart", "Панель перезапущена")
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "панель перезапускается", "startedAt": processStarted.UTC()})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		// Пауза даёт ответу уйти клиенту до закрытия соединений.
		time.Sleep(500 * time.Millisecond)
		log.Print("перезапуск панели по запросу администратора")
		exitProcess(0)
	}()
}
