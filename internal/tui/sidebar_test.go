package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

func testViewer(w, h int) *Viewer {
	v := &Viewer{
		width: w, height: h,
		showSidebar: true, ready: true,
		botAlive: true, botPID: 4821, uptime: "1ч 12м", version: "2.2.2",
		warn: 3, err: 12,
		activity: []int{0, 1, 2, 5, 9, 3, 1, 0, 0, 2, 7, 4, 1, 0, 1, 3, 8, 2, 0, 0, 1, 4, 2, 1},
	}
	v.vp = viewport.New(v.logWidth(), h-2)
	v.scr.reset(v.logWidth())
	feed(&v.scr,
		"2026-07-27 00:13:01 [INFO] heroku.modules.terminal: Command .sh executed",
		"2026-07-27 00:13:04 [WARNING] heroku.modules.spotify: Token refresh took 4.2s",
		"2026-07-27 00:13:07 [ERROR] heroku.tl_cache: Failed to resolve @unknown_channel",
		"2026-07-27 00:13:09 [ERROR] heroku.tl_cache: Failed to resolve @unknown_channel",
		"2026-07-27 00:13:20 [INFO] root: всё хорошо",
	)
	v.applyContent()
	v.vp.GotoBottom()
	return v
}

func testScreen(v *Viewer) string {
	body := v.vp.View()
	if v.sidebarVisible() {
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			v.renderSidebar(v.vp.Height), sidebarDivider(v.vp.Height), body)
	}
	return v.renderHeader() + "\n" + body + "\n" + v.renderFooter()
}

// Ни одна строка экрана не должна быть шире или уже окна: лишняя ячейка
// переносится терминалом и ломает всю рамку до самого низа.
func TestScreenWidthExact(t *testing.T) {
	for _, sz := range [][2]int{{84, 8}, {83, 8}, {104, 24}, {120, 5}, {200, 40}} {
		v := testViewer(sz[0], sz[1])
		for i, line := range strings.Split(testScreen(v), "\n") {
			if got := lipgloss.Width(line); got != sz[0] {
				t.Errorf("%dx%d строка %d: ширина %d, want %d", sz[0], sz[1], i, got, sz[0])
			}
		}
	}
}

// Панель прячется сама на узком окне: отнимать колонки у сообщения там
// дороже, чем показать статистику.
func TestSidebarHidesOnNarrow(t *testing.T) {
	if v := testViewer(sidebarMinTotal, 20); !v.sidebarVisible() {
		t.Error("на пороговой ширине панель должна быть видна")
	}
	v := testViewer(sidebarMinTotal-1, 20)
	if v.sidebarVisible() {
		t.Error("ниже порога панель должна прятаться")
	}
	if v.logWidth() != v.width {
		t.Errorf("без панели лог занимает всю ширину: got %d, want %d", v.logWidth(), v.width)
	}
	// Счётчики не должны пропасть вместе с панелью — они переезжают в подвал.
	if !strings.Contains(v.renderFooter(), "✗ 12") {
		t.Error("без панели счётчики должны вернуться в подвал")
	}
}

// При нехватке высоты режется список модулей и график, но не счётчики
// проблем и не состояние процесса — ради них панель и существует.
func TestSidebarKeepsEssentialsWhenShort(t *testing.T) {
	// Четыре строки на панель — минимум из минимумов.
	v := testViewer(104, 6)
	sb := v.renderSidebar(v.vp.Height)
	if !strings.Contains(sb, "✗ 12") {
		t.Error("счётчик ошибок должен пережить обрезку по высоте")
	}
	if !strings.Contains(sb, "● live") {
		t.Error("состояние процесса должно пережить обрезку по высоте")
	}
	if got := len(strings.Split(sb, "\n")); got != v.vp.Height {
		t.Errorf("высота панели: got %d, want %d", got, v.vp.Height)
	}

	// Чуть больше места — возвращается pid, но модулей всё ещё нет.
	v = testViewer(104, 9)
	sb = v.renderSidebar(v.vp.Height)
	if !strings.Contains(sb, "pid 4821") {
		t.Error("на семи строках pid уже должен помещаться")
	}
}

// Панель всегда ровно запрошенной высоты: она стоит рядом с логом, и лишняя
// строка сдвинула бы нижнюю рамку окна.
func TestSidebarExactHeight(t *testing.T) {
	for h := 1; h <= 30; h++ {
		v := testViewer(104, h+2)
		if got := len(strings.Split(v.renderSidebar(h), "\n")); got != h {
			t.Errorf("высота %d: got %d строк", h, got)
		}
	}
}

func TestSidebarModuleStats(t *testing.T) {
	v := testViewer(104, 24)
	stats := v.scr.moduleStats()
	if len(stats) == 0 {
		t.Fatal("статистика пуста")
	}
	// Самый шумный первым, схлопнутая серия считается своим Count.
	if stats[0].name != "tl_cache" || stats[0].count != 2 {
		t.Errorf("первый модуль: got %s ×%d, want tl_cache ×2", stats[0].name, stats[0].count)
	}
	if stats[0].err != 2 {
		t.Errorf("ошибки tl_cache: got %d, want 2", stats[0].err)
	}
	sb := v.renderSidebar(v.vp.Height)
	if !strings.Contains(sb, "tl_cache") {
		t.Error("модуль должен попасть в панель")
	}
}

func TestBarProportions(t *testing.T) {
	if got := bar(10, 10, 5); lipgloss.Width(got) != 5 {
		t.Errorf("полный столбик: got %q (%d ячеек), want 5", got, lipgloss.Width(got))
	}
	if got := bar(0, 10, 5); got != "" {
		t.Errorf("нулевой столбик: got %q, want пусто", got)
	}
	// Значение больше пика не должно вылезать за отведённые ячейки.
	if got := bar(99, 10, 5); lipgloss.Width(got) > 5 {
		t.Errorf("переполнение: got %q (%d ячеек)", got, lipgloss.Width(got))
	}
}
