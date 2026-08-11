// Package state сохраняет настройки экрана вьюера между запусками: без
// этого DEBUG, порог показа и автоперезапуск приходится каждый раз включать
// заново, хотя человек выключил или включил их осознанно в прошлый раз.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// State — то, что переживает перезапуск консоли. Только настройки показа и
// поведения — сам лог, фильтр и позиция прокрутки каждый раз актуальны
// заново и сохранению не подлежат.
type State struct {
	ShowDebug   bool   `json:"showDebug"`
	ShowSidebar bool   `json:"showSidebar"`
	MinLevel    int    `json:"minLevel"`
	Watchdog    bool   `json:"watchdog"`
	Remote      Remote `json:"remote"`
	// UpdateChannel — откуда самообновление берёт "последнюю версию":
	// "" (значение по умолчанию) — стабильные релизы из main, "dev" —
	// сборки прямо с ветки dev (см. selfupdate.CheckChannel/ApplyChannel).
	// Только GUI даёт это выбрать — у TUI своего экрана обновления с
	// таким выбором нет, он всегда на стабильном канале.
	UpdateChannel string `json:"updateChannel"`
}

// Remote — настройки подключения к боту на другой машине по SSH (см. пакет
// remotebot). Актуально только для GUI: TUI запускается уже на той же
// машине, где живёт бот, ему нечего конфигурировать. Host пустой — значит
// GUI управляет ботом локально, как раньше; это и есть поведение по
// умолчанию для уже сохранённых state.json без этого поля.
type Remote struct {
	Host    string `json:"host"`
	User    string `json:"user"`
	KeyPath string `json:"keyPath"`
	Dir     string `json:"dir"`
}

// Default — с чем консоль запускается, если сохранённого состояния нет или
// оно повреждено: панель видна, DEBUG скрыт, порог снят, автоперезапуск
// выключен — прежнее поведение до появления персистентности.
var Default = State{ShowSidebar: true}

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hkc", "state.json"), nil
}

// Load читает сохранённое состояние. Любая ошибка — нет файла, битый JSON,
// недоступен конфиг-каталог — тихо возвращает Default: настройки экрана не
// стоят того, чтобы ронять запуск консоли сообщением об ошибке.
func Load() State {
	p, err := path()
	if err != nil {
		return Default
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Default
	}
	var s State
	if json.Unmarshal(data, &s) != nil {
		return Default
	}
	return s
}

// Save пишет состояние при выходе из вьюера. Ошибка никуда не сообщается —
// это фоновое удобство, а не то, из-за чего стоит портить прощание с
// консолью сообщением об ошибке.
func Save(s State) {
	p, err := path()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}
