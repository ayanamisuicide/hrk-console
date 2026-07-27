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

// cardWidth — внутренняя ширина карточки. Фиксированная, а не по окну:
// меню из четырёх строк, растянутое на 200 колонок, читается хуже — глаз
// проходит всю ширину экрана ради одного слова.
const cardWidth = 46

func (m *Menu) View() string {
	if m.done {
		return ""
	}
	width, height := m.width, m.height
	if width < 40 {
		width = 72
	}
	if height < 10 {
		height = 24
	}

	var b strings.Builder

	// ─── шапка над карточкой ───
	b.WriteString(theme.Title.Render("HEROKU") + "\n")
	if m.botPID != 0 {
		b.WriteString(theme.StatusLive.Render("● запущен") +
			theme.Meta.Render(fmt.Sprintf("   pid %d   ·   %s", m.botPID, m.uptime)))
	} else {
		b.WriteString(theme.StatusDown.Render("○ не запущен"))
	}
	b.WriteString("\n\n")

	// ─── карточка с действиями ───
	// Ширина колонки заголовка считается по самому длинному пункту, а не
	// задаётся числом: иначе при правке текста колонка описаний разъезжается.
	titleW := 0
	for _, it := range menuItems {
		if w := lipgloss.Width(it.title); w > titleW {
			titleW = w
		}
	}

	var card strings.Builder
	for i, it := range menuItems {
		selected := i == m.cursor

		marker, numStyle, titleStyle := "  ", theme.Faint, lipgloss.NewStyle().Foreground(theme.Text)
		descStyle := theme.ItemDesc
		if selected {
			marker = theme.Title.Render("▸ ")
			numStyle = lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
			titleStyle = lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
		}

		row := marker + numStyle.Render(fmt.Sprintf("%d", i+1)) + "  " +
			titleStyle.Render(pad(it.title, titleW)) + "   " +
			descStyle.Render(it.desc)

		if gap := cardWidth - lipgloss.Width(row); gap > 0 {
			row += strings.Repeat(" ", gap)
		}
		if selected {
			// Заливка тянется во всю ширину карточки: оборванная по концу
			// текста полоса читается как артефакт, а не как выделение.
			row = lipgloss.NewStyle().Background(lipgloss.Color("237")).Render(row)
		}
		card.WriteString(row)
		if i < len(menuItems)-1 {
			card.WriteString("\n")
		}
	}

	b.WriteString(lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Overlay).
		Padding(1, 2).
		Render(card.String()))
	b.WriteString("\n\n")
	b.WriteString(theme.Faint.Render("↑↓ выбор  ·  Enter запуск  ·  1–4 сразу  ·  q выход"))

	// Карточка ставится по центру экрана целиком, вместе с шапкой и
	// подсказками: центрируется композиция, а не каждый кусок по отдельности,
	// иначе при изменении размера окна они разъезжаются друг относительно друга.
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().Align(lipgloss.Center).Render(b.String()))
}
