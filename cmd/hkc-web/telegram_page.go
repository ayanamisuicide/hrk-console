package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"heroku-console/internal/telegram"
)

// Страница «Telegram» одна на админку и мини-приложение: те же карточки, тот же порядок, одни и те
// же адреса /api/telegram. Админка приходит с административным токеном, мини-приложение — с сессией
// Telegram. Отличие одно: главный переключатель управления меняется только из админки, иначе выключивший
// его из Telegram сам закрыл бы себе дорогу назад.

// pageViewer — кто открыл страницу «Telegram».
type pageViewer struct {
	actor string
	// user — администратор Telegram из мини-приложения; nil — администратор панели.
	user *telegram.User
}

type pageViewerKey struct{}

// telegramPageAuth пускает администратора панели и администратора Telegram из мини-приложения.
func (s *server) telegramPageAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		viewer := pageViewer{actor: "admin"}
		if !s.adminAuthorized(r) {
			// Мини-приложение заново получит сессию по данным запуска и узнает настоящую причину отказа.
			user, status, _ := s.telegramSession(r)
			if status != 0 {
				writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
				return
			}
			viewer = pageViewer{actor: user.Actor(), user: &user}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), pageViewerKey{}, viewer)))
	}
}

func requestViewer(r *http.Request) pageViewer {
	viewer, _ := r.Context().Value(pageViewerKey{}).(pageViewer)
	return viewer
}

// pageControl — бот Telegram или пустая заглушка, если он не настроен: страница всё равно
// показывает шаги настройки.
func (s *server) pageControl() *telegramControl {
	if s.telegram != nil {
		return s.telegram
	}
	return newTelegramControl(s, telegramConfig{})
}

func (s *server) getTelegram(w http.ResponseWriter, r *http.Request) {
	viewer := requestViewer(r)
	control := s.pageControl()
	view := control.view()
	view["settings"] = control.settings()
	view["activity"] = s.telegramActivity(15)
	view["digest"] = control.digestView()
	view["notifications"] = s.alerts.channels()["telegram"]
	view["viewer"] = map[string]any{"telegram": viewer.user != nil, "name": strings.TrimPrefix(viewer.actor, "telegram:")}

	mute := map[string]any{"available": control.api != nil, "active": false}
	if state, ok := control.mute.Active(); ok {
		mute["active"], mute["all"] = true, state.All
		mute["until"] = state.Until.UTC()
		mute["untilText"] = digestWhen(state.Until, time.Now())
	}
	view["mute"] = mute

	watchdog := map[string]any{"available": s.operations != nil}
	if s.operations != nil {
		snapshot := s.watchdog.snapshot()
		watchdog["enabled"] = s.operations.watchdogSettings().Enabled
		watchdog["state"], watchdog["message"] = snapshot.State, snapshot.Message
	}
	view["watchdog"] = watchdog

	update := map[string]any{"available": s.updates != nil, "installed": currentVersion().Version}
	if s.updates != nil {
		remote := s.updates.snapshot()
		if remote.Version != "" && compareVersions(remote.Version, currentVersion().Version) > 0 {
			update["latest"], update["note"] = remote.Version, firstNote(remote.Releases)
		}
		update["running"] = jobRunning(readUpdateJob())
	}
	view["update"] = update
	writeJSON(w, http.StatusOK, view)
}

func (s *server) setTelegram(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeJSON(w, http.StatusServiceUnavailable, actionResponse{Message: "хранилище операций недоступно"})
		return
	}
	viewer := requestViewer(r)
	settings := s.operations.telegramSettings()
	control := settings.ControlEnabled
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректные настройки Telegram"})
		return
	}
	if settings.DigestAt != "" && !digestTimeRe.MatchString(settings.DigestAt) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "время сводки — в виде ЧЧ:ММ"})
		return
	}
	if viewer.user != nil && settings.ControlEnabled != control {
		writeJSON(w, http.StatusForbidden, actionResponse{Message: "управление из Telegram включается и выключается в админке панели"})
		return
	}
	if err := s.operations.setTelegram(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	if s.telegram != nil {
		s.telegram.reload()
	}
	s.record(r, viewer.actor, "telegram.settings", fmt.Sprintf("control=%t confirm=%t digest=%t at=%s", settings.ControlEnabled, settings.Confirm, !settings.DigestOff, settings.DigestAt))
	s.getTelegram(w, r)
}

