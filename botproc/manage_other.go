//go:build !linux

package botproc

import "errors"

// errUnsupported — на любой ОС, кроме Linux, локальное управление ботом
// (Setsid, flock, сигналы) не имеет смысла: сам бот живёт на Linux-машине
// (см. /proc-сканирование в botproc.go, тоже пустое здесь). GUI на такой ОС
// подключается к боту удалённо (см. пакет remotebot) и эти методы никогда
// не вызывает — они существуют только чтобы botproc вообще собирался под
// Windows/macOS, раз App держит *botproc.Manager как поле независимо от
// режима.
var errUnsupported = errors.New("локальное управление ботом не поддерживается на этой ОС — используйте удалённое подключение")

func (m *Manager) Stop() int { return 1 }

func (m *Manager) Start() StartResult { return StartResult{Err: errUnsupported} }
