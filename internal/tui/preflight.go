package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/preflight"
	"heroku-console/internal/theme"
)

// Preflight — экран проверок перед запуском. Проверки идут по одной, а не
// разом: так видно, на какой именно всё встало, и прогон читается как
// последовательность, а не как список, который моргнул и закрылся.
type Preflight struct {
	checks  []preflight.Check
	current int
	width   int
	frame   int  // кадр анимации спиннера
	done    bool
	Failed  bool // хоть одна проверка не прошла — вызывающий решает, что делать
	// hold удерживает экран после завершения: при успехе коротко, чтобы
	// глаз успел увидеть результат, при провале — до нажатия клавиши.
	holdUntil time.Time
}

func NewPreflight(herokuDir, logFile string) *Preflight {
	return &Preflight{checks: preflight.All(herokuDir, logFile)}
}

type checkDoneMsg struct {
	index  int
	detail string
	status preflight.Status
	took   time.Duration
}
type frameMsg time.Time
type holdOverMsg struct{}

const spinnerFPS = 12

func frameCmd() tea.Cmd {
	return tea.Tick(time.Second/spinnerFPS, func(t time.Time) tea.Msg { return frameMsg(t) })
}

// runCheck выполняет проверку в фоне, чтобы анимация не замирала: go test
// занимает секунды, и без этого спиннер стоял бы колом всё это время.
func (p *Preflight) runCheck(i int) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		detail, status := p.checks[i].Run()
		return checkDoneMsg{i, detail, status, time.Since(start)}
	}
}

func (p *Preflight) Init() tea.Cmd {
	if len(p.checks) == 0 {
		return tea.Quit
	}
	p.checks[0].Status = preflight.Running
	return tea.Batch(frameCmd(), p.runCheck(0))
}

func (p *Preflight) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		return p, nil

	case frameMsg:
		p.frame++
		if p.done && time.Now().After(p.holdUntil) && !p.Failed {
			return p, tea.Quit
		}
		return p, frameCmd()

	case checkDoneMsg:
		p.checks[msg.index].Status = msg.status
		p.checks[msg.index].Detail = msg.detail
		p.checks[msg.index].Took = msg.took
		if msg.status == preflight.Failed {
			p.Failed = true
		}
		next := msg.index + 1
		if next < len(p.checks) {
			p.current = next
			p.checks[next].Status = preflight.Running
			return p, p.runCheck(next)
		}
		p.done = true
		p.holdUntil = time.Now().Add(700 * time.Millisecond)
		return p, nil

	case tea.KeyMsg:
		// При провале ждём подтверждения — иначе сообщение о том, что не так,
		// смахнёт следующим экраном раньше, чем его успеют прочитать.
		if p.done {
			return p, tea.Quit
		}
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			p.Failed = true
			return p, tea.Quit
		}
	}
	return p, nil
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (p *Preflight) View() string {
	var b strings.Builder
	width := p.width
	if width < 40 {
		width = 60
	}

	b.WriteString("\n")
	title := theme.Title.Render("  HEROKU") + theme.Meta.Render("  ·  проверки перед запуском")
	b.WriteString(title + "\n")
	b.WriteString("  " + theme.Gradient("─", width-4, false) + "\n\n")

	for i := range p.checks {
		c := &p.checks[i]
		var mark, name, detail string

		switch c.Status {
		case preflight.Pending:
			mark = theme.Faint.Render("○")
			name = theme.Faint.Render(c.Name)
		case preflight.Running:
			mark = lipgloss.NewStyle().Foreground(theme.Mauve).
				Render(spinnerFrames[p.frame%len(spinnerFrames)])
			name = lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render(c.Name)
		case preflight.Passed:
			mark = lipgloss.NewStyle().Foreground(theme.Green).Render("✓")
			name = theme.Meta.Render(c.Name)
			detail = theme.Faint.Render(c.Detail)
		case preflight.Failed:
			mark = lipgloss.NewStyle().Foreground(theme.Red).Bold(true).Render("✗")
			name = lipgloss.NewStyle().Foreground(theme.Red).Bold(true).Render(c.Name)
			detail = lipgloss.NewStyle().Foreground(theme.Red).Render(c.Detail)
		case preflight.Skipped:
			mark = theme.Faint.Render("–")
			name = theme.Faint.Render(c.Name)
			detail = theme.Faint.Render(c.Detail)
		}

		// Имя печатаем уже стилизованным, а колонку выравниваем по видимой
		// ширине: lipgloss.Width не считает ANSI-коды за символы.
		line := "  " + mark + "  " + name + strings.Repeat(" ", maxInt(1, 22-lipgloss.Width(name)))
		if detail != "" {
			// Пояснение (обычно путь) может быть длиннее окна — обрезаем
			// слева, чтобы остался хвост: у путей значимая часть в конце.
			line += clipLeft(c.Detail, detail, width-lipgloss.Width(line)-2)
		}
		if c.Status == preflight.Passed && c.Took > 50*time.Millisecond {
			line += theme.Faint.Render(fmt.Sprintf("  (%s)", c.Took.Round(10*time.Millisecond)))
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	switch {
	case !p.done:
		total, done := len(p.checks), p.current
		b.WriteString("  " + theme.Meta.Render(progressBar(done, total, 28)) +
			theme.Faint.Render(fmt.Sprintf("  %d/%d", done, total)) + "\n")
	case p.Failed:
		b.WriteString("  " + lipgloss.NewStyle().Foreground(theme.Red).Bold(true).
			Render("не всё в порядке") +
			theme.Faint.Render("  ·  любая клавиша — продолжить всё равно") + "\n")
	default:
		b.WriteString("  " + lipgloss.NewStyle().Foreground(theme.Green).Bold(true).
			Render("всё готово") + "\n")
	}
	return b.String()
}

// clipLeft укорачивает пояснение до limit ячеек, срезая начало и подставляя
// многоточие: plain — чистый текст для замера, styled — он же со стилем.
// Если влезает целиком, возвращается styled как есть.
func clipLeft(plain, styled string, limit int) string {
	if limit < 8 {
		return ""
	}
	if lipgloss.Width(plain) <= limit {
		return styled
	}
	runes := []rune(plain)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > limit {
		runes = runes[1:]
	}
	return theme.Faint.Render("…" + string(runes))
}

// progressBar — заполнение блочными символами; тот же визуальный язык, что
// у sparkline в шапке вьюера.
func progressBar(done, total, width int) string {
	if total == 0 {
		return ""
	}
	filled := done * width / total
	return lipgloss.NewStyle().Foreground(theme.Mauve).Render(strings.Repeat("━", filled)) +
		theme.Faint.Render(strings.Repeat("━", maxInt(0, width-filled)))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
