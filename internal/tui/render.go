package tui

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/theme"
	"heroku-console/logfeed"
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
// читает имя. Палитра насыщенная и разнородная — модули должны различаться
// с одного взгляда, не вчитываясь в имя.
var modulePalette = []lipgloss.Color{
	lipgloss.Color("51"), lipgloss.Color("213"), lipgloss.Color("214"),
	lipgloss.Color("118"), lipgloss.Color("63"), lipgloss.Color("201"),
	lipgloss.Color("220"), lipgloss.Color("87"),
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
// Правило темы: маркер уровня несёт самый насыщенный цвет. Модуль подкрашен
// ярко, но стабильно (см. moduleColor), время всегда самое тихое.
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
				b.WriteString(lipgloss.NewStyle().Foreground(theme.Accent).Render("↳ "))
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

// labelSpace — сколько ячеек остаётся под правую подпись в отрезке рамки.
// Повторяет раскладку frameLine: углы, линии-отбивки, левая подпись и хотя
// бы одна ячейка заполнителя. Считать это на глазок нельзя — подпись длиннее
// остатка не обрезается, а раздвигает линию, терминал переносит лишнее на
// новую строку, и разъезжается весь экран до самого низа.
func labelSpace(width int, lc, rc, left string) int {
	lead := lipgloss.Width(lc) + 1 // lc + "─"
	gapL := 0
	if left != "" {
		lead++ // пробел-отбивка после линии
		gapL = 1
	}
	tail := 1 + 2 + lipgloss.Width(rc) // отбивка + " ─" + rc
	return width - lead - lipgloss.Width(left) - gapL - tail - 1
}

// fits выбирает первый вариант подписи, влезающий в space ячеек. Варианты
// передаются парами «чистый текст, он же со стилем»: мерить надо текст без
// ANSI-кодов, а рисовать — со стилем.
func fits(space int, variants ...[2]string) string {
	for _, v := range variants {
		if lipgloss.Width(v[0]) <= space {
			return v[1]
		}
	}
	return ""
}

func (v *Viewer) renderHeader() string {
	title := theme.Title.Render("HEROKU")
	// При видимой панели название уезжает в левый отрезок рамки, а состояние
	// рисуется в правом — там левой подписи уже нет.
	space := labelSpace(v.width, "╭", "╮", "HEROKU")
	if v.sidebarVisible() {
		space = labelSpace(v.logWidth(), "", "╮", "")
	}

	var right string
	switch {
	case v.streamIssue != "":
		// Обрыв слежения важнее состояния процесса: бот может быть жив, а
		// лог при этом не читаться, и молчащий экран должен объяснить, что
		// он молчит не потому, что всё хорошо.
		alarm := lipgloss.NewStyle().Foreground(theme.Red).Bold(true).Render("⚠ лог не читается")
		right = fits(space,
			[2]string{"⚠ лог не читается  " + v.streamIssue, alarm + theme.Meta.Render("  "+v.streamIssue)},
			[2]string{"⚠ лог не читается", alarm},
			[2]string{"⚠", lipgloss.NewStyle().Foreground(theme.Red).Bold(true).Render("⚠")},
		)
	case v.botAlive:
		live := theme.StatusLive.Render("● live")
		right = fits(space,
			[2]string{fmt.Sprintf("● live  %s  ·  %s", orDash(v.version), v.uptime),
				live + theme.Meta.Render(fmt.Sprintf("  %s  ·  %s", orDash(v.version), v.uptime))},
			[2]string{"● live  " + v.uptime, live + theme.Meta.Render("  "+v.uptime)},
			[2]string{"● live", live},
			[2]string{"●", theme.StatusLive.Render("●")},
		)
	default:
		right = fits(space,
			[2]string{"○ не запущен", theme.StatusDown.Render("○ не запущен")},
			[2]string{"○", theme.StatusDown.Render("○")},
		)
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
	//
	// Без панели всё возвращается сюда, и левая часть тоже должна уметь
	// ужиматься: длинный фильтр в одиночку перерастал ширину узкого окна и
	// ломал рамку. Порядок жертв — от самого необязательного: фильтр виден
	// и в самом логе (по нему отобраны строки), pid нужен реже счётчиков,
	// а счётчики проблем уходят последними.
	left := ""
	if !v.sidebarVisible() {
		status, statusPlain := theme.StatusLive.Render("●"), "●"
		pid := fmt.Sprintf(" pid %d", v.botPID)
		if !v.botAlive {
			status, statusPlain = theme.StatusDown.Render("○"), "○"
			pid = " не запущен"
		}
		counts := theme.Meta.Render("  ·  ") + theme.WarnBadge.Render(fmt.Sprintf("⚠ %d", v.warn)) +
			"  " + theme.ErrBadge.Render(fmt.Sprintf("✗ %d", v.err))
		countsPlain := fmt.Sprintf("  ·  ⚠ %d  ✗ %d", v.warn, v.err)
		filter := ""
		filterPlain := ""
		if v.filter != "" {
			filter = theme.Meta.Render("  ·  ") + theme.SearchBar.Render("/"+v.filter)
			filterPlain = "  ·  /" + v.filter
		}

		variants := [][2]string{
			{statusPlain + pid + countsPlain + filterPlain, status + theme.Meta.Render(pid) + counts + filter},
			{statusPlain + pid + countsPlain, status + theme.Meta.Render(pid) + counts},
			{statusPlain + countsPlain, status + counts},
			{statusPlain, status},
		}
		for _, cand := range variants {
			if labelSpace(v.width, "╰", "╯", plainWidthOf(cand[0])) >= 0 {
				left = cand[1]
				break
			}
		}
	}

	// Подсказки укорачиваются от самой необязательной к самой нужной, пока
	// не поместятся: на узком окне лучше показать "q выход", чем поломать
	// рамку переносом. Полный список живёт за "?", поэтому здесь достаточно
	// напомнить о нём и о самом частом.
	//
	// Порог показа попадает в подсказки только когда включён: постоянная
	// надпись "порог: всё" занимала бы место, не сообщая ничего.
	lvl := ""
	if l := levelLabel(v.minLevel); l != "" {
		lvl = "порог: " + l + " · "
	}
	hints := []string{
		lvl + "debug: " + debugState(v.showDebug) + " · ? справка · q выход",
		lvl + "? справка · q выход",
		"? справка · q выход",
		"q выход",
		"",
	}

	// Мерить надо по тому отрезку рамки, в котором подсказки реально
	// рисуются: при видимой панели это колонка лога, а не всё окно, и
	// разница в 21 ячейку выдавливала бы линию за край.
	space := labelSpace(v.width, "╰", "╯", plainWidthOf(left))
	if v.sidebarVisible() {
		space = labelSpace(v.logWidth(), "", "╯", "")
	}
	if space < 0 {
		space = 0
	}
	right := ""
	for _, h := range hints {
		if lipgloss.Width(h) <= space {
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

// overlayBox — модальное окно поверх лога: заголовок, строки «ключ —
// пояснение», подсказка внизу.
//
// Главное здесь — ужиматься. Окно бывает маленьким, а рамка, которая не
// влезла, не обрезается сама: терминал переносит хвост на новую строку, и
// разъезжается всё до самого низа. Поэтому сначала уходит колонка пояснений,
// потом лишние строки, и только в конце стоит жёсткий обрез как страховка.
func (v *Viewer) overlayBox(title string, rows [][2]string, hint string) string {
	const chrome = 6 // рамка и внутренние отступы по горизонтали
	maxW := v.width - 2
	maxH := v.vp.Height

	keyW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > keyW {
			keyW = w
		}
	}
	descW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[1]); w > descW {
			descW = w
		}
	}
	// Пояснения — первое, чем жертвуем: клавиша без пояснения всё ещё
	// напоминает, что она есть, пояснение без клавиши бесполезно.
	showDesc := chrome+2+keyW+3+descW <= maxW
	if avail := maxW - chrome - 2 - keyW - 3; showDesc && avail < descW {
		descW = avail
	}

	// Заголовок, отбивка, подсказка и отбивка перед ней — четыре строки,
	// которые есть всегда; остальное отдаётся списку.
	roomForRows := maxH - 4 - 2 // -2 на верх и низ рамки
	cut := false
	if roomForRows < 1 {
		roomForRows = 1
	}
	if len(rows) > roomForRows {
		rows = rows[:roomForRows]
		cut = true
	}

	var b strings.Builder
	b.WriteString(theme.Title.Render("  "+title) + "\n\n")
	for _, r := range rows {
		line := "  " + lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(pad(r[0], keyW))
		if showDesc {
			line += "   " + theme.Meta.Render(pad(r[1], descW))
		}
		b.WriteString(line + "\n")
	}
	if cut {
		b.WriteString(theme.Faint.Render("  …") + "\n")
	}
	b.WriteString("\n" + theme.Faint.Render("  "+hint))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2).
		Render(b.String())

	// Страховка: что бы ни случилось с расчётами выше, наружу не должно
	// выйти ничего шире и выше окна.
	return lipgloss.NewStyle().MaxWidth(v.width).MaxHeight(maxH).
		Render(lipgloss.Place(v.width, maxH, lipgloss.Center, lipgloss.Center, box))
}

// renderHelp — список клавиш поверх лога. Держать его на экране постоянно
// незачем: подсказки нужны первые пару раз, а место в подвале дорогое.
func (v *Viewer) renderHelp() string {
	return v.overlayBox("Клавиши", [][2]string{
		{"d", "показать или скрыть DEBUG"},
		{"s", "показать или скрыть панель статистики"},
		{"w", "порог показа: всё → warning → error"},
		{"m", "выбрать модуль из панели и отфильтровать"},
		{"/", "поиск: подстрока или re:шаблон, пустой ввод снимает"},
		{"a", "автоперезапуск бота при падении: вкл/выкл"},
		{"n  N", "к следующей / предыдущей проблеме"},
		{"r  R", "к следующему / предыдущему перезапуску"},
		{"↑ ↓  j k", "прокрутка на строку"},
		{"PgUp PgDn", "прокрутка на полэкрана"},
		{"g  G", "в начало / в конец"},
		{"колесо", "прокрутка мышью"},
		{"q", "выход (бот продолжит работать)"},
	}, "любая клавиша — закрыть")
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

// renderModPick — список модулей поверх лога. Порядок и цифры те же, что в
// панели: выбирают глазами там, а нажимают здесь, и расхождение между двумя
// списками читалось бы как ошибка.
func (v *Viewer) renderModPick() string {
	mods := v.modList
	rows := make([][2]string, 0, len(mods))
	for i, m := range mods {
		name := m.name
		if i == v.modCursor {
			name = "▸ " + name
		} else {
			name = "  " + name
		}
		desc := fmt.Sprintf("%d", m.count)
		switch {
		case m.err > 0:
			desc += fmt.Sprintf("   ✗ %d", m.err)
		case m.warn > 0:
			desc += fmt.Sprintf("   ⚠ %d", m.warn)
		}
		rows = append(rows, [2]string{name, desc})
	}
	if len(rows) == 0 {
		rows = [][2]string{{"—", "модулей пока нет"}}
	}
	return v.overlayBox("Модуль", rows, "Enter — отфильтровать · Esc — отмена")
}

// levelLabel — как подписан текущий порог показа.
func levelLabel(l logfeed.Level) string {
	switch l {
	case logfeed.LevelWarning:
		return "warning+"
	case logfeed.LevelError:
		return "error+"
	default:
		return ""
	}
}

// plainWidthOf возвращает строку из пробелов той же видимой ширины, что и
// переданная подпись. Нужна там, где в расчёт раскладки надо отдать только
// ширину: lipgloss.Width считает видимые ячейки и не путается в ANSI-кодах,
// поэтому годится и для стилизованной строки, и для чистой.
func plainWidthOf(styled string) string {
	return strings.Repeat(" ", lipgloss.Width(styled))
}
