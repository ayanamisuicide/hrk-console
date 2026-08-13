package main

import (
	"fmt"
	"time"

	"heroku-console/logfeed"
	"heroku-console/state"
)

// ClearLog сбрасывает буфер и показанную историю — старые накопленные
// ошибки перестают маячить в панели и счётчиках. Сам файл heroku.log не
// трогается: живой хвост (followLog) продолжает читать его с той же
// позиции, что и раньше, просто дальше пишет уже в пустой буфер.
func (a *App) ClearLog() {
	a.mu.Lock()
	a.ring = a.ring[:0]
	a.visible = nil
	a.parser = logfeed.NewParser()
	a.mu.Unlock()
}

func (a *App) SetFilter(text string) []Rec {
	a.mu.Lock()
	a.filter = text
	a.rebuildLocked()
	recs := toRecs(a.visible)
	a.mu.Unlock()
	return recs
}

func (a *App) SetShowDebug(on bool) []Rec {
	a.mu.Lock()
	a.ui.ShowDebug = on
	a.rebuildLocked()
	recs, ui := toRecs(a.visible), a.ui
	a.mu.Unlock()
	state.Save(ui)
	return recs
}

// CycleMinLevel — тот же цикл, что клавиша w в TUI: всё → warning+ → error+.
func (a *App) CycleMinLevel() []Rec {
	a.mu.Lock()
	switch logfeed.Level(a.ui.MinLevel) {
	case logfeed.LevelWarning:
		a.ui.MinLevel = int(logfeed.LevelError)
	case logfeed.LevelError:
		a.ui.MinLevel = int(logfeed.LevelDebug)
	default:
		a.ui.MinLevel = int(logfeed.LevelWarning)
	}
	a.rebuildLocked()
	recs, ui := toRecs(a.visible), a.ui
	a.mu.Unlock()
	state.Save(ui)
	return recs
}

func (a *App) SetWatchdog(on bool) {
	a.mu.Lock()
	a.ui.Watchdog = on
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
}

func (a *App) StartBot() ActionResult {
	// Отмечаем попытку до самого запуска: exec ещё не заменил cmdline bash на
	// "python3 -m heroku" в первые мгновения, PID() короткое время видит 0, и
	// без этой отметки вотчдог на ближайшем тике принимает только что
	// стартовавший процесс за упавший и бьёт тревогу поверх ручного запуска.
	a.watchMu.Lock()
	a.lastRestartAttempt = time.Now()
	a.watchMu.Unlock()

	res := a.start()
	switch {
	case res.Err != nil:
		return ActionResult{OK: false, Message: res.Err.Error()}
	case res.AlreadyStarting:
		return ActionResult{OK: true, Message: "запуск уже идёт в другом окне"}
	default:
		return ActionResult{OK: true, Message: fmt.Sprintf("pid %d", res.PID)}
	}
}

func (a *App) StopBot() ActionResult {
	code, err := a.stop()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	switch code {
	case 0:
		return ActionResult{OK: true, Message: "остановлен"}
	case 2:
		return ActionResult{OK: true, Message: "принудительно остановлен"}
	default:
		return ActionResult{OK: true, Message: "не был запущен"}
	}
}

func (a *App) RestartBot() ActionResult {
	if a.alive() {
		a.stop()
	}
	return a.StartBot()
}
