package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/theme"
	"heroku-console/selfupdate"
)

// hkcAssetPrefix/hkcAssetSuffix — имя архива с hkc в релизе, как его кладёт
// release.yml: tar -czf hkc-$TAG-linux-amd64.tar.gz. GUI в том же релизе
// носит своё имя (hrk-console-gui-*) — selfupdate.Apply различает их по
// этим prefix/suffix, иначе скачал бы не то.
const (
	hkcAssetPrefix = "hkc-"
	hkcAssetSuffix = "-linux-amd64.tar.gz"
)

type updatePhase int

const (
	updateChecking updatePhase = iota
	updateUpToDate
	updateAvailable
	updateApplying
	updateDone
	updateFailed
)

// updateStageOrder — те же четыре шага, что рисует окно обновления в GUI
// (gui/frontend/src/main.js, UP_STEPS): ровно то, что selfupdate реально
// делает по порядку, не выдуманная для красоты последовательность.
// StageUnpack и StageDone в список не входят — распаковка идёт тем же
// потоком, что скачивание (tar тянет из gzip, тот из сети, они происходят
// одновременно), отдельная строка для неё открылась бы и закрылась в один
// кадр; StageDone просто дублирует итог, который и так виден по фазе экрана.
var updateStageOrder = []selfupdate.Stage{
	selfupdate.StageQuery,
	selfupdate.StageFind,
	selfupdate.StageDownload,
	selfupdate.StageSwap,
}

var updateStageLabel = map[selfupdate.Stage]string{
	selfupdate.StageQuery:    "запрос к GitHub",
	selfupdate.StageFind:     "поиск сборки в релизе",
	selfupdate.StageDownload: "скачивание и распаковка",
	selfupdate.StageSwap:     "подмена на диске",
}

// updateCardWidth — ширина блока карточки (см. View, Width(updateCardWidth)).
// updateCardTextWidth — сколько из неё реально остаётся под текст: lipgloss
// вписывает Padding(1, 2) ВНУТРЬ заданной Width, а не добавляет её поверх
// (проверено эмпирически — .Width(46) с Padding(1,2) переносит текст уже на
// 43-м видимом символе), так что паддинг по 2 колонки с каждой стороны надо
// вычитать. Без этой поправки renderSteps завышал доступное место на 4
// колонки, и длинное имя архива (hkc-v1.16.0-linux-amd64.tar.gz) всё равно
// переносилось на новую строку с потерей отступа, хотя рамка при этом не
// рвалась.
const (
	updateCardWidth     = 46
	updateCardTextWidth = updateCardWidth - 4
)

type stepState int

const (
	stepPending stepState = iota
	stepRunning
	stepDone
)

type updateStep struct {
	state        stepState
	note         string
	bytes, total int64
}

// UpdateScreen — экран самообновления hkc, тот же приём, что у GUI-бейджа
// (CheckForUpdate/ApplyUpdate), только текстом: проверка → либо "уже
// последняя", либо явное "u — обновить" → скачивание/подмена → явное
// "любая клавиша — перезапустить". Ничего не происходит само по себе —
// подмена собственного исполняемого файла необратима без переустановки,
// оба шага (обновить / перезапустить) требуют отдельного явного действия,
// тот же контракт, что и в gui/frontend/src/main.js.
type UpdateScreen struct {
	current string
	phase   updatePhase
	latest  string
	message string

	// steps/progressCh — только во время updateApplying (и то, что успело
	// накопиться, остаётся видно на updateDone/updateFailed). nil, пока
	// обновление не запущено, и в юнит-тестах, которые ставят фазу напрямую,
	// минуя реальный applyCmd — renderSteps на нём просто ничего не рисует.
	steps      map[selfupdate.Stage]*updateStep
	progressCh chan selfupdate.Progress

	// RestartRequested — пользователь подтвердил перезапуск на updateDone.
	// Читается вызывающим после p.Run() (тот же приём, что Preflight.Failed/
	// Aborted): перезапуск обязан случиться, когда терминал уже отдан
	// обратно, а не изнутри Update.
	RestartRequested bool

	width, height int
	frame         int
}

func NewUpdateScreen(currentVersion string) *UpdateScreen {
	return &UpdateScreen{current: currentVersion}
}

type updateCheckMsg selfupdate.CheckResult
type updateApplyMsg struct {
	version string
	err     error
}
type updateFrameMsg time.Time
type updateProgressMsg selfupdate.Progress

func (u *UpdateScreen) Init() tea.Cmd {
	return tea.Batch(updateFrameCmd(), checkCmd(u.current))
}

