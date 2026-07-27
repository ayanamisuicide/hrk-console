package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/logfeed"
)

// Рамка не обрезает подписи сама: строка длиннее окна не влезает, а
// раздвигает линию, терминал переносит хвост, и разъезжается весь экран до
// самого низа. Поэтому ширина проверяется во всех состояниях, которые
// добавляют текста в шапку и подвал.
func TestFrameWidthInAllStates(t *testing.T) {
	states := map[string]func(*Viewer){
		"обычно": func(v *Viewer) {},
		"порог":  func(v *Viewer) { v.minLevel = logfeed.LevelWarning },
		"обрыв":  func(v *Viewer) { v.streamIssue = "файл лога недоступен  ·  3 м" },
		"порог+обрыв": func(v *Viewer) {
			v.minLevel = logfeed.LevelError
			v.streamIssue = "файл лога недоступен  ·  1 ч 12 м"
		},
		"фильтр":    func(v *Viewer) { v.filter = "urllib3.connectionpool" },
		"бот мёртв": func(v *Viewer) { v.botAlive = false },
	}

	for _, w := range []int{40, 46, 52, 60, 70, 83, 84, 90, 104, 120, 200} {
		for name, setup := range states {
			v := testViewer(w, 20)
			setup(v)
			for i, line := range strings.Split(testScreen(v), "\n") {
				if got := lipgloss.Width(line); got != w {
					t.Errorf("%d/%s строка %d: ширина %d, want %d", w, name, i, got, w)
				}
			}
		}
	}
}

// Модальные экраны рисуются поверх лога и обязаны укладываться в окно так
// же, как он: не шире и не выше.
func TestOverlaysFitWindow(t *testing.T) {
	for _, w := range []int{40, 50, 60, 84, 104, 200} {
		for _, h := range []int{5, 6, 8, 12, 24, 40} {
			v := testViewer(w, h)
			v.modList = v.scr.moduleStats()
			for name, body := range map[string]string{
				"справка": v.renderHelp(),
				"модули":  v.renderModPick(),
			} {
				lines := strings.Split(body, "\n")
				for i, line := range lines {
					if got := lipgloss.Width(line); got > w {
						t.Errorf("%s %dx%d строка %d: ширина %d > %d", name, w, h, i, got, w)
					}
				}
				if len(lines) > v.vp.Height {
					t.Errorf("%s %dx%d: %d строк при высоте %d", name, w, h, len(lines), v.vp.Height)
				}
			}
		}
	}
}

// Пустой экран — обычное состояние сразу после запуска: ничего из того, что
// считает по блокам, не должно на нём падать.
func TestEmptyScreenIsSafe(t *testing.T) {
	v := testViewer(104, 24)
	v.scr.reset(v.logWidth())
	v.applyContent()

	_ = testScreen(v)
	v.jumpProblem(1)
	v.jumpProblem(-1)
	v.jumpRestart(1)
	v.jumpRestart(-1)
	_ = v.renderSidebar(v.vp.Height)
	_ = v.renderModPick()

	if got := v.scr.totalLines(); got != 0 {
		t.Errorf("пустой экран: got %d строк", got)
	}
	if len(v.scr.moduleStats()) != 0 {
		t.Error("на пустом экране статистика должна быть пустой")
	}
}

// Список модулей — снимок на момент открытия: лог продолжает идти, и живой
// пересчёт переставлял бы строки под курсором, из-за чего Enter выбирал бы
// не то, что видно на экране.
func TestModPickUsesSnapshot(t *testing.T) {
	v := testViewer(104, 24)
	v.modList = v.scr.moduleStats()
	before := len(v.modList)

	// В лог приходит новый шумный модуль — снимок не должен измениться.
	for i := 0; i < 20; i++ {
		feed(&v.scr, "2026-07-27 00:14:00 [INFO] heroku.modules.newcomer: шум")
	}
	if len(v.modList) != before {
		t.Errorf("снимок изменился: было %d, стало %d", before, len(v.modList))
	}
	if v.modList[0].name != "tl_cache" {
		t.Errorf("порядок в снимке поехал: первый %q", v.modList[0].name)
	}
}
