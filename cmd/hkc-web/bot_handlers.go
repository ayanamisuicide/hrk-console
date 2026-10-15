package main

import (
	"heroku-console/botproc"
	"net/http"
	"os"
)

// status возвращает состояние именно управляемого бота и последние строки его стартового журнала.
func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	pid := s.bot.PID()
	_, logErr := os.Stat(s.bot.LogFile)
	status := statusResponse{
		Running:    pid != 0,
		PID:        pid,
		Uptime:     botproc.Uptime(pid),
		Version:    s.bot.Version(),
		HerokuDir:  s.bot.HerokuDir,
		LogReady:   logErr == nil,
		StartupLog: tailText(s.bot.StartupLog, 12),
	}
	if pid == 0 {
		status.LastStop = s.currentStop()
	}
	writeJSON(w, http.StatusOK, status)
}

// action выполняет действие пользователя или API-клиента, уведомляет о состоянии и записывает автора в
// аудит.
func (s *server) action(w http.ResponseWriter, r *http.Request) {
	result, status := s.performAction(r.PathValue("action"))
	if status == http.StatusOK {
		actor := "user"
		if apiActor, ok := r.Context().Value(apiActorKey{}).(string); ok {
			actor = apiActor
		}
		if p, ok := requestPrincipal(r); ok {
			actor = p.name
		}
		s.record(r, actor, "bot."+r.PathValue("action"), result.Message)
		// Аудит пишется до проверки процесса: по нему уведомление объясняет, кто остановил бота.
		s.notifier.observe(s.bot.PID() != 0)
	}
	writeJSON(w, status, result)
}

// Все действия сериализованы с автоматическим восстановлением.
func (s *server) performAction(action string) (actionResponse, int) {
	s.botActionMu.Lock()
	defer s.botActionMu.Unlock()
	return s.performActionLocked(action)
}

// performActionLocked вызывается с захваченным botActionMu.
func (s *server) performActionLocked(action string) (actionResponse, int) {
	result := actionResponse{OK: true}
	switch action {
	case "start":
		started := s.bot.Start()
		if started.Err != nil {
			result.OK = false
			result.Message = started.Err.Error()
		} else if started.AlreadyStarting {
			result.Message = "запуск уже выполняется"
		} else {
			result.PID = started.PID
			result.Message = "бот запущен"
		}
	case "stop":
		code := s.bot.Stop()
		result.OK = code != 2
		switch code {
		case 1:
			result.Message = "бот уже остановлен"
		case 2:
			result.Message = "бот остановлен принудительно"
		default:
			result.Message = "бот остановлен"
		}
	case "restart":
		s.bot.Stop()
		started := s.bot.Start()
		if started.Err != nil {
			result.OK = false
			result.Message = started.Err.Error()
		} else {
			result.PID = started.PID
			result.Message = "бот перезапущен"
		}
	default:
		return actionResponse{Message: "неизвестное действие"}, http.StatusNotFound
	}
	status := http.StatusOK
	if !result.OK {
		status = http.StatusConflict
	}
	return result, status
}
