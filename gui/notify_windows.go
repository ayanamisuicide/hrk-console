//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// notifyDesktop — Windows-аналог notify-send: всплывающее уведомление через
// System.Windows.Forms.NotifyIcon (тот же механизм, что и обычная иконка в
// области уведомлений — доступен в любой Windows из коробки, не требует ни
// регистрации AppID, ни лишних модулей, в отличие от современного toast
// API). PowerShell-процесс живёт несколько секунд сам по себе и не
// блокирует вызывающего — уведомление не настолько важно, чтобы ждать его
// отрисовки; HideWindow — чтобы на экране не мелькало окно консоли.
func notifyDesktop(title, body string) {
	script := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
$n = New-Object System.Windows.Forms.NotifyIcon
$n.Icon = [System.Drawing.SystemIcons]::Warning
$n.Visible = $true
$n.ShowBalloonTip(6000, %s, %s, [System.Windows.Forms.ToolTipIcon]::Warning)
Start-Sleep -Seconds 7
$n.Dispose()
`, psQuote(title), psQuote(body))

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