// telegramMute включает или снимает тишину уведомлений: {"for":"1h"|"3h"|"morning","all":bool} или {"off":true}.
func (s *server) telegramMute(w http.ResponseWriter, r *http.Request) {
	control := s.telegram
	if control == nil || control.api == nil {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "Telegram-бот не настроен"})
		return
	}
	var input struct {
		For string `json:"for"`
		All bool   `json:"all"`
		Off bool   `json:"off"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	viewer := requestViewer(r)
	if input.Off {
		if err := control.mute.Clear(); err != nil {
			writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
			return
		}
		s.record(r, viewer.actor, "telegram.unmute", "")
		s.getTelegram(w, r)
		return
	}
	now := control.now()
	var d time.Duration
	switch input.For {
	case "1h":
		d = time.Hour
	case "3h":
		d = 3 * time.Hour
	case "morning":
		d, _ = telegram.ParseMuteDuration("до утра", now)
	default:
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "на сколько: 1h, 3h или morning"})
		return
	}
	until := now.Add(d)
	if err := control.mute.Set(until, input.All); err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: err.Error()})
		return
	}
	s.record(r, viewer.actor, "telegram.mute", fmt.Sprintf("до %s all=%t", until.UTC().Format(time.RFC3339), input.All))
	s.getTelegram(w, r)
}

// telegramWatchdog включает и выключает автовосстановление ({"enabled":bool}) или возобновляет его
// после приостановки ({"resume":true}). Остальные настройки — время и число попыток — в админке.
func (s *server) telegramWatchdog(w http.ResponseWriter, r *http.Request) {
	if s.operations == nil {
		writeJSON(w, http.StatusServiceUnavailable, actionResponse{Message: "хранилище операций недоступно"})
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
		Resume  bool  `json:"resume"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil || (input.Enabled == nil && !input.Resume) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректный запрос"})
		return
	}
	viewer := requestViewer(r)
	now := time.Now()
	s.botActionMu.Lock()
	settings := s.operations.watchdogSettings()
	if input.Enabled != nil {
		settings.Enabled = *input.Enabled
		if err := s.operations.setWatchdog(settings); err != nil {
			s.botActionMu.Unlock()
			writeJSON(w, http.StatusBadRequest, actionResponse{Message: err.Error()})
			return
		}
	}
	// Явное действие человека снимает приостановку, как и сохранение настроек в админке.
	s.watchdog.resume()
	pid, healthy, reason := s.watchdogObservation(now)
	s.watchdog.tick(now, settings, pid, healthy, reason)
	s.botActionMu.Unlock()
	if input.Enabled != nil {
		s.record(r, viewer.actor, "watchdog.update", fmt.Sprintf("enabled=%t timeout=%d maxAttempts=%d", settings.Enabled, settings.TimeoutSeconds, settings.MaxAttempts))
	} else {
		s.record(r, viewer.actor, "watchdog.resume", "серия попыток сброшена")
	}
	s.getTelegram(w, r)
}

// telegramUpdate ставит новую версию панели. Из мини-приложения итог установки придёт и в чат с
// ботом: панель перезапустится, и приложение на минуту потеряет связь.
func (s *server) telegramUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "служба обновления не настроена"})
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil || !releaseTag.MatchString(input.Version) {
		writeJSON(w, http.StatusBadRequest, actionResponse{Message: "некорректная версия"})
		return
	}
	if compareVersions(input.Version, currentVersion().Version) <= 0 {
		writeJSON(w, http.StatusConflict, actionResponse{Message: input.Version + " уже установлена"})
		return
	}
	viewer := requestViewer(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, status := s.launchUpdate(ctx, updateRequest{Action: "install", Version: input.Version})
	detail := result.Message
	if status == http.StatusAccepted {
		detail = input.Version
	}
	s.record(r, viewer.actor, "update.install", detail)
	if status == http.StatusAccepted && viewer.user != nil && s.telegram != nil && s.telegram.api != nil {
		c := s.telegram
		pending := pendingUpdate{Chat: viewer.user.ID, Version: input.Version, Actor: viewer.actor, At: c.now().UTC()}
		if err := c.savePending(pending); err != nil {
			log.Printf("telegram: не удалось запомнить обновление: %v", err)
		}
		go c.watchUpdate(pending)
	}
	writeJSON(w, status, result)
}
