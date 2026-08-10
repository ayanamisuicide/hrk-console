package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"heroku-console/selfupdate"
)

func TestUpdateScreenTransitionsToUpToDate(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	model, _ := u.Update(updateCheckMsg(selfupdate.CheckResult{OK: true, Current: "v1.0.0"}))
	u = model.(*UpdateScreen)
	if u.phase != updateUpToDate {
		t.Fatalf("phase = %v, ожидался updateUpToDate", u.phase)
	}
	if !strings.Contains(u.View(), "уже последняя") {
		t.Error("экран должен сообщать, что версия уже последняя")
	}
}

func TestUpdateScreenTransitionsToAvailable(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	model, _ := u.Update(updateCheckMsg(selfupdate.CheckResult{
		OK: true, Available: true, Current: "v1.0.0", Latest: "v9.9.9", URL: "https://example/release",
	}))
	u = model.(*UpdateScreen)
	if u.phase != updateAvailable || u.latest != "v9.9.9" {
		t.Fatalf("phase=%v latest=%q, ожидался updateAvailable/v9.9.9", u.phase, u.latest)
	}
	if !strings.Contains(u.View(), "v9.9.9") {
		t.Error("экран должен показывать найденную версию")
	}
}

// Осечка проверки (сеть, dev-сборка) должна быть видна и объяснена, а не
// молча оставлять экран в "проверяю…" навсегда.
func TestUpdateScreenTransitionsToFailedOnCheckError(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	model, _ := u.Update(updateCheckMsg(selfupdate.CheckResult{Current: "v1.0.0", Message: "сети нет"}))
	u = model.(*UpdateScreen)
	if u.phase != updateFailed {
		t.Fatalf("phase = %v, ожидался updateFailed", u.phase)
	}
	if !strings.Contains(u.View(), "сети нет") {
		t.Error("причина осечки должна быть видна на экране")
	}
}

// Клавиша "u" запускает скачивание только в состоянии "доступно" — в любом
// другом состоянии нажатие должно просто закрывать экран (возврат в меню),
// а не пытаться качать неизвестно что.
func TestUpdateScreenOnlyAppliesWhenAvailable(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateUpToDate

	model, cmd := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	u = model.(*UpdateScreen)
	if u.phase != updateUpToDate {
		t.Errorf("нажатие u вне updateAvailable не должно менять фазу, got %v", u.phase)
	}
	if cmd == nil {
		t.Error("ожидался tea.Quit — экран должен закрыться")
	}
}

func TestUpdateScreenAppliesOnU(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateAvailable
	u.latest = "v9.9.9"

	model, cmd := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	u = model.(*UpdateScreen)
	if u.phase != updateApplying {
		t.Fatalf("phase = %v, ожидался updateApplying после нажатия u", u.phase)
	}
	if cmd == nil {
		t.Error("ожидалась команда запуска скачивания")
	}
}

func TestUpdateScreenApplyResult(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateApplying

	model, _ := u.Update(updateApplyMsg{err: errors.New("диск занят")})
	u = model.(*UpdateScreen)
	if u.phase != updateFailed || !strings.Contains(u.View(), "диск занят") {
		t.Errorf("ошибка применения должна попадать на экран, phase=%v view=%q", u.phase, u.View())
	}

	u2 := NewUpdateScreen("v1.0.0")
	u2.phase = updateApplying
	model2, _ := u2.Update(updateApplyMsg{version: "v9.9.9"})
	u2 = model2.(*UpdateScreen)
	if u2.phase != updateDone || u2.latest != "v9.9.9" {
		t.Errorf("успешное применение: phase=%v latest=%q, ожидался updateDone/v9.9.9", u2.phase, u2.latest)
	}
}

// Пока идёт запрос (проверка или применение), клавиши не должны ничего
// значить — иначе случайное нажатие во время сетевого ожидания закрыло бы
// экран или запустило бы применение раньше ответа.
func TestUpdateScreenIgnoresKeysWhileBusy(t *testing.T) {
	for _, phase := range []updatePhase{updateChecking, updateApplying} {
		u := NewUpdateScreen("v1.0.0")
		u.phase = phase
		model, cmd := u.Update(tea.KeyMsg{Type: tea.KeyEnter})
		u = model.(*UpdateScreen)
		if u.phase != phase {
			t.Errorf("phase %v изменилась на %v при нажатии клавиши во время ожидания", phase, u.phase)
		}
		if cmd != nil {
			t.Errorf("phase %v: нажатие клавиши во время ожидания не должно порождать команду", phase)
		}
	}
}
