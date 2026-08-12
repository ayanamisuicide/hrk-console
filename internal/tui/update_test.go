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

// Нажатие "u" обязано завести все четыре шага в состоянии "не начат" —
// без этого renderSteps не рисует список вообще, и первый кадр выглядел бы
// пустым, пока не придёт первое сообщение о прогрессе.
func TestUpdateScreenInitializesStepsOnU(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateAvailable
	u.latest = "v9.9.9"

	model, _ := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	u = model.(*UpdateScreen)

	if len(u.steps) != len(updateStageOrder) {
		t.Fatalf("заведено %d шагов, ожидалось %d", len(u.steps), len(updateStageOrder))
	}
	for _, s := range updateStageOrder {
		if u.steps[s] == nil || u.steps[s].state != stepPending {
			t.Errorf("шаг %v должен быть stepPending сразу после запуска", s)
		}
	}
	if u.progressCh == nil {
		t.Error("канал прогресса не создан")
	}
	if !strings.Contains(u.View(), "запрос к GitHub") {
		t.Error("список шагов должен быть виден сразу, не дожидаясь первого прогресса")
	}
}

// updateProgressMsg обязан переводить нужный шаг в running/done и не трогать
// остальные — иначе одно сообщение красило бы весь список разом.
func TestUpdateProgressMsgUpdatesOnlyItsStep(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateApplying
	u.steps = map[selfupdate.Stage]*updateStep{}
	for _, s := range updateStageOrder {
		u.steps[s] = &updateStep{}
	}
	u.progressCh = make(chan selfupdate.Progress, 1)

	model, _ := u.Update(updateProgressMsg{Stage: selfupdate.StageFind})
	u = model.(*UpdateScreen)
	if u.steps[selfupdate.StageFind].state != stepRunning {
		t.Error("StageFind должен стать running")
	}
	if u.steps[selfupdate.StageQuery].state != stepPending || u.steps[selfupdate.StageSwap].state != stepPending {
		t.Error("остальные шаги не должны меняться от чужого сообщения")
	}

	model, _ = u.Update(updateProgressMsg{
		Stage: selfupdate.StageFind, Done: true, Note: "hkc-v9.9.9-linux-amd64.tar.gz",
	})
	u = model.(*UpdateScreen)
	if u.steps[selfupdate.StageFind].state != stepDone {
		t.Error("StageFind с Done=true должен стать stepDone")
	}
	if u.steps[selfupdate.StageFind].note != "hkc-v9.9.9-linux-amd64.tar.gz" {
		t.Errorf("note = %q, ожидалось имя архива", u.steps[selfupdate.StageFind].note)
	}
}

// StageUnpack и StageDone не входят в updateStageOrder (см. комментарий
// там же) — сообщения про них не должны падать на nil-шаге и не должны
// прерывать чтение канала.
func TestUpdateProgressMsgIgnoresStagesNotInOrder(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateApplying
	u.steps = map[selfupdate.Stage]*updateStep{}
	for _, s := range updateStageOrder {
		u.steps[s] = &updateStep{}
	}
	u.progressCh = make(chan selfupdate.Progress, 1)

	model, cmd := u.Update(updateProgressMsg{Stage: selfupdate.StageUnpack, Done: true})
	u = model.(*UpdateScreen)
	if cmd == nil {
		t.Error("даже проигнорированное сообщение должно переиздать listenProgressCmd")
	}
	for _, s := range updateStageOrder {
		if u.steps[s].state != stepPending {
			t.Errorf("StageUnpack не должен трогать шаг %v", s)
		}
	}
}

