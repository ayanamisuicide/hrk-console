package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"heroku-console/preflight"
)

// Полоса всегда ровно заданной ширины: она стоит в строке рядом со
// счётчиком и таймером, и лишняя ячейка сдвинула бы их при каждом кадре.
func TestProgressBarWidth(t *testing.T) {
	for _, frac := range []float64{-1, 0, 0.01, 0.33, 0.5, 0.999, 1, 2} {
		for _, w := range []int{16, 28, 40} {
			if got := lipgloss.Width(progressBar(frac, w)); got != w {
				t.Errorf("progressBar(%.2f, %d): ширина %d", frac, w, got)
			}
		}
	}
}

// Дробная часть должна давать движение внутри ячейки: без частичных блоков
// полоса на семи проверках дёргалась бы шагами по четыре ячейки.
func TestProgressBarSubCell(t *testing.T) {
	a, b := progressBar(0.30, 28), progressBar(0.32, 28)
	if a == b {
		t.Error("полоса не двигается при изменении заполнения на 2%")
	}
}

func TestFmtDuration(t *testing.T) {
	cases := map[time.Duration]string{
		120 * time.Millisecond:  "120 мс",
		1400 * time.Millisecond: "1.4 с",
		12 * time.Second:        "12.0 с",
	}
	for d, want := range cases {
		if got := fmtDuration(d); got != want {
			t.Errorf("fmtDuration(%v): got %q, want %q", d, got, want)
		}
	}
}

func TestPreflightCountsDone(t *testing.T) {
	p := NewPreflight("/nonexistent", "/nonexistent/heroku.log")
	if got := p.doneCount(); got != 0 {
		t.Errorf("в начале готовых быть не должно, got %d", got)
	}
	p.checks[0].Status = preflight.Passed
	p.checks[1].Status = preflight.Skipped
	p.checks[2].Status = preflight.Failed
	p.checks[3].Status = preflight.Running
	if got := p.doneCount(); got != 3 {
		t.Errorf("пропущенная и упавшая тоже отработали: got %d, want 3", got)
	}
}

// Экран должен показывать итог и подсказку, а не пустую рамку: при провале
// он остаётся на экране до нажатия, и на нём должно быть написано почему.
func TestPreflightViewShowsOutcome(t *testing.T) {
	p := NewPreflight("/nonexistent", "/nonexistent/heroku.log")
	p.width = 80
	for i := range p.checks {
		p.checks[i].Status = preflight.Passed
	}
	p.done = true
	if v := p.View(); !strings.Contains(v, "всё готово") {
		t.Error("успешный итог не показан")
	}

	p.Failed = true
	v := p.View()
	if !strings.Contains(v, "не всё в порядке") {
		t.Error("провал не показан")
	}
	if !strings.Contains(v, "любая клавиша") {
		t.Error("при провале должно быть сказано, как продолжить")
	}
}
