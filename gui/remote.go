package main

import (
	"fmt"
	"os"
	"path/filepath"

	"heroku-console/remotebot"
	"heroku-console/state"
)

// startRemote поднимает бота на другой машине по SSH вместо локального
// botproc — переставляет точки подмены (pid/aliveAt/start/stop/uptime/
// botVersion) на remotebot.Client, дальше statusLoop и вотчдог работают
// не зная разницы. Осечка при подключении (неверный хост, ключ не
// подходит) оставляет точки подмены как есть — они по-прежнему смотрят на
// локальный a.bot, что на машине без /proc и без каталога бота просто
// молча ничего не находит (PID()==0, Version()==""), а не падает: то же
// самое "тихая осечка не должна ронять окно", что и у остальных
// best-effort частей GUI. Notice в логе — единственный способ узнать
// почему.
func (a *App) startRemote() {
	r := a.ui.Remote
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		a.emit("notice", "не удалось определить каталог настроек: "+err.Error())
		a.setRemoteState("offline", err.Error())
		a.failRemotePreflight(err)
		return
	}
	c, err := remotebot.Dial(remotebot.Config{
		Host:           r.Host,
		User:           r.User,
		KeyPath:        r.KeyPath,
		HerokuDir:      r.Dir,
		KnownHostsPath: filepath.Join(cfgDir, "hkc", "known_hosts"),
	})
	if err != nil {
		a.emit("notice", "не удалось подключиться к "+r.Host+": "+err.Error())
		a.setRemoteState("offline", err.Error())
		a.failRemotePreflight(err)
		return
	}
	a.remote = c
	a.setRemoteState("online", "SSH · "+r.Dir)

	// Сорвавшаяся SSH-команда — это «не смог спросить», а не «бота нет».
	// Разница видна невооружённым глазом: на локальной машине /proc не
	// подводит, а через сеть ответ теряется регулярно, и без этого различия
	// pid в окне мигал 1234 → 0 → 1234 на каждой заминке, а вотчдог
	// принимался «спасать» живого бота. Держим последний достоверный ответ
	// remoteStaleWindow — дальше уже честнее признать, что мы не знаем.
	//
	// Заодно это и единственный регулярный пульс соединения: опрос pid идёт
	// каждую секунду, отдельно пинговать хост было бы тем же запросом ещё
	// раз.
	online := func(pid int) int {
		a.setRemoteState("online", "SSH · "+r.Dir)
		a.rememberPID(pid)
		return pid
	}
	offline := func(err error) int {
		a.setRemoteState("offline", err.Error())
		return a.stalePID()
	}
	a.pid = func() int {
		pid, err := c.PID()
		if err != nil {
			return offline(err)
		}
		return online(pid)
	}
	a.aliveAt = func(pid int) bool {
		pids, err := c.PIDs()
		if err != nil {
			return offline(err) == pid
		}
		for _, p := range pids {
			if p == pid {
				online(pid)
				return true
			}
		}
		a.setRemoteState("online", "SSH · "+r.Dir)
		return false
	}
	a.start = func() startResult {
		r := c.Start()
		return startResult{PID: r.PID, AlreadyStarting: r.AlreadyStarting, Err: r.Err}
	}
	a.stop = c.Stop
	a.uptime = func(pid int) string {
		s, err := c.Uptime(pid)
		if err != nil {
			return "—"
		}
		return s
	}
	a.botVersion = func() string {
		v, _ := c.Version()
		return v
	}

	go a.runChecklist(c.Preflight())
	a.loadRemoteHistory()
	go a.followRemoteLog()
}

// failRemotePreflight отмечает все проверки как заведомо неудавшиеся —
// подключиться не вышло даже для того, чтобы их выполнить (плохие
// настройки, сеть). Экран проверок не должен виснуть с крутящимися
// индикаторами до бесконечности только потому, что нет самого способа их
// прогнать: пользователь должен увидеть ровно ту же причину, что уже ушла
// в notice, прямо там, куда и так смотрит.
func (a *App) failRemotePreflight(err error) {
	checks := (&remotebot.Client{}).Preflight()
	for i, c := range checks {
		a.emit("preflight-check", PreflightEvent{
			Index: i, Total: len(checks), Name: c.Name,
			Status: "failed", Detail: "нет подключения: " + err.Error(),
		})
	}
}

// loadRemoteHistory — то же, что loadHistory делает локально, только
// историю приносит одна SSH-команда (remotebot.Client.TailLines), а не
// чтение файла с диска.
func (a *App) loadRemoteHistory() {
	lines, err := a.remote.TailLines(ringCapacity)
	if err != nil {
		a.emit("notice", "не удалось прочитать лог на удалённой машине: "+err.Error())
		return
	}
	a.mu.Lock()
	a.ring = append(a.ring[:0], lines...)
	a.rebuildLocked()
	a.mu.Unlock()
}

// followRemoteLog — удалённый аналог followLog: тот же feedLine на каждую
// новую строку, разница только в источнике (remotebot.Client.Follow вместо
// logfeed.Follow).
func (a *App) followRemoteLog() {
	h, err := a.remote.Follow()
	if err != nil {
		a.emit("notice", "не удалось начать слежение за логом: "+err.Error())
		return
	}
	a.remoteFollower = h
	for raw := range h.Lines {
		a.feedLine(raw)
	}
}

// ConnectRemote сохраняет настройки подключения к боту на другой машине и
// перезапускает приложение (тем же RestartApp, что и после самообновления).
// Переключение между локальным ботом и удалённым на лету — по сути другая
// программа (другой источник /proc, другой файл лога, другое соединение) —
// пересобирать половину App вместо честного перезапуска было бы источником
// трудноуловимых гонок между старыми и новыми горутинами.
func (a *App) ConnectRemote(cfg state.Remote) ActionResult {
	a.mu.Lock()
	a.ui.Remote = cfg
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
	return a.RestartApp()
}

// DisconnectRemote возвращает к локальному боту тем же перезапуском.
func (a *App) DisconnectRemote() ActionResult {
	a.mu.Lock()
	a.ui.Remote = state.Remote{}
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
	return a.RestartApp()
}

// TestRemoteConnection — «проверить соединение» до того, как сохранить
// настройки и перезапустить окно: тот же remotebot.Dial, что и настоящее
// подключение (включая TOFU-проверку ключа хоста), но соединение сразу
// закрывается и ничего не сохраняется. Без этого метода единственный
// способ узнать, что хост/логин/ключ введены неверно, — сохранить,
// перезапустить окно и увидеть notice постфактум.
func (a *App) TestRemoteConnection(cfg state.Remote) ActionResult {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	c, err := remotebot.Dial(remotebot.Config{
		Host:           cfg.Host,
		User:           cfg.User,
		KeyPath:        cfg.KeyPath,
		HerokuDir:      cfg.Dir,
		KnownHostsPath: filepath.Join(cfgDir, "hkc", "known_hosts"),
	})
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}
	defer c.Close()

	pid, err := c.PID()
	if err != nil {
		return ActionResult{OK: false, Message: "подключился, но не смог прочитать /proc: " + err.Error()}
	}
	if pid == 0 {
		return ActionResult{OK: true, Message: "подключение работает, но бот сейчас не запущен"}
	}
	return ActionResult{OK: true, Message: fmt.Sprintf("подключение работает, бот жив (pid %d)", pid)}
}