// Полоса скачивания и цифры "X / Y" должны появляться только когда сервер
// прислал Content-Length (total > 0) — без него рисовать проценты от
// неизвестного целого было бы враньём, тот же принцип, что в GUI.
func TestUpdateScreenRendersDownloadProgress(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateApplying
	u.steps = map[selfupdate.Stage]*updateStep{}
	for _, s := range updateStageOrder {
		u.steps[s] = &updateStep{}
	}
	u.steps[selfupdate.StageDownload].state = stepRunning
	u.steps[selfupdate.StageDownload].bytes = 2 * 1048576
	u.steps[selfupdate.StageDownload].total = 5 * 1048576

	view := u.View()
	if !strings.Contains(view, "2.0 МБ / 5.0 МБ") {
		t.Errorf("ожидались цифры прогресса в МБ, view: %q", view)
	}

	u2 := NewUpdateScreen("v1.0.0")
	u2.phase = updateApplying
	u2.steps = map[selfupdate.Stage]*updateStep{}
	for _, s := range updateStageOrder {
		u2.steps[s] = &updateStep{}
	}
	u2.steps[selfupdate.StageDownload].state = stepRunning
	u2.steps[selfupdate.StageDownload].bytes = 300 * 1024
	// total не задан (сервер не прислал Content-Length)
	view2 := u2.View()
	if strings.Contains(view2, "/") {
		t.Errorf("без известного total не должно быть доли — view: %q", view2)
	}
	if !strings.Contains(view2, "300 КБ") {
		t.Errorf("без total должно быть видно хотя бы сколько уже скачано, view: %q", view2)
	}
}

// Сквозной поток через настоящий канал: applyCmd эмитит шаги по порядку,
// listenProgressCmd их вычитывает и переиздаёт себя, а завершающий
// updateApplyMsg приходит уже после того, как все шаги на экране отмечены
// done, — ровно то, что визуально обещает GUI-аналог того же обновления.
func TestUpdateScreenFullProgressFlow(t *testing.T) {
	u := NewUpdateScreen("v1.0.0")
	u.phase = updateApplying
	u.steps = map[selfupdate.Stage]*updateStep{}
	for _, s := range updateStageOrder {
		u.steps[s] = &updateStep{}
	}
	u.progressCh = make(chan selfupdate.Progress)

	fire := func(p selfupdate.Progress) tea.Cmd {
		var cmd tea.Cmd
		var model tea.Model
		model, cmd = u.Update(updateProgressMsg(p))
		u = model.(*UpdateScreen)
		return cmd
	}

	fire(selfupdate.Progress{Stage: selfupdate.StageQuery})
	fire(selfupdate.Progress{Stage: selfupdate.StageQuery, Done: true, Note: "v1.1.0"})
	fire(selfupdate.Progress{Stage: selfupdate.StageFind})
	fire(selfupdate.Progress{Stage: selfupdate.StageFind, Done: true, Note: "hkc-v1.1.0-linux-amd64.tar.gz"})
	fire(selfupdate.Progress{Stage: selfupdate.StageDownload})
	fire(selfupdate.Progress{Stage: selfupdate.StageDownload, Bytes: 500_000, Total: 1_000_000})
	fire(selfupdate.Progress{Stage: selfupdate.StageDownload, Done: true, Bytes: 1_000_000, Total: 1_000_000})
	fire(selfupdate.Progress{Stage: selfupdate.StageUnpack, Done: true}) // не в updateStageOrder — просто пропускается
	fire(selfupdate.Progress{Stage: selfupdate.StageSwap})
	fire(selfupdate.Progress{Stage: selfupdate.StageSwap, Done: true, Note: "/usr/local/bin/hkc"})

	for _, s := range updateStageOrder {
		if u.steps[s].state != stepDone {
			t.Errorf("шаг %v должен быть done после полного прогона, state=%v", s, u.steps[s].state)
		}
	}
	if u.steps[selfupdate.StageDownload].bytes != 1_000_000 || u.steps[selfupdate.StageDownload].total != 1_000_000 {
		t.Error("итоговые байты скачивания не сохранились")
	}

	model, _ := u.Update(updateApplyMsg{version: "v1.1.0"})
	u = model.(*UpdateScreen)
	if u.phase != updateDone {
		t.Fatalf("phase = %v, ожидался updateDone", u.phase)
	}
	view := u.View()
	if !strings.Contains(view, "✓ обновлено до v1.1.0") {
		t.Error("итоговое сообщение об успехе должно быть видно")
	}
	if !strings.Contains(view, "подмена на диске") {
		t.Error("список шагов должен оставаться на экране updateDone, а не исчезать")
	}
}
