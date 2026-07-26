package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/logfeed"
	"heroku-console/internal/theme"
)

const (
	timeW = 8
	modW  = 14
)

// renderRecord превращает разобранную запись лога в колонки:
//
//	00:12:10  ●  root            🪐 Heroku 2.2.2 started
//
// Насыщенный цвет несёт только маркер уровня — единственный принцип темы,
// который стоит помнить: время и модуль приглушены, сообщение читаемо.
func renderRecord(rec *logfeed.Record) string {
	style := theme.ForLevel(int(rec.Level))
	gutter := timeW + 2 + 1 + 2 + modW + 2

	var b strings.Builder
	for i, line := range rec.Lines {
		switch {
		case i == 0 && rec.Time != "":
			timePad := timeW - len(rec.Time)
			if timePad < 0 {
				timePad = 0
			}
			modPad := modW - len(rec.Module)
			if modPad < 0 {
				modPad = 0
			}
			b.WriteString(theme.Meta.Render(rec.Time + strings.Repeat(" ", timePad)))
			b.WriteString("  ")
			b.WriteString(style.Marker.Render(style.Glyph))
			b.WriteString("  ")
			b.WriteString(theme.Meta.Render(rec.Module + strings.Repeat(" ", modPad)))
			b.WriteString("  ")
			b.WriteString(style.Text.Render(line))
		case rec.Hard[i]:
			// физически новая строка внутри записи (трейсбек, дамп)
			b.WriteString("\n")
			b.WriteString(strings.Repeat(" ", gutter-2))
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Mauve).Render("↳ "))
			b.WriteString(style.Text.Render(line))
		default:
			b.WriteString("\n")
			b.WriteString(strings.Repeat(" ", gutter))
			b.WriteString(style.Text.Render(line))
		}
	}
	return b.String()
}

// renderHeader — одна тонкая строка сверху вместо тяжёлой рамки: название,
// статус бота, версия и аптайм. Один акцентный цвет, без коробки — по духу
// современных Bubble Tea/Lip Gloss интерфейсов, где рамка используется
// экономно, а не на каждую панель.
func (v *Viewer) renderHeader() string {
	left := theme.Title.Render("HEROKU")
	var status string
	if v.botAlive {
		status = theme.StatusLive.Render("● live") + theme.Meta.Render(fmt.Sprintf("  ·  %s  ·  %s", orDash(v.version), v.uptime))
	} else {
		status = theme.StatusDown.Render("○ не запущен")
	}
	rule := theme.Rule.Render(strings.Repeat("─", max(1, v.width-lipgloss.Width(left)-lipgloss.Width(status)-4)))
	return "  " + left + "  " + rule + "  " + status + "  "
}

// renderFooter — статус-строка снизу: pid, счётчики, активный фильтр,
// подсказки по клавишам. Подсказки при нехватке места сокращаются от самой
// необязательной к самой нужной — та же идея, что fit_right в bash-версии,
// но здесь это нужно даже реже: viewport сам переносит длинные строки лога,
// а вот эту статусную строку сокращать всё равно приходится вручную.
func (v *Viewer) renderFooter() string {
	if v.searching {
		return "  " + v.search.View()
	}

	var left strings.Builder
	if v.botAlive {
		left.WriteString(theme.StatusLive.Render("●") + fmt.Sprintf(" pid %d", v.botPID))
	} else {
		left.WriteString(theme.StatusDown.Render("○") + " не запущен")
	}
	left.WriteString(theme.Meta.Render(fmt.Sprintf("  ·  ⚠ %d  ✗ %d", v.warn, v.err)))
	if v.filter != "" {
		left.WriteString("  " + theme.SearchBar.Render("/"+v.filter))
	}

	hints := []string{
		"/ — поиск · d — debug: " + debugState(v.showDebug) + " · q — выход",
		"d — debug: " + debugState(v.showDebug) + " · q — выход",
		"q — выход",
	}
	leftW := lipgloss.Width(left.String())
	var right string
	for _, h := range hints {
		if v.width-4-leftW-len(h) >= 1 {
			right = h
			break
		}
	}

	fill := v.width - 4 - leftW - len(right)
	if fill < 1 {
		fill = 1
	}
	return "  " + left.String() + "  " + theme.Rule.Render(strings.Repeat("─", fill)) + "  " + theme.Meta.Render(right) + "  "
}

func debugState(on bool) string {
	if on {
		return "виден"
	}
	return "скрыт"
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
