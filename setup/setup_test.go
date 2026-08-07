package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNeedHerokuDir(t *testing.T) {
	dir := t.TempDir()
	if needHerokuDir(dir) {
		t.Error("существующий каталог посчитан отсутствующим")
	}
	if !needHerokuDir(filepath.Join(dir, "нет-такого")) {
		t.Error("отсутствующий каталог посчитан существующим")
	}

	// Файл с именем каталога бота — не каталог, клонировать всё равно надо.
	file := filepath.Join(dir, "файл")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !needHerokuDir(file) {
		t.Error("обычный файл принят за каталог бота")
	}
}

func TestNeedVenv(t *testing.T) {
	// Каталога бота нет — venv ставить некуда, и шаг обязан промолчать, а не
	// пытаться создать окружение в пустоте.
	if needVenv(filepath.Join(t.TempDir(), "нет-такого")) {
		t.Error("venv затребован для несуществующего каталога бота")
	}

	dir := t.TempDir()
	if !needVenv(dir) {
		t.Error("venv не затребован там, где его нет")
	}

	activate := filepath.Join(dir, "venv", "bin", "activate")
	if err := os.MkdirAll(filepath.Dir(activate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if needVenv(dir) {
		t.Error("готовый venv посчитан отсутствующим")
	}
}

// Без venv или без каталога загруженных модулей проверять нечем: разбор
// импортов запускается питоном из venv бота, чужой не годится.
func TestScanModuleDepsWithoutVenv(t *testing.T) {
	dir := t.TempDir()
	if got := scanModuleDeps(dir); got != nil {
		t.Errorf("на пустом каталоге ожидался nil, got %v", got)
	}

	// venv есть, каталога модулей нет — тоже нечего разбирать.
	python := filepath.Join(dir, "venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(python), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := scanModuleDeps(dir); got != nil {
		t.Errorf("без loaded_modules ожидался nil, got %v", got)
	}
}

// Разбор запускается питоном бота на каждом старте, а ответ спрашивают
// дважды — автонастройка и preflight. Второй раз должен прийти из кэша.
func TestMissingModuleDepsUsesCache(t *testing.T) {
	t.Cleanup(invalidateDepCache)

	invalidateDepCache()
	cached := []string{"httpx"}
	depCache = &cached

	got := MissingModuleDeps(t.TempDir()) // каталог пустой — без кэша был бы nil
	if len(got) != 1 || got[0] != "httpx" {
		t.Errorf("кэш не использован: got %v", got)
	}

	invalidateDepCache()
	if got := MissingModuleDeps(t.TempDir()); got != nil {
		t.Errorf("после сброса кэша ожидался пересчёт, got %v", got)
	}
}

func TestRunReportsExitStatus(t *testing.T) {
	if !run("true") {
		t.Error("успешная команда отмечена как провалившаяся")
	}
	if run("false") {
		t.Error("команда с ненулевым кодом отмечена как успешная")
	}
	if run("hkc-заведомо-нет-такой-команды") {
		t.Error("несуществующая команда отмечена как успешная")
	}
}
