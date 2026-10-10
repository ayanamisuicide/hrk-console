package botproc

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Мост входит в бинарник и подключается только к виртуальному окружению этой установки.
// Исходники Heroku остаются нетронутыми; .pth переживает перезапуски из Telegram/systemd.
//
//go:embed bridge/hkc_modules_bridge.py
var modulesBridge []byte

func (m *Manager) InstallModulesBridge() error {
	venv := m.VirtualEnv()
	if venv == "" {
		return fmt.Errorf("виртуальное окружение Heroku не найдено")
	}
	sites, err := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages"))
	if err != nil || len(sites) != 1 {
		return fmt.Errorf("ожидался один каталог site-packages в %s", venv)
	}
	if err := writeBridgeFile(filepath.Join(sites[0], "hkc_modules_bridge.py"), modulesBridge); err != nil {
		return err
	}
	root, err := filepath.Abs(m.HerokuDir)
	if err != nil {
		return err
	}
	line := "import hkc_modules_bridge; hkc_modules_bridge.install(" + strconv.Quote(root) + ")\n"
	return writeBridgeFile(filepath.Join(sites[0], "hkc_modules_bridge.pth"), []byte(line))
}

// Атомарная замена не оставляет недописанный импорт при параллельном рестарте Python.
func writeBridgeFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".hkc-bridge-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
