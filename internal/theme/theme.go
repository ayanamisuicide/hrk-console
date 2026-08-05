// Package theme задаёт единый визуальный язык приложения. Один источник
// цвета и стилей — перекрашивать проект нужно только здесь.
//
// Сознательно без рамок на каждой панели: современный TUI-стиль (Bubble
// Tea / Lip Gloss практика 2026 года) избегает "коробки на каждый виджет" —
// один внешний акцент сверху, дальше разделение цветом и отступом, а не
// линиями. Насыщенный цвет несёт только маркер уровня лога, весь остальной
// текст — приглушённый или нейтральный, чтобы глазу было за что зацепиться
// именно на уровне записи, а не на декоре вокруг.
package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Палитра в духе btop/htop — насыщенная, контрастная, с градиентными
// meter-барами вместо плоской заливки: цвет несёт информацию о величине, а
// не только служит украшением. Подобрана так, чтобы читалось и на
// true-color, и на 256-цветных терминалах (lipgloss сам подбирает ближайший
// цвет).
var (
	Accent   = lipgloss.Color("51")  // акцент бренда — заголовок, рамка, поиск
	Text     = lipgloss.Color("255") // основной текст сообщения
	Subtext  = lipgloss.Color("250") // время, модуль — приглушены, но не тускло
	Overlay  = lipgloss.Color("240") // самое тихое: линии, декор
	Green    = lipgloss.Color("82")  // info / live — сочный, не болотный
	Yellow   = lipgloss.Color("220") // warning
	Red      = lipgloss.Color("196") // error — чистый красный
	Crit     = lipgloss.Color("201") // critical — маджента, чтобы не путался с error на глаз
	DebugDim = lipgloss.Color("238") // debug — почти невидим
)

var (
	Title = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	Rule  = lipgloss.NewStyle().Foreground(Overlay)
	Meta  = lipgloss.NewStyle().Foreground(Subtext)
	Faint = lipgloss.NewStyle().Foreground(Overlay)

	StatusLive = lipgloss.NewStyle().Foreground(Green).Bold(true)
	StatusDown = lipgloss.NewStyle().Foreground(Red)

	WarnBadge = lipgloss.NewStyle().Foreground(Yellow)
	ErrBadge  = lipgloss.NewStyle().Foreground(Red).Bold(true)

	SelectedItem = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(Accent).Bold(true)
	NormalItem   = lipgloss.NewStyle().Foreground(Text)
	ItemDesc     = lipgloss.NewStyle().Foreground(Subtext)

	SearchBar = lipgloss.NewStyle().Foreground(Accent).Bold(true)
)

// LevelStyle — бейдж уровня и цвет сообщения.
//
// Уровень показывается бейджем с заливкой, а не одиночным значком: залитый
// прямоугольник читается периферийным зрением, и ошибка в потоке видна, не
// вчитываясь. Исключение — DEBUG: он остаётся тихим, без фона, иначе шум
// начинает спорить за внимание с тем, ради чего лог открывают.
type LevelStyle struct {
	Label string // ровно 3 буквы — колонка бейджа не должна прыгать по ширине
	Glyph string
	Badge lipgloss.Style
	Text  lipgloss.Style
}

// Тёмный текст на цветной заливке — контрастнее, чем цветной на тёмном.
func badge(bg lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color("235")).Bold(true).Padding(0, 1)
}

var (
	styleDebug = LevelStyle{"DBG", "·",
		lipgloss.NewStyle().Foreground(DebugDim).Padding(0, 1),
		lipgloss.NewStyle().Foreground(Overlay)}
	styleInfo = LevelStyle{"INF", "●",
		lipgloss.NewStyle().Foreground(Green).Padding(0, 1),
		lipgloss.NewStyle().Foreground(Text)}
	styleWarn = LevelStyle{"WRN", "▲",
		badge(Yellow),
		lipgloss.NewStyle().Foreground(Yellow)}
	styleErr = LevelStyle{"ERR", "✗",
		badge(Red),
		lipgloss.NewStyle().Foreground(Red)}
	styleCrit = LevelStyle{"CRT", "✖",
		lipgloss.NewStyle().Background(Crit).Foreground(lipgloss.Color("231")).Bold(true).Padding(0, 1),
		lipgloss.NewStyle().Foreground(Crit).Bold(true)}
)

// Zebra — фон каждой второй записи. Разница в один шаг яркости: полосы не
// должны читаться как отдельный элемент, их задача — только удержать глаз
// на строке при переводе взгляда через всю ширину окна.
var Zebra = lipgloss.NewStyle().Background(lipgloss.Color("236"))

