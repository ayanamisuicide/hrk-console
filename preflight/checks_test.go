package preflight

import (
	"os"
	"path/filepath"
	"testing"
)

// find возвращает проверку по имени — порядок в All меняется, привязываться
// к индексам не стоит.
func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("проверки %q нет в списке", name)
	return Check{}
}

func TestChecksOnMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "нет-такого")
	checks := All(dir, filepath.Join(dir, "heroku.log"))

	for _, name := range []string{"каталог бота", "виртуальное окружение", "права на запись"} {
		detail, st := find(t, checks, name).Run()
		if st != Failed {
			t.Errorf("%q: статус %v, ожидался Failed", name, st)
		}
		if detail == "" {
			t.Errorf("%q: пустое пояснение — рядом с именем показывать нечего", name)
		}
	}

	// Лога ещё нет — это не отказ: он появится при первом запуске бота.
	if _, st := find(t, checks, "файл лога").Run(); st != Skipped {
		t.Errorf("отсутствующий лог: статус %v, ожидался Skipped", st)
	}

	// Проверять зависимости модулей нечем, пока нет venv.
	if _, st := find(t, checks, "зависимости модулей").Run(); st != Skipped {
		t.Errorf("зависимости без venv: статус %v, ожидался Skipped", st)
	}
}

func TestChecksOnReadyDir(t *testing.T) {
	dir := t.TempDir()
	activate := filepath.Join(dir, "venv", "bin", "activate")
	if err := os.MkdirAll(filepath.Dir(activate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "heroku.log")
	if err := os.WriteFile(logFile, make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := All(dir, logFile)

	for _, name := range []string{"каталог бота", "виртуальное окружение", "права на запись"} {
		if _, st := find(t, checks, name).Run(); st != Passed {
			t.Errorf("%q: статус %v, ожидался Passed", name, st)
		}
	}

	detail, st := find(t, checks, "файл лога").Run()
	if st != Passed {
		t.Fatalf("файл лога: статус %v, ожидался Passed", st)
	}
	if detail != "2 КБ" {
		t.Errorf("размер лога: got %q, want «2 КБ»", detail)
	}

	// Проверка прав действительно создаёт .launch.lock — иначе она проверяет
	// не то, что заявляет.
	if _, err := os.Stat(filepath.Join(dir, ".launch.lock")); err != nil {
		t.Errorf("проверка прав не создала .launch.lock: %v", err)
	}
}

// python3 и ffmpeg зависят от машины, поэтому проверяется только контракт:
// исход терминальный и пояснение есть в любом случае.
func TestExternalToolChecksAlwaysExplain(t *testing.T) {
	checks := All(t.TempDir(), "")
	for _, name := range []string{"python3", "ffmpeg"} {
		detail, st := find(t, checks, name).Run()
		if st != Passed && st != Failed {
			t.Errorf("%q: статус %v, ожидался Passed или Failed", name, st)
		}
		if detail == "" {
			t.Errorf("%q: пустое пояснение", name)
		}
	}
}

func TestHasGoMod(t *testing.T) {
	dir := t.TempDir()
	if hasGoMod(dir) {
		t.Error("пустой каталог принят за корень исходников")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasGoMod(dir) {
		t.Error("каталог с go.mod не признан корнем исходников")
	}
}

// Вшитый линкером путь мог указывать на репозиторий, который с тех пор
// перенесли или удалили, — такой путь брать нельзя.
func TestSourceRootIgnoresStaleRepoRoot(t *testing.T) {
	saved := repoRoot
	t.Cleanup(func() { repoRoot = saved })

	repoRoot = filepath.Join(t.TempDir(), "перенесённый-репозиторий")
	if got := sourceRoot(); got == repoRoot {
		t.Error("несуществующий вшитый путь принят за корень исходников")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	repoRoot = dir
	if got := sourceRoot(); got != dir {
		t.Errorf("корень исходников: got %q, want %q", got, dir)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0: "0 Б", 512: "512 Б", 1024: "1 КБ", 2048: "2 КБ",
		1 << 20: "1 МБ", 10 << 20: "10 МБ",
	}
	for n, want := range cases {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d): got %q, want %q", n, got, want)
		}
	}
}
