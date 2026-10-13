package main

import (
	"path/filepath"
)

// Тишина уведомлений хранится в каталоге данных панели, поэтому переживает её перезапуск.
// Разбор фраз и само состояние — в internal/telegram (mute.go).

const muteFile = "telegram-mute.json"

func muteFilePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, muteFile)
}
