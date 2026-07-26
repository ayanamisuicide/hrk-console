package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"heroku-console/internal/botproc"
	"heroku-console/internal/theme"
)

var menuItems = []struct{ title, desc string }{
	{"Подключиться к боту", "не трогая процесс"},
	{"Перезапустить бота", "стоп и старт заново"},
	{"Два окна", "логи + родная консоль"},
	{"Остановить бота", "просто stop, без окна"},
}

// Menu — стартовый экран выбора режима. Один тонкий акцент вместо рамки
// на каждый пункт: выбранная строка подсвечивается заливкой, а не своей
// собственной коробкой.
type Menu struct {
	bot      *botproc.Manager
	cursor   int
	width    int
	Choice   int // 1..4, 0 — ничего не выбрано (вышли по q)
	done     bool
}

func NewMenu(bot *botproc.Manager) *Menu { return &Menu{bot: bot} }

func (m *Menu) Init() tea.Cmd { return nil }

func (m *Menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
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
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(theme.Title.Render("HEROKU USERBOT"))
	b.WriteString("\n  ")
	if botproc.Alive() {
		b.WriteString(theme.StatusLive.Render(fmt.Sprintf("● бот запущен · pid %d", botproc.PID())))
	} else {
		b.WriteString(theme.StatusDown.Render("○ бот не запущен"))
	}
	b.WriteString("\n\n")

	for i, it := range menuItems {
		num := fmt.Sprintf("%d", i+1)
		line := fmt.Sprintf("  %s  %-22s %s", num, it.title, it.desc)
		if i == m.cursor {
			b.WriteString(theme.SelectedItem.Render(line))
		} else {
			b.WriteString(theme.NormalItem.Render("  "+num+"  "+it.title) + "  " + theme.ItemDesc.Render(it.desc))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n  ")
	b.WriteString(theme.Meta.Render("↑↓ выбор  ·  Enter  ·  1-4 сразу  ·  q выход"))
	b.WriteString("\n")
	return b.String()
}