func checkCmd(current string) tea.Cmd {
	return func() tea.Msg { return updateCheckMsg(selfupdate.Check(current)) }
}

// applyCmd качает и подменяет бинарник, попутно сливая каждый шаг в ch —
// тем же способом, что и GUI (selfupdate.ApplyChannelProgress), только
// колбэк здесь не эмитит событие в webview, а пишет в канал: обычный Cmd
// не может звать Update() напрямую из чужой горутины, а канал + отдельная
// "слушающая" команда (listenProgressCmd) — стандартный приём Bubble Tea
// для потоковых данных. Канал без буфера — каждая запись блокируется, пока
// слушатель не заберёт предыдущую, так шаги идут строго по порядку, а не
// пачкой, если получатель отстанет.
func applyCmd(ch chan<- selfupdate.Progress) tea.Cmd {
	return func() tea.Msg {
		v, err := selfupdate.ApplyChannelProgress("", hkcAssetPrefix, hkcAssetSuffix,
			func(p selfupdate.Progress) { ch <- p })
		close(ch)
		return updateApplyMsg{version: v, err: err}
	}
}

// listenProgressCmd вычитывает одно сообщение и сразу возвращает его как
// tea.Msg; Update() при получении updateProgressMsg переиздаёт эту же
// команду — так очередь слушается, пока applyCmd не закроет канал (тогда
// чтение отдаёт ok=false, и команда молча завершается, ничего не переиздавая).
func listenProgressCmd(ch <-chan selfupdate.Progress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return updateProgressMsg(p)
	}
}

// Тот же spinnerFPS/spinnerFrames, что у экрана проверок (preflight.go) —
// один и тот же тикер на пакет, а не свой для каждого экрана с крутящимся
// индикатором.
func updateFrameCmd() tea.Cmd {
	return tea.Tick(time.Second/spinnerFPS, func(t time.Time) tea.Msg { return updateFrameMsg(t) })
}

func (u *UpdateScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
		return u, nil

	case updateFrameMsg:
		u.frame++
		if u.phase == updateChecking || u.phase == updateApplying {
			return u, updateFrameCmd()
		}
		return u, nil

	case updateCheckMsg:
		r := selfupdate.CheckResult(msg)
		if !r.OK {
			u.phase = updateFailed
			u.message = r.Message
			return u, nil
		}
		if r.Available {
			u.phase = updateAvailable
			u.latest = r.Latest
		} else {
			u.phase = updateUpToDate
		}
		return u, nil

	case updateProgressMsg:
		p := selfupdate.Progress(msg)
		// StageUnpack и StageDone не рисуются отдельной строкой (см.
		// updateStageOrder) — просто продолжаем слушать канал дальше.
		if step := u.steps[p.Stage]; step != nil {
			if p.Done {
				step.state = stepDone
			} else {
				step.state = stepRunning
			}
			if p.Note != "" {
				step.note = p.Note
			}
			if p.Stage == selfupdate.StageDownload {
				step.bytes, step.total = p.Bytes, p.Total
			}
		}
		return u, listenProgressCmd(u.progressCh)

	case updateApplyMsg:
		if msg.err != nil {
			u.phase = updateFailed
			u.message = msg.err.Error()
			return u, nil
		}
		u.phase = updateDone
		u.latest = msg.version
		return u, nil

	case tea.KeyMsg:
		switch u.phase {
		case updateChecking, updateApplying:
			return u, nil // идёт запрос — клавиши пока не значат ничего
		case updateAvailable:
			if msg.String() == "u" {
				u.phase = updateApplying
				u.steps = make(map[selfupdate.Stage]*updateStep, len(updateStageOrder))
				for _, s := range updateStageOrder {
					u.steps[s] = &updateStep{}
				}
				u.progressCh = make(chan selfupdate.Progress)
				return u, tea.Batch(updateFrameCmd(), applyCmd(u.progressCh), listenProgressCmd(u.progressCh))
			}
			return u, tea.Quit
		case updateDone:
			// Сам перезапуск делает не этот экран, а вызывающий (см.
			// runUpdateScreen в cmd/hkc/main.go) — уже ПОСЛЕ того, как Bubble
			// Tea вернёт терминал в обычный режим. Раньше здесь стоял
			// немедленный restartSelf с os.Exit внутри: он проскакивал мимо
			// tea.Quit, оставляя за собой alt-screen и raw-режим, и подменял
			// процесс в момент, когда терминал ещё принадлежал Bubble Tea.
			u.RestartRequested = true
			return u, tea.Quit
		default: // updateUpToDate, updateFailed
			return u, tea.Quit
		}
	}
	return u, nil
}

