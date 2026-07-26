package tui

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/logfeed"
	"heroku-console/internal/theme"
)

const (
	timeW = 8
	modW  = 14
	// Отступ, с которого начинается колонка сообщения. Под него же
	// выравниваются перенесённые хвосты длинных строк.
	gutter = timeW + 2 + 1 + 2 + modW + 2
)

// wrapText переносит текст по словам под заданную ширину. Ширина считается
// в видимых ячейках (lipgloss.Width), а не в байтах: len() на кириллице
// врёт вдвое, из-за чего строки обрезались по краю окна.
func wrapText(text string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
		// одно слово длиннее строки (длинный урл, хеш) — режем жёстко
		for lipgloss.Width(line) > width {
			runes := []rune(line)
			cut := len(runes)
			for lipgloss.Width(string(runes[:cut])) > width && cut > 1 {
				cut--
			}
			out = append(out, string(runes[:cut]))
			line = string(runes[cut:])
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// moduleColor даёт модулю стабильный цвет: один и тот же модуль всегда
// одного оттенка, так что взгляд цепляется за "кто написал" быстрее, чем
// читает имя. Палитра приглушённая — колонка модуля не должна спорить с
// маркером уровня за внимание.
var modulePalette = []lipgloss.Color{
	lipgloss.Color("109"), lipgloss.Color("108"), lipgloss.Color("144"),
	lipgloss.Color("139"), lipgloss.Color("110"), lipgloss.Color("143"),
	lipgloss.Color("175"), lipgloss.Color("073"),
}

func moduleColor(name string) lipgloss.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return modulePalette[int(h.Sum32())%len(modulePalette)]
}

// renderRecord превращает разобранную запись лога в колонки:
//
//	00:12:10  ●  root            🪐 Heroku 2.2.2 started
//
// Правило темы: насыщенный цвет несёт маркер уровня. Модуль подкрашен
// приглушённо и стабильно, время всегда самое тихое.
func renderRecord(rec *logfeed.Record, width int) string {
	style := theme.ForLevel(int(rec.Level))
	msgWidth := width - gutter
	if msgWidth < 20 {
		msgWidth = 20
	}

	var b strings.Builder
	first := true
	for i, line := range rec.Lines {
		hard := i < len(rec.Hard) && rec.Hard[i]

		for j, wrapped := range wrapText(line, msgWidth) {
			if !first {
				b.WriteByte('\n')
			}
			switch {
			case first && rec.Time != "":
				// заголовок записи: время, маркер, модуль, текст
				b.WriteString(theme.Meta.Render(pad(rec.Time, timeW)))
				b.WriteString("  ")
				b.WriteString(style.Marker.Render(style.Glyph))
				b.WriteString("  ")
				b.WriteString(lipgloss.NewStyle().Foreground(moduleColor(rec.Module)).Render(pad(rec.Module, modW)))
				b.WriteString("  ")
				b.WriteString(style.Text.Render(wrapped))
			case hard && j == 0:
				// физически новая строка внутри записи (трейсбек, дамп)
				b.WriteString(strings.Repeat(" ", gutter-2))
				b.WriteString(lipgloss.NewStyle().Foreground(theme.Mauve).Render("↳ "))
				b.WriteString(style.Text.Render(wrapped))
			default:
				// мягкий перенос — выравниваем под колонку сообщения
				b.WriteString(strings.Repeat(" ", gutter))
				b.WriteString(style.Text.Render(wrapped))
			}
			first = false
		}
	}
	return b.String()
}

// pad дополняет строку пробелами до ширины в видимых ячейках.
func pad(s string, w int) string {
	n := w - lipgloss.Width(s)
	if n < 0 {
		// не влезает — обрезаем по рунам с многоточием
		runes := []rune(s)
		for len(runes) > 1 && lipgloss.Width(string(runes))+1 > w {
			runes = runes[:len(runes)-1]
		}
		return string(runes) + "…"
	}
	return s + strings.Repeat(" ", n)
}

// ─── рамка окна ───────────────────────────────────────────────────────────
// Лог живёт внутри рамки: она держит границы, и по ней сразу видно, где
// кончается вывод, а где начинается статус. Подписи вшиты прямо в линии
// рамки — верх несёт название и состояние бота, низ счётчики и подсказки.

// frameLine собирает линию рамки вида "╭─ левое ──────── правое ─╮".
// Все длины считаются в видимых ячейках: с кириллицей и эмодзи байтовая
// длина не годится.
//
// Раскладка: "╭─ " + левое + " " + заполнитель + " " + правое + " ─╮",
// то есть на неизменяемые края уходит ровно 8 ячеек (3 + 1 + 1 + 3).
func frameLine(left, right string, width int, lc, rc string) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	fill := width - 8 - lw - rw
	if fill < 1 {
		fill = 1
	}
	rule := theme.Rule.Render(strings.Repeat("─", fill))
	return theme.Rule.Render(lc+"─ ") + left + " " + rule + " " + right + theme.Rule.Render(" ─"+rc)
}

func (v *Viewer) renderHeader() string {
	left := theme.Title.Render("HEROKU")
	var right string
	if v.botAlive {
		right = theme.StatusLive.Render("● live") +
			theme.Meta.Render(fmt.Sprintf("  %s  ·  %s", orDash(v.version), v.uptime))
	} else {
		right = theme.StatusDown.Render("○ не запущен")
	}
	return frameLine(left, right, v.width, "╭", "╮")
}

func (v *Viewer) renderFooter() string {
	if v.searching {
		// строка поиска занимает всю нижнюю рамку — видно, что ввод активен
		return theme.Rule.Render("╰─ ") + v.search.View()
	}

	var left string
	if v.botAlive {
		left = theme.StatusLive.Render("●") + theme.Meta.Render(fmt.Sprintf(" pid %d", v.botPID))
	} else {
		left = theme.StatusDown.Render("○") + theme.Meta.Render(" не запущен")
	}
	left += theme.Meta.Render("  ·  ") + theme.WarnBadge.Render(fmt.Sprintf("⚠ %d", v.warn)) +
		"  " + theme.ErrBadge.Render(fmt.Sprintf("✗ %d", v.err))
	if v.filter != "" {
		left += theme.Meta.Render("  ·  ") + theme.SearchBar.Render("/"+v.filter)
	}

	// Подсказки укорачиваются от самой необязательной к самой нужной, пока
	// не поместятся: на узком окне лучше показать "q — выход", чем поломать
	// рамку переносом.
	hints := []string{
		"/ поиск · d debug: " + debugState(v.showDebug) + " · q выход",
		"d debug: " + debugState(v.showDebug) + " · q выход",
		"q выход",
		"",
	}
	right := ""
	for _, h := range hints {
		if v.width-6-lipgloss.Width(left)-lipgloss.Width(h) >= 1 {
			right = theme.Meta.Render(h)
			break
		}
	}
	return frameLine(left, right, v.width, "╰", "╯")
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
