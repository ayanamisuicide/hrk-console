//go:build !linux && !windows

package main

// notifyDesktop — ни Linux, ни Windows: реализации нет (проект собирается
// и публикуется только под эти две ОС), но заглушка держит пакет
// собираемым на любой другой — то же best-effort решение, что и у
// notify-send: молчать, когда сказать нечем, лучше, чем не собраться.
func notifyDesktop(title, body string) {}
