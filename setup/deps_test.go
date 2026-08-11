package setup

import (
	"strings"
	"testing"
	"time"
)

func TestParseDepOutput(t *testing.T) {
	out := strings.Join([]string{
		"import httpx",
		"import PIL",    // ставить надо pillow, а не PIL
		"import heroku", // сам бот, не пакет
		"pip whoosh",    // из заголовка # requires: — имя уже пакетное
		"import aiogram",
		"",
	}, "\n")

	got := ParseDepOutput(out)
	want := []string{"httpx", "pillow", "whoosh", "aiogram"}
	if len(got) != len(want) {
		t.Fatalf("получено %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("позиция %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// Один и тот же пакет может прийти и импортом, и заголовком requires —
// ставить его дважды незачем.
func TestParseDepOutputDeduplicates(t *testing.T) {
	got := ParseDepOutput("import httpx\npip httpx\nimport httpx\n")
	if len(got) != 1 || got[0] != "httpx" {
		t.Errorf("дубликаты не схлопнулись: %v", got)
	}
}

// Пустой вывод — всё на месте, ставить нечего.
func TestParseDepOutputEmpty(t *testing.T) {
	if got := ParseDepOutput("\n  \n"); len(got) != 0 {
		t.Errorf("на пустом выводе ожидался пустой список, got %v", got)
	}
}

func TestFailedDepsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := loadFailedDeps(dir); got != nil {
		t.Fatalf("на пустом каталоге ожидался nil, got %v", got)
	}

	now := time.Now()
	saveFailedDeps(dir, map[string]failedDep{"nope-pkg": {At: now}})
	got := loadFailedDeps(dir)
	fd, ok := got["nope-pkg"]
	if !ok {
		t.Fatal("сохранённая запись не нашлась после перечитывания")
	}
	if !fd.At.Equal(now) {
		t.Errorf("время: got %v, want %v", fd.At, now)
	}
}
