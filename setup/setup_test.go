package setup

import (
	"os"
	"path/filepath"
	"strings"
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

// aptInstall обязан повторить попытку ровно один раз после apt-get update,
// если первая install-попытка провалилась, — и не обновлять индекс снова на
// следующий вызов aptInstall в том же прогоне (см. aptUpdated). Настоящих
// apt-get/sudo в CI нет, поэтому runFn подменяется на счётчик вызовов вместо
// реального exec.

func withFakeRun(t *testing.T, fn func(name string, args ...string) bool) {
	t.Helper()
	origRun, origHasApt, origUpdated := runFn, hasAptGet, aptUpdated
	runFn, hasAptGet, aptUpdated = fn, func() bool { return true }, false
	t.Cleanup(func() { runFn, hasAptGet, aptUpdated = origRun, origHasApt, origUpdated })
}

func TestAptInstallRetriesOnceAfterUpdate(t *testing.T) {
	var calls []string
	withFakeRun(t, func(name string, args ...string) bool {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		// Первая попытка install проваливается ("устаревший индекс"),
		// apt-get update и повторный install — успешны.
		if len(calls) == 1 {
			return false
		}
		return true
	})

	if !aptInstall("ffmpeg") {
		t.Fatal("aptInstall должен был вернуть true после успешного повтора")
	}
	want := []string{
		"sudo apt-get install -y ffmpeg",
		"sudo apt-get update",
		"sudo apt-get install -y ffmpeg",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("вызовы = %v, ожидалось %v", calls, want)
	}
}

func TestAptInstallDoesNotRepeatUpdateInSameRun(t *testing.T) {
	var updateCalls int
	withFakeRun(t, func(name string, args ...string) bool {
		if name == "sudo" && len(args) > 0 && args[0] == "apt-get" && len(args) > 1 && args[1] == "update" {
			updateCalls++
			return true
		}
		return false // install всегда проваливается — имя пакета правда не существует
	})

	aptInstall("kitty")
	aptInstall("ffmpeg") // второй провал в том же прогоне не должен снова обновлять индекс

	if updateCalls != 1 {
		t.Errorf("apt-get update вызван %d раз за прогон, ожидался ровно один", updateCalls)
	}
}

// Успешная первая попытка не должна трогать apt-get update вообще — это и
// есть подавляющее большинство запусков (индекс обычно свежий), платить
// временем сетевого update ради них было бы напрасно.
func TestAptInstallNoRetryWhenFirstAttemptSucceeds(t *testing.T) {
	var calls int
	withFakeRun(t, func(name string, args ...string) bool {
		calls++
		return true
	})

	if !aptInstall("python3") {
		t.Fatal("aptInstall должен был вернуть true")
	}
	if calls != 1 {
		t.Errorf("вызовов run: %d, ожидался ровно один (без apt-get update)", calls)
	}
}
