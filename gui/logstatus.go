package main

import (
	"time"

	"heroku-console/logfeed"
)

// loadHistory поднимает хвост уже написанного лога при старте окна — без
// этого приложение открывалось бы на пустом экране, пока не придёт первая
// новая строка.
func (a *App) loadHistory() {
	lines := logfeed.TailLines(a.bot.LogFile, ringCapacity)
	a.mu.Lock()
	a.ring = append(a.ring[:0], lines...)
	a.rebuildLocked()
	a.mu.Unlock()
}

// rebuildLocked пересобирает a.visible с нуля из a.ring под текущие
// filter/showDebug/minLevel — нужна и при первой загрузке, и при любой
// смене этих настроек с фронтенда. Новый парсер продолжает жить как
// a.parser: дальше живой хвост (feedLine) кормит именно его.
// Вызывающий обязан держать a.mu.
func (a *App) rebuildLocked() {
	p := logfeed.NewParser()
	var out []*logfeed.Record
	for _, raw := range a.ring {
		rec, complete := p.Feed(raw)
		if !complete {
			continue
		}
		if !logfeed.Visible(rec, a.ui.ShowDebug, a.filter, logfeed.Level(a.ui.MinLevel)) {
			continue
		}
		if n := len(out); n > 0 && logfeed.SameEntry(out[n-1], rec) {
			out[n-1].Count = maxInt(out[n-1].Count, 1) + 1
			out[n-1].Time = rec.Time
			continue
		}
		out = append(out, rec)
	}
	a.visible = out
	a.parser = p
}

// followLog следит за живым хвостом heroku.log и кормит им a.parser по
// одной строке — в отличие от rebuildLocked, тут не полная пересборка, а
// инкрементальное схлопывание относительно уже показанного хвоста.
func (a *App) followLog() {
	from := logfeed.LineCount(a.bot.LogFile) + 1
	f, err := logfeed.Follow(a.bot.LogFile, from)
	if err != nil {
		return
	}
	a.follower = f
	for raw := range f.Lines {
		a.feedLine(raw)
	}
}

func (a *App) feedLine(raw string) {
	a.mu.Lock()
	a.ring = append(a.ring, raw)
	if len(a.ring) > ringCapacity+500 {
		a.ring = a.ring[len(a.ring)-ringCapacity:]
	}
	rec, complete := a.parser.Feed(raw)
	if !complete {
		a.mu.Unlock()
		return
	}
	var evt *TailEvent
	if logfeed.Visible(rec, a.ui.ShowDebug, a.filter, logfeed.Level(a.ui.MinLevel)) {
		if n := len(a.visible); n > 0 && logfeed.SameEntry(a.visible[n-1], rec) {
			last := a.visible[n-1]
			last.Count = maxInt(last.Count, 1) + 1
			last.Time = rec.Time
			evt = &TailEvent{Replace: true, Rec: toRec(last)}
		} else {
			a.visible = append(a.visible, rec)
			// Тот же предел, что у кольцевого буфера. Без него visible рос
			// без границы всю жизнь окна: обрезала его только пересборка при
			// смене фильтра и кнопка очистки, то есть у окна, которое просто
			// оставили открытым, — никогда. Показать больше, чем помнит ring,
			// всё равно нельзя: пересборка возьмёт историю именно оттуда.
			if len(a.visible) > ringCapacity+500 {
				a.visible = a.visible[len(a.visible)-ringCapacity:]
			}
			evt = &TailEvent{Replace: false, Rec: toRec(rec)}
		}
	}
	a.mu.Unlock()
	if evt != nil {
		a.emit("log-tail", evt)
	}
}

func (a *App) statusLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		// Один pid на весь тик: раньше statusLocked и maybeWatchdogRestart
		// спрашивали его независимо, то есть дважды сканировали /proc в
		// одну и ту же секунду ради одного и того же ответа.
		pid := a.tickPID()
		a.emitStatus(pid)
		a.maybeWatchdogRestart(pid)
	}
}

func (a *App) emitStatus(pid int) {
	a.mu.Lock()
	st := a.statusLocked(pid)
	a.mu.Unlock()
	a.emit("status", st)
}

// statusLocked собирает Status из текущего состояния. Вызывающий обязан
// держать a.mu (кроме watchMu — это отдельный замок, дедлока не будет).
func (a *App) statusLocked(pid int) Status {
	uptime := "—"
	if pid != 0 {
		uptime = a.uptime(pid)
	}
	streamIssue, streamIssueFor := "", ""
	switch {
	case a.follower != nil:
		if ok, reason, since := a.follower.Alive(); !ok {
			streamIssue, streamIssueFor = reason, humanSince(time.Since(since))
		}
	case a.remoteFollower != nil:
		if ok, reason, since := a.remoteFollower.Alive(); !ok {
			streamIssue, streamIssueFor = reason, humanSince(time.Since(since))
		}
	}
	a.watchMu.Lock()
	restarting := a.restarting
	a.watchMu.Unlock()
	return Status{
		Alive: pid != 0, PID: pid, Uptime: uptime,
		BotVersion: a.botVersion(), HkcVersion: version,
		Watchdog: a.ui.Watchdog, Restarting: restarting,
		StreamIssue: streamIssue, StreamIssueFor: streamIssueFor,
		RemoteState: a.remoteState, RemoteStateNote: a.remoteStateNote,
	}
}

// maybeWatchdogRestart — тот же контракт, что у watchdog в TUI-вьюере:
// включается вручную, поднимает бота заново только если он упал сам, не
// чаще watchdogCooldown. pid — тот же, что уже получил statusLoop за этот
// тик (tickPID), отдельно не спрашивается.
func (a *App) maybeWatchdogRestart(pid int) {
	a.mu.Lock()
	on := a.ui.Watchdog
	a.mu.Unlock()

	alive := pid != 0
	a.watchMu.Lock()
	if alive {
		a.everAlive = true
	}
	seen := a.everAlive
	a.watchMu.Unlock()

	if !on || alive || !seen {
		return
	}

	a.watchMu.Lock()
	if a.restarting || time.Since(a.lastRestartAttempt) < watchdogCooldown {
		a.watchMu.Unlock()
		return
	}
	a.restarting = true
	a.lastRestartAttempt = time.Now()
	a.watchMu.Unlock()

	a.emit("notice", "watchdog: бот не отвечает — перезапускаю")
	notifyDesktop("Heroku", "Бот не отвечает — перезапускаю")
	go func() {
		a.start()
		a.watchMu.Lock()
		a.restarting = false
		a.watchMu.Unlock()
	}()
}