// renderSteps рисует список шагов обновления, тот же язык, что и у экрана
// проверок (preflight.go): ○ не начат, спиннер — идёт сейчас, ✓ — сделано.
// Пусто, если steps ещё не заведён (обновление не запускалось, или экран в
// состоянии, которое юнит-тест выставил напрямую, минуя applyCmd).
func (u *UpdateScreen) renderSteps() string {
	if u.steps == nil {
		return ""
	}
	var b strings.Builder
	for _, stage := range updateStageOrder {
		st := u.steps[stage]
		var mark, label string
		switch st.state {
		case stepPending:
			mark = theme.Faint.Render("○")
			label = theme.Faint.Render(updateStageLabel[stage])
		case stepRunning:
			mark = lipgloss.NewStyle().Foreground(theme.Accent).
				Render(spinnerFrames[u.frame%len(spinnerFrames)])
			label = lipgloss.NewStyle().Foreground(theme.Text).Render(updateStageLabel[stage])
		case stepDone:
			mark = lipgloss.NewStyle().Foreground(theme.Green).Render("✓")
			label = theme.Meta.Render(updateStageLabel[stage])
		}
		line := "  " + mark + "  " + label
		// Полоса — только у скачивания и только когда известен размер
		// (Content-Length): у остальных шагов нет ни объёма, ни
		// длительности, рисовать им полосу значило бы показывать прогресс,
		// которого не существует (см. gui/frontend/src/style.css, тот же
		// принцип для GUI).
		switch {
		case stage == selfupdate.StageDownload && st.total > 0:
			// Ширина полосы — 14, не 20: с реальным размером GUI-архива
			// (~12 МБ) подпись "XX.X МБ / XX.X МБ" уже 17 колонок, и на
			// widthе 20 строка "     " + полоса + подпись перевешивает
			// updateCardTextWidth (42) и переносится без отступа — тот же
			// баг, что чинит clipLeft у note чуть ниже.
			line += "\n     " + theme.MeterBar(int(st.bytes), int(st.total), 14) +
				theme.Faint.Render("  "+humanBytes(st.bytes)+" / "+humanBytes(st.total))
		case stage == selfupdate.StageDownload && st.bytes > 0:
			line += theme.Faint.Render("  " + humanBytes(st.bytes))
		case st.note != "":
			// clipLeft (см. preflight.go) режет длинное имя архива слева,
			// оставляя хвост с многоточием, вместо того чтобы отдавать его
			// автопереносу lipgloss — тот переносит без сохранения отступа,
			// и вторая строка съезжает к левому краю карточки.
			plain := "  " + st.note
			styled := theme.Faint.Render(plain)
			line += clipLeft(plain, styled, updateCardTextWidth-lipgloss.Width(line)-2)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// humanBytes — тот же формат, что humanBytes в gui/frontend/src/main.js:
// КБ до мегабайта, дальше МБ с одним знаком после запятой.
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 КБ"
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.0f КБ", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f МБ", float64(n)/1048576)
}

func (u *UpdateScreen) View() string {
	width, height := u.width, u.height
	if width < 40 {
		width = 60
	}
	if height < 10 {
		height = 24
	}

	var b strings.Builder
	b.WriteString(theme.Title.Render("HEROKU") + theme.Faint.Render("  ·  обновления hkc") + "\n\n")
	b.WriteString(theme.Meta.Render("текущая версия: ") + theme.Meta.Render(u.current) + "\n\n")

	spin := spinnerFrames[u.frame%len(spinnerFrames)]
	switch u.phase {
	case updateChecking:
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Accent).Render(spin) + "  проверяю GitHub…")
	case updateUpToDate:
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Green).Bold(true).Render("✓ уже последняя версия") +
			theme.Faint.Render("  ·  любая клавиша — назад"))
	case updateAvailable:
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render("↑ найдена "+u.latest) +
			"\n" + theme.Faint.Render("u — скачать и подменить себя  ·  любая другая клавиша — назад"))
	case updateApplying:
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Accent).Render(spin) + "  обновляю…\n\n")
		b.WriteString(u.renderSteps())
	case updateDone:
		b.WriteString(u.renderSteps())
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Yellow).Bold(true).Render("✓ обновлено до "+u.latest) +
			"\n" + theme.Faint.Render("любая клавиша — перезапустить hkc"))
	case updateFailed:
		b.WriteString(u.renderSteps())
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Red).Bold(true).Render("✗ "+u.message) +
			"\n" + theme.Faint.Render("любая клавиша — назад"))
	}

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Overlay).
		Padding(1, 2).
		Width(updateCardWidth).
		Render(b.String())

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, card)
}
