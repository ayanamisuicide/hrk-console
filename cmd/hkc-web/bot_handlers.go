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
	writeJSON(w, http.StatusOK, statusResponse{
		Running:    pid != 0,
		PID:        pid,
		Uptime:     botproc.Uptime(pid),
		Version:    s.bot.Version(),
		HerokuDir:  s.bot.HerokuDir,
		LogReady:   logErr == nil,
		StartupLog: tailText(s.bot.StartupLog, 12),
	})
}

// action выполняет действие пользователя или API-клиента, уведомляет о состоянии и записывает автора в
// аудит.
func (s *server) action(w http.ResponseWriter, r *http.Request) {
	result, status := s.performAction(r.PathValue("action"))
	if status == http.StatusOK {
		s.notifier.observe(s.bot.PID() != 0)
	}
	if status == http.StatusOK {
		actor := "user"
		if apiActor, ok := r.Context().Value(apiActorKey{}).(string); ok {
			actor = apiActor
		}
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if username, ok := s.sessions.get(cookie.Value); ok {
				actor = username
			}
		}
		s.record(r, actor, "bot."+r.PathValue("action"), result.Message)
	}
	writeJSON(w, status, result)
}

// performAction запрещает запуск и перезапуск во время обслуживания; остановка остаётся доступной.
func (s *server) performAction(action string) (actionResponse, int) {
	if s.operations != nil && action != "stop" {
		maintenance := s.operations.maintenanceState()
		if maintenance.Enabled {
			message := "режим обслуживания включён"
			if maintenance.Message != "" {
				message += ": " + maintenance.Message
			}
			return actionResponse{Message: message}, http.StatusConflict
		}
	}
	return s.performActionUnchecked(action)
}

// performActionUnchecked преобразует start, stop или restart в операции менеджера процесса и результат
// HTTP. Вызывать после необходимых проверок доступа и обслуживания.
func (s *server) performActionUnchecked(action string) (actionResponse, int) {
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
		if code == 1 {
			result.Message = "бот уже остановлен"
		} else if code == 2 {
			result.Message = "бот остановлен принудительно"
		} else {
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
