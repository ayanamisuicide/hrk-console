package setup

import (
	"strings"
	"testing"
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

	got := parseDepOutput(out)
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
	got := parseDepOutput("import httpx\npip httpx\nimport httpx\n")
	if len(got) != 1 || got[0] != "httpx" {
		t.Errorf("дубликаты не схлопнулись: %v", got)
	}
}

// Пустой вывод — всё на месте, ставить нечего.
func TestParseDepOutputEmpty(t *testing.T) {
	if got := parseDepOutput("\n  \n"); len(got) != 0 {
		t.Errorf("на пустом выводе ожидался пустой список, got %v", got)
	}
}
