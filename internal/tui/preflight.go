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
	frame   int // кадр анимации спиннера
	done    bool
	Failed  bool // хоть одна проверка не прошла — вызывающий решает, что делать
	Aborted bool // прервано пользователем: выходим, а не идём дальше в меню
	// hold удерживает экран после завершения: при успехе коротко, чтобы
	// глаз успел увидеть результат, при провале — до нажатия клавиши.
	holdUntil time.Time
	started   time.Time

	// progress — заполнение прогресс-бара, догоняющее реальное число готовых
	// проверок. Отдельное поле, потому что проверки завершаются рывками
	// (шесть мгновенных и одна на секунду), а полоса, скачущая рывками,
	// читается как подвисание интерфейса.
	progress float64
}

// Сколько минимум держится каждая проверка на экране. Проверки окружения
// отрабатывают за доли миллисекунды, и без выдержки весь список успевал
// смениться внутри одного кадра — смотреть было не на что.
const minCheckDwell = 260 * time.Millisecond

func NewPreflight(herokuDir, logFile string) *Preflight {
	return &Preflight{checks: preflight.All(herokuDir, logFile), started: time.Now()}
}

type checkDoneMsg struct {
	index  int
	detail string
	status preflight.Status
	took   time.Duration
}
type frameMsg time.Time
type holdOverMsg struct{}

// Частота кадров: полоса догоняет цель по экспоненте, и на 12 кадрах
// движение получалось ступенчатым — видно отдельные кадры, а не движение.
const spinnerFPS = 24

func frameCmd() tea.Cmd {
	return tea.Tick(time.Second/spinnerFPS, func(t time.Time) tea.Msg { return frameMsg(t) })
}

// runCheck выполняет проверку в фоне, чтобы анимация не замирала: go test
// занимает секунды, и без этого спиннер стоял бы колом всё это время.
//
// Быстрая проверка додерживается до minCheckDwell — прогон должен читаться
// как последовательность шагов, а не как список, моргнувший целиком. Время в
// отчёте при этом настоящее, замеряется до выдержки.
func (p *Preflight) runCheck(i int) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		detail, status := p.checks[i].Run()
		took := time.Since(start)
		if rest := minCheckDwell - took; rest > 0 {
			time.Sleep(rest)
		}
		return checkDoneMsg{i, detail, status, took}
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
		// Полоса догоняет цель по экспоненте: быстро трогается с места и
		// мягко останавливается. Линейное движение к цели, которая скачет
		// рывками, выглядит дёрганым.
		target := float64(p.doneCount()) / float64(len(p.checks))
		p.progress += (target - p.progress) * 0.12
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
		// Полосе нужно доехать до конца, и итог надо успеть прочитать —
		// прежние 700 мс уходили целиком на анимацию полосы, а "всё готово"
		// смахивалось раньше, чем взгляд до него доходил.
		p.holdUntil = time.Now().Add(1600 * time.Millisecond)
		return p, nil

	case tea.KeyMsg:
		// При провале ждём подтверждения — иначе сообщение о том, что не так,
		// смахнёт следующим экраном раньше, чем его успеют прочитать.
		if p.done {
			return p, tea.Quit
		}
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			p.Aborted = true
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
	// Полоса на месте при любом состоянии: она же показывает, что прогон
	// закончен целиком. Исчезающий в финале индикатор оставлял бы вопрос,
	// доехал он или экран сменился раньше времени.
	barW := minInt(maxInt(width-24, 16), 40)
	b.WriteString("  " + progressBar(p.progress, barW) +
		theme.Faint.Render(fmt.Sprintf("  %d/%d", p.doneCount(), len(p.checks))) +
		theme.Faint.Render("  ·  "+fmtDuration(time.Since(p.started))) + "\n\n")

	switch {
	case !p.done:
		name := ""
		if p.current < len(p.checks) {
			name = p.checks[p.current].Name
		}
		b.WriteString("  " + theme.Meta.Render(name+"…") + "\n")
	case p.Failed:
		b.WriteString("  " + lipgloss.NewStyle().Foreground(theme.Red).Bold(true).
			Render("не всё в порядке") +
			theme.Faint.Render("  ·  любая клавиша — продолжить всё равно") + "\n")
	default:
		b.WriteString("  " + lipgloss.NewStyle().Foreground(theme.Green).Bold(true).
			Render("всё готово") +
			theme.Faint.Render("  ·  любая клавиша — не ждать") + "\n")
	}
	return b.String()
}

// doneCount — сколько проверок уже отработало.
func (p *Preflight) doneCount() int {
	n := 0
	for i := range p.checks {
		switch p.checks[i].Status {
		case preflight.Passed, preflight.Failed, preflight.Skipped:
			n++
		}
	}
	return n
}

// fmtDuration — секунды с одним знаком: на глаз читается лучше, чем
// "1.4372s" из стандартного форматирования.
func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d мс", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f с", d.Seconds())
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

// progressBar рисует полосу заполнения frac (0..1) шириной width ячеек.
//
// Дробная часть дорисовывается частичным блоком, поэтому полоса движется
// плавно, а не прыжками по целой ячейке: на 28 ячейках и семи проверках
// целочисленный шаг был бы в четыре ячейки — это уже не движение, а мигание.
// Цвет заполненной части — тот же градиент, что у рамок вьюера, так что
// экран проверок читается как часть того же приложения.
func progressBar(frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	eighths := int(frac * float64(width) * 8)
	full := eighths / 8
	rest := eighths % 8

	var b strings.Builder
	b.WriteString(theme.Gradient("█", full, false))
	if full < width && rest > 0 {
		b.WriteString(theme.Faint.Render(string([]rune("▏▎▍▌▋▊▉")[rest-1])))
		full++
	}
	if gap := width - full; gap > 0 {
		b.WriteString(theme.Faint.Render(strings.Repeat("─", gap)))
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
