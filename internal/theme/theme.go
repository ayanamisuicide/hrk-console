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

import "github.com/charmbracelet/lipgloss"

// Палитра в духе Catppuccin Mocha — тёмный, приглушённый, с одним ярким
// акцентом на семейство. Подобрана так, чтобы читалось и на true-color,
// и на 256-цветных терминалах (lipgloss сам подбирает ближайший цвет).
var (
	Mauve    = lipgloss.Color("183") // акцент бренда — заголовок, рамка
	Text     = lipgloss.Color("252") // основной текст сообщения
	Subtext  = lipgloss.Color("245") // время, модуль — приглушены
	Overlay  = lipgloss.Color("240") // самое тихое: линии, декор
	Green    = lipgloss.Color("108") // info / live
	Yellow   = lipgloss.Color("221") // warning
	Red      = lipgloss.Color("203") // error
	Crit     = lipgloss.Color("196") // critical
	DebugDim = lipgloss.Color("238") // debug — почти невидим
)

var (
	Title = lipgloss.NewStyle().Bold(true).Foreground(Mauve)
	Rule  = lipgloss.NewStyle().Foreground(Overlay)
	Meta  = lipgloss.NewStyle().Foreground(Subtext)
	Faint = lipgloss.NewStyle().Foreground(Overlay)

	StatusLive = lipgloss.NewStyle().Foreground(Green).Bold(true)
	StatusDown = lipgloss.NewStyle().Foreground(Red)

	WarnBadge = lipgloss.NewStyle().Foreground(Yellow)
	ErrBadge  = lipgloss.NewStyle().Foreground(Red).Bold(true)

	SelectedItem = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(Mauve).Bold(true)
	NormalItem   = lipgloss.NewStyle().Foreground(Text)
	ItemDesc     = lipgloss.NewStyle().Foreground(Subtext)

	SearchBar = lipgloss.NewStyle().Foreground(Mauve).Bold(true)
)

// LevelStyle — маркер и цвет сообщения для уровня лога.
type LevelStyle struct {
	Glyph string
	Marker lipgloss.Style
	Text   lipgloss.Style
}

var (
	styleDebug = LevelStyle{"·", lipgloss.NewStyle().Foreground(DebugDim), lipgloss.NewStyle().Foreground(Overlay)}
	styleInfo  = LevelStyle{"●", lipgloss.NewStyle().Foreground(Green), lipgloss.NewStyle().Foreground(Text)}
	styleWarn  = LevelStyle{"▲", lipgloss.NewStyle().Foreground(Yellow), lipgloss.NewStyle().Foreground(Yellow)}
	styleErr   = LevelStyle{"✗", lipgloss.NewStyle().Foreground(Red), lipgloss.NewStyle().Foreground(Red)}
	styleCrit  = LevelStyle{"✖", lipgloss.NewStyle().Foreground(Crit).Bold(true), lipgloss.NewStyle().Foreground(Crit).Bold(true)}
)

// ForLevel возвращает стиль для уровня 0..4 (logfeed.Level).
func ForLevel(level int) LevelStyle {
	switch level {
	case 1:
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
