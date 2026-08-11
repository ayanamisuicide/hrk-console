//go:build linux

package main

import "os/exec"

// notifyDesktop — best-effort desktop-уведомление через notify-send (часть
// libnotify-bin, обычно уже стоит на Linux-рабочих столах): чтобы падение
// бота было видно, даже когда окно GUI свёрнуто или не в фокусе. Нет
// бинарника или недоступны DISPLAY/DBUS — тихо ничего не делает, само
// уведомление не настолько важно, чтобы падать или шуметь в лог.
func notifyDesktop(title, body string) {
	_ = exec.Command("notify-send", "-a", "Heroku", title, body).Run()
}
