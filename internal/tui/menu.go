package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/botproc"
	"heroku-console/internal/theme"
)

var menuItems = []struct{ title, desc string }{
	{"Подключиться к боту", "не трогая процесс"},
	{"Перезапустить бота", "стоп и старт заново"},
	{"Два окна", "логи + родная консоль"},
	{"Остановить бота", "просто stop, без окна"},
}

// Menu — стартовый экран выбора режима, в той же рамке и с той же
// типографикой, что и вьюер: это первый экран, и он задаёт впечатление
// обо всём остальном.
type Menu struct {
	bot    *botproc.Manager
	cursor int
	width  int
	height int
	Choice int // 1..4, 0 — ничего не выбрано (вышли по q)
	done   bool

	botPID int
	uptime string
}

func NewMenu(bot *botproc.Manager) *Menu { return &Menu{bot: bot} }

func (m *Menu) Init() tea.Cmd {
	m.refresh()
	return tickCmd()
}

func (m *Menu) refresh() {
	m.botPID = botproc.PID()
	if m.botPID != 0 {
		m.uptime = botproc.Uptime(m.botPID)
	}
}

func (m *Menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.refresh()
		return m, tickCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(menuItems)) % len(menuItems)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(menuItems)
		case "1", "2", "3", "4":
			m.Choice = int(msg.String()[0] - '0')
			m.done = true
			return m, tea.Quit
		case "enter":
			m.Choice = m.cursor + 1
			m.done = true
			return m, tea.Quit
		case "q", "ctrl+c", "esc":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Menu) View() string {
	if m.done {
		return ""
	}
	width := m.width
	if width < 40 {
		width = 72
	}

	var status string
	if m.botPID != 0 {
		status = theme.StatusLive.Render("● запущен") +
			theme.Meta.Render(fmt.Sprintf("  pid %d  ·  %s", m.botPID, m.uptime))
	} else {
		status = theme.StatusDown.Render("○ не запущен")
	}

	var b strings.Builder
	b.WriteString(frameLine(theme.Title.Render("HEROKU"), status, width, "╭", "╮", false))
	b.WriteString("\n\n")

	// Ширина колонки заголовка считается по самому длинному пункту, а не
	// задаётся числом: иначе при правке текста колонка описаний разъезжается.
	titleW := 0
	for _, it := range menuItems {
		if w := lipgloss.Width(it.title); w > titleW {
			titleW = w
		}
	}

	for i, it := range menuItems {
		num := fmt.Sprintf("%d", i+1)
		selected := i == m.cursor

		marker := "  "
		if selected {
			marker = theme.Title.Render("▸ ")
		}
		numStyle := theme.Faint
		titleStyle := lipgloss.NewStyle().Foreground(theme.Text)
		if selected {
			numStyle = lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
			titleStyle = lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
		}

		row := "  " + marker + numStyle.Render(num) + "  " +
			titleStyle.Render(pad(it.title, titleW)) + "   " +
			theme.ItemDesc.Render(it.desc)

		if selected {
			// заливка тянется во всю ширину, иначе полоса обрывается по концу
			// текста и читается как случайный артефакт, а не как выделение
			if gap := width - lipgloss.Width(row); gap > 0 {
				row += strings.Repeat(" ", gap)
			}
			row = lipgloss.NewStyle().Background(lipgloss.Color("237")).Render(row)
		}
		b.WriteString(row + "\n")
	}

	// Подвал прижимаем к низу окна, чтобы меню не висело в воздухе посреди
	// пустого экрана.
	used := 1 + 1 + len(menuItems) + 1
	if gap := m.height - used - 1; gap > 0 {
		b.WriteString(strings.Repeat("\n", gap))
	} else {
		b.WriteString("\n")
	}
	hints := theme.Meta.Render("↑↓ выбор · Enter запуск · 1-4 сразу · q выход")
	b.WriteString(frameLine("", hints, width, "╰", "╯", true))
	return b.String()
}
