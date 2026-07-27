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
	timeW  = 8
	badgeW = 5 // " DBG " — три буквы плюс padding с двух сторон
	modW   = 14
	// Отступ, с которого начинается колонка сообщения. Под него же
	// выравниваются перенесённые хвосты длинных строк.
	gutter = 1 + timeW + 1 + badgeW + 2 + modW + 2
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
// zebra — индекс записи: чётные подкрашиваются фоном. Передаётся снаружи,
// потому что запись может занять несколько строк экрана, и полоса должна
// накрывать её целиком, а не чередоваться построчно.
func renderRecord(rec *logfeed.Record, width int, zebra bool) string {
	style := theme.ForLevel(int(rec.Level))
	msgWidth := width - gutter - 1
	if msgWidth < 20 {
		msgWidth = 20
	}

	// Фон должен тянуться во всю ширину окна, иначе полоса обрывается по
	// концу текста и выглядит грязью, а не полосой. tail прижимается к
	// правому краю той же пустотой, что тянет фон.
	row := func(s, tail string) string {
		if gap := width - lipgloss.Width(s) - lipgloss.Width(tail); gap > 0 {
			s += strings.Repeat(" ", gap)
		}
		s += tail
		if zebra {
			return theme.Zebra.Render(s)
		}
		return s
	}

	// Счётчик повторов у правого края, а не после текста: сообщения разной
	// длины, и по краю счётчики выстраиваются колонкой, которую видно, не
	// читая строк. Цвет — по уровню записи: десять ошибок подряд должны
	// выглядеть тревожнее десяти info.
	countTail := ""
	if rec.Count > 1 {
		countTail = style.Badge.Render(fmt.Sprintf(" ×%d ", rec.Count))
		// Место под счётчик отнимается у переноса: иначе сообщение ровно во
		// всю ширину плюс счётчик не влезли бы в строку, и viewport перенёс
		// бы хвост сам — уже без наших отступов под колонку.
		if w := msgWidth - lipgloss.Width(countTail); w >= 20 {
			msgWidth = w
		}
	}

	var out []string
	first := true
	for i, line := range rec.Lines {
		hard := i < len(rec.Hard) && rec.Hard[i]

		for j, wrapped := range wrapText(line, msgWidth) {
			var b strings.Builder
			switch {
			case first && rec.Time != "":
				b.WriteString(" ")
				b.WriteString(theme.Meta.Render(pad(rec.Time, timeW)))
				b.WriteString(" ")
				b.WriteString(style.Badge.Render(style.Label))
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
			tail := ""
			if len(out) == 0 {
				tail = countTail
			}
			out = append(out, row(b.String(), tail))
			first = false
		}
	}
	return strings.Join(out, "\n")
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
func frameLine(left, right string, width int, lc, rc string, reverse bool) string {
	// Пустую подпись пропускаем вместе с её пробелом-отбивкой, иначе в углу
	// повисает разрыв, читающийся как дефект рамки.
	lead, tail := lc+"─ ", " ─"+rc
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	gapL, gapR := " ", " "
	if lw == 0 {
		lead, gapL = lc+"─", ""
	}
	if rw == 0 {
		tail, gapR = "─"+rc, ""
	}
	fill := width - lipgloss.Width(lead) - lipgloss.Width(tail) - lw - rw - len(gapL) - len(gapR)
	if fill < 1 {
		fill = 1
	}
	return theme.Rule.Render(lead) + left + gapL +
		theme.Gradient("─", fill, reverse) +
		gapR + right + theme.Rule.Render(tail)
}

func (v *Viewer) renderHeader() string {
	title := theme.Title.Render("HEROKU")
	var right string
	if v.botAlive {
		right = theme.StatusLive.Render("● live") +
			theme.Meta.Render(fmt.Sprintf("  %s  ·  %s", orDash(v.version), v.uptime))
	} else {
		right = theme.StatusDown.Render("○ не запущен")
	}
	if !v.sidebarVisible() {
		return frameLine(title, right, v.width, "╭", "╮", false)
	}
	// Рамка разветвляется там же, где стоит разделитель колонок: иначе
	// вертикальная линия панели упирается в сплошной верх и читается как
	// случайная черта поверх окна, а не как граница колонки.
	return frameLine(title, "", sidebarWidth+1, "╭", "┬", false) +
		frameLine("", right, v.logWidth(), "", "╮", false)
}

func (v *Viewer) renderFooter() string {
	if v.searching {
		// строка поиска занимает всю нижнюю рамку — видно, что ввод активен
		return theme.Rule.Render("╰─ ") + v.search.View()
	}

	// Состояние процесса, счётчики и фильтр переехали в панель — в подвале
	// они дублировались бы слово в слово. Остаётся то, чего в панели нет:
	// напоминание о клавишах.
	var left string
	if !v.sidebarVisible() {
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
	}

	// Подсказки укорачиваются от самой необязательной к самой нужной, пока
	// не поместятся: на узком окне лучше показать "q выход", чем поломать
	// рамку переносом. Полный список живёт за "?", поэтому здесь достаточно
	// напомнить о нём и о самом частом.
	hints := []string{
		"debug: " + debugState(v.showDebug) + " · ? справка · q выход",
		"? справка · q выход",
		"q выход",
		"",
	}
	right := ""
	for _, h := range hints {
		if v.width-8-lipgloss.Width(left)-lipgloss.Width(h) >= 1 {
			right = theme.Meta.Render(h)
			break
		}
	}
	if !v.sidebarVisible() {
		return frameLine(left, right, v.width, "╰", "╯", true)
	}
	return frameLine("", "", sidebarWidth+1, "╰", "┴", true) +
		frameLine("", right, v.logWidth(), "", "╯", true)
}

// renderHelp — список клавиш поверх лога. Держать его на экране постоянно
// незачем: подсказки нужны первые пару раз, а место в подвале дорогое.
func (v *Viewer) renderHelp() string {
	rows := [][2]string{
		{"d", "показать или скрыть DEBUG"},
		{"s", "показать или скрыть панель статистики"},
		{"/", "поиск по подстроке, пустой ввод снимает"},
		{"n  N", "к следующей / предыдущей проблеме (warning и выше)"},
		{"↑ ↓  j k", "прокрутка на строку"},
		{"PgUp PgDn", "прокрутка на полэкрана"},
		{"g  G", "в начало / в конец"},
		{"колесо", "прокрутка мышью"},
		{"q", "выход (бот продолжит работать)"},
	}

	keyW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > keyW {
			keyW = w
		}
	}

	var b strings.Builder
	b.WriteString(theme.Title.Render("  Клавиши") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " +
			lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true).Render(pad(r[0], keyW)) +
			"   " + theme.Meta.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + theme.Faint.Render("  любая клавиша — закрыть"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Mauve).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(v.width, v.vp.Height, lipgloss.Center, lipgloss.Center, box)
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
