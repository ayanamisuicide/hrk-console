package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/theme"
)

// Боковая панель отвечает на вопросы, ради которых иначе пришлось бы читать
// сам лог: кто сейчас шумит, где копятся ошибки, жив ли процесс. Лог отвечает
// на «что именно случилось» — и эти две задачи разведены по разным колонкам,
// чтобы не спорить за одно и то же место.
const (
	sidebarWidth = 20 // вместе с внутренними отступами, без разделителя
	sidebarPadX  = 1
	// Ниже этой общей ширины панель прячется сама: отнимать 21 колонку у
	// сообщения на узком окне — значит порезать текст ради украшений.
	sidebarMinTotal = 84
	// Сколько модулей показывать. Хвост длинного списка не несёт информации:
	// интересны те, кто говорит громче всех.
	sidebarMaxModules = 7
)

func (v *Viewer) sidebarVisible() bool {
	return v.showSidebar && v.width >= sidebarMinTotal
}

// logWidth — ширина колонки лога. Именно она идёт в renderRecord: перенос
// длинных строк должен считаться по реальному месту под сообщение, а не по
// ширине окна.
func (v *Viewer) logWidth() int {
	if v.sidebarVisible() {
		return v.width - sidebarWidth - 1 // -1 на разделитель
	}
	return v.width
}

// sectionTitle — заголовок раздела панели. Разрядка вместо жирности: панель
// должна оставаться фоном для лога, а не спорить с ним за внимание.
func sectionTitle(s string) string {
	return theme.Faint.Render(strings.Join(strings.Split(s, ""), " "))
}

// bar рисует горизонтальный столбик длиной width ячеек. Дробная часть —
// частичным блоком: на коротких столбиках разница между 1 и 2 записями иначе
// теряется в округлении.
func bar(value, peak, width int) string {
	if peak <= 0 || width <= 0 {
		return ""
	}
	eighths := value * width * 8 / peak
	full := eighths / 8
	if full > width {
		full = width
	}
	rest := eighths % 8
	s := strings.Repeat("█", full)
	if full < width && rest > 0 {
		s += string([]rune("▏▎▍▌▋▊▉")[rest-1])
	}
	return s
}

// renderSidebar собирает панель ровно на height строк: верх прижат к шапке,
// блок процесса — к подвалу. Между ними воздух, который и держит панель
// спокойной при любой высоте окна.
func (v *Viewer) renderSidebar(height int) string {
	if height <= 0 {
		return ""
	}
	inner := sidebarWidth - sidebarPadX*2

	// ─── процесс, прижат к низу ───
	var bottom []string
	if v.botAlive {
		bottom = append(bottom,
			theme.StatusLive.Render("● live"),
			theme.Meta.Render(fmt.Sprintf("pid %d", v.botPID)),
			theme.Meta.Render(v.uptime),
		)
	} else {
		bottom = append(bottom, theme.StatusDown.Render("○ не запущен"))
	}

	// ─── проблемы и фильтр: место под них резервируется первым ───
	fixed := []string{
		sectionTitle("ПРОБЛЕМЫ"),
		theme.WarnBadge.Render(fmt.Sprintf("⚠ %-4d", v.warn)) + theme.ErrBadge.Render(fmt.Sprintf("✗ %d", v.err)),
	}
	if v.filter != "" {
		fixed = append(fixed, "", sectionTitle("ФИЛЬТР"), theme.SearchBar.Render(pad("/"+v.filter, inner)))
	}

	// Остаток высоты делится между графиком и списком модулей — они и режутся
	// первыми. Счётчики проблем и состояние процесса не режутся никогда: это
	// ответ на «всё ли плохо», ради которого панель и появилась.
	budget := height - 1 - len(fixed) - len(bottom) // -1 на отбивку сверху
	var top []string
	top = append(top, "")

	// ─── модули ───
	stats := v.scr.moduleStats()
	const activityCost = 3 // заголовок + график + отбивка
	modBudget := budget
	if len(v.activity) > 0 && modBudget > activityCost {
		modBudget -= activityCost
	}
	if n := modBudget - 2; len(stats) > 0 && n >= 1 { // -2 на заголовок и отбивку
		top = append(top, sectionTitle("МОДУЛИ"))
		peak := stats[0].count
		shown := stats
		if len(shown) > minInt(sidebarMaxModules, n) {
			shown = shown[:minInt(sidebarMaxModules, n)]
		}
		const (
			barW   = 5
			countW = 4
		)
		nameW := inner - barW - countW - 1
		for _, m := range shown {
			// Столбик красится по худшему, что было у модуля: так видно не
			// только кто шумит, но и у кого из них плохо.
			col := theme.Overlay
			switch {
			case m.err > 0:
				col = theme.Red
			case m.warn > 0:
				col = theme.Yellow
			}
			b := bar(m.count, peak, barW)
			line := lipgloss.NewStyle().Foreground(moduleColor(m.name)).Render(pad(m.name, nameW)) +
				" " + lipgloss.NewStyle().Foreground(col).Render(pad(b, barW)) +
				theme.Meta.Render(padLeft(fmt.Sprint(m.count), countW))
			top = append(top, line)
		}
		top = append(top, "")
	}

	// ─── поток ───
	// Заголовок короткий не случайно: разрядка растягивает слово вдвое, и
	// "АКТИВНОСТЬ" не влезала в ширину панели.
	if len(top)+activityCost+len(fixed)+len(bottom) <= height {
		if spark := theme.Sparkline(tailInts(v.activity, inner)); spark != "" {
			top = append(top, sectionTitle("ПОТОК"), spark, "")
		}
	}

	top = append(top, fixed...)

	// Совсем низкое окно: сначала уходят подробности процесса (аптайм, pid),
	// потом отбивка и остатки верхних разделов. Последними на экране
	// остаются "● live" и счётчики проблем — минимум, ради которого стоит
	// держать колонку.
	for len(top)+len(bottom) > height && len(bottom) > 1 {
		bottom = bottom[:len(bottom)-1]
	}
	for len(top)+len(bottom) > height && len(top) > len(fixed) {
		top = top[1:]
	}

	lines := top
	if gap := height - len(top) - len(bottom); gap > 0 {
		lines = append(lines, make([]string, gap)...)
	}
	lines = append(lines, bottom...)
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}

	padX := strings.Repeat(" ", sidebarPadX)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = padX + pad(l, inner) + padX
	}
	return strings.Join(out, "\n")
}

// sidebarDivider — вертикальная линия между панелью и логом на всю высоту.
func sidebarDivider(height int) string {
	line := theme.Rule.Render("│")
	rows := make([]string, height)
	for i := range rows {
		rows[i] = line
	}
	return strings.Join(rows, "\n")
}

// padLeft прижимает строку к правому краю поля: числа читаются колонкой
// только выровненные по правому краю.
func padLeft(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// tailInts берёт последние n значений — sparkline рисуется по ширине панели,
// а история активности длиннее.
func tailInts(xs []int, n int) []int {
	if n <= 0 {
		return nil
	}
	if len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