// ─── градиент рамки ───────────────────────────────────────────────────────
// Ровная линия одного цвета выглядит чертой, проведённой по линейке.
// Перетекание от акцента к фону даёт рамке глубину, при этом остаётся
// достаточно тихим, чтобы не спорить с содержимым.
var (
	gradFrom, _ = colorful.Hex("#00d7ff") // акцент — у названия
	gradTo, _   = colorful.Hex("#3b3f51") // почти фон — к дальнему краю
)

// Gradient рисует строку из символа ch длиной n с переходом цвета.
// reverse разворачивает направление: у нижней рамки насыщенный конец должен
// быть там же, где у верхней, иначе окно выглядит перекошенным.
func Gradient(ch string, n int, reverse bool) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		t := float64(i) / float64(maxInt(n-1, 1))
		if reverse {
			t = 1 - t
		}
		c := gradFrom.BlendLab(gradTo, t).Clamped()
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Render(ch))
	}
	return b.String()
}

// ─── meter-бар ─────────────────────────────────────────────────────────────
// Фирменный элемент btop/htop: столбик закрашен не одним цветом, а
// градиентом по своей заполненной части — от зелёного через жёлтый к
// красному. Величина видна не только длиной столбика, но и его "температурой",
// считываемой периферийным зрением ещё быстрее.
var (
	meterLow, _  = colorful.Hex("#3ee06b") // заполнение только началось
	meterMid, _  = colorful.Hex("#f5d130") // уже заметно
	meterHigh, _ = colorful.Hex("#ff3b3b") // почти до края
)

func meterColor(t float64) colorful.Color {
	if t < 0.5 {
		return meterLow.BlendLab(meterMid, t*2).Clamped()
	}
	return meterMid.BlendLab(meterHigh, (t-0.5)*2).Clamped()
}

// MeterBar рисует столбик шириной width ячеек, закрашенный на долю
// value/peak. Незакрашенный остаток — тихая заливка, не пустота: так видно
// границу столбика даже при нулевом значении.
func MeterBar(value, peak, width int) string {
	if width <= 0 {
		return ""
	}
	if peak <= 0 {
		return Faint.Render(strings.Repeat("░", width))
	}
	eighths := value * width * 8 / peak
	full := eighths / 8
	if full > width {
		full = width
	}
	rest := eighths % 8

	var b strings.Builder
	cellColor := func(i int) string {
		t := float64(i) / float64(maxInt(width-1, 1))
		return lipgloss.NewStyle().Foreground(lipgloss.Color(meterColor(t).Hex())).Render("█")
	}
	for i := 0; i < full; i++ {
		b.WriteString(cellColor(i))
	}
	if full < width && rest > 0 {
		partial := string([]rune("▏▎▍▌▋▊▉")[rest-1])
		t := float64(full) / float64(maxInt(width-1, 1))
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(meterColor(t).Hex())).Render(partial))
		full++
	}
	if full < width {
		b.WriteString(Faint.Render(strings.Repeat("░", width-full)))
	}
	return b.String()
}

// ─── sparkline активности ─────────────────────────────────────────────────
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Sparkline рисует счётчики блочными символами: видно, лог сейчас молчит
// или захлёбывается, без чтения самих строк. Цвет — по величине столбика,
// так всплеск заметен даже боковым зрением.
func Sparkline(counts []int) string {
	if len(counts) == 0 {
		return ""
	}
	peak := 0
	for _, c := range counts {
		if c > peak {
			peak = c
		}
	}
	var b strings.Builder
	for _, c := range counts {
		if c == 0 {
			b.WriteString(Faint.Render("▁"))
			continue
		}
		idx := 0
		if peak > 1 {
			idx = (c - 1) * (len(sparkRunes) - 1) / (peak - 1)
		}
		col := Green
		switch {
		case idx >= len(sparkRunes)-2:
			col = Yellow
		case idx >= len(sparkRunes)/2:
			col = Accent
		}
		b.WriteString(lipgloss.NewStyle().Foreground(col).Render(string(sparkRunes[idx])))
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ForLevel возвращает стиль для уровня 0..4 (logfeed.Level).
func ForLevel(level int) LevelStyle {
	switch level {
	case 0:
		return styleDebug
	case 2:
		return styleWarn
	case 3:
		return styleErr
	case 4:
		return styleCrit
	default:
		return styleInfo
	}
}
