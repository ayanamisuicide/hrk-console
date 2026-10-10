//go:build !linux

package botproc

import "errors"

// errUnsupported сообщает, что сервер управления должен работать рядом с
// ботом на Linux. Открывать саму веб-панель браузером можно с любой ОС.
var errUnsupported = errors.New("сервер управления ботом поддерживается только на Linux")

// Stop возвращает результат без Linux-управления процессом: эта платформа не поддерживается.
func (m *Manager) Stop() int { return 1 }

// Start возвращает ошибку неподдерживаемой платформы вместо запуска бота.
func (m *Manager) Start() StartResult { return StartResult{Err: errUnsupported} }
