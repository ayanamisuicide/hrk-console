//go:build !linux

package botproc

// RunForeground — только для "hkc console" в TUI, который всегда запущен
// на той же Linux-машине, что и бот (см. console.go). GUI на других ОС им
// не пользуется вовсе; -1 здесь просто держит контракт "не удалось
// запустить" на ОС, где такое в принципе не имеет смысла.
func (m *Manager) RunForeground() int { return -1 }
