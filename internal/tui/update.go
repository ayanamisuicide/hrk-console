package tui

import (
	"os"
	"os/exec"
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

func (u *UpdateScreen) Init() tea.Cmd {
	return tea.Batch(updateFrameCmd(), checkCmd(u.current))
}

func checkCmd(current string) tea.Cmd {
	return func() tea.Msg { return updateCheckMsg(selfupdate.Check(current)) }
}

func applyCmd() tea.Cmd {
	return func() tea.Msg {
		v, err := selfupdate.Apply(hkcAssetPrefix, hkcAssetSuffix)
		return updateApplyMsg{version: v, err: err}
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
				return u, tea.Batch(updateFrameCmd(), applyCmd())
			}
			return u, tea.Quit
		case updateDone:
			if err := restartSelf(); err != nil {
				u.phase = updateFailed
				u.message = err.Error()
				return u, nil
			}
			return u, tea.Quit
		default: // updateUpToDate, updateFailed
			return u, tea.Quit
		}
	}
	return u, nil
}

// restartSelf поднимает новый процесс того же бинарника (уже подменённого
// selfupdate.Apply) и завершает текущий — тот же приём, что у RestartApp в
// GUI. Отдельный явный шаг, а не часть applyCmd: обновление на диске и
// разрыв текущей сессии должны быть двумя разными решениями пользователя,
// не одним неожиданным.
func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil // недостижимо, но компилятору нужен возврат
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
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Accent).Render(spin) + "  скачиваю и подменяю…")
	case updateDone:
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Yellow).Bold(true).Render("✓ обновлено до "+u.latest) +
			"\n" + theme.Faint.Render("любая клавиша — перезапустить hkc"))
	case updateFailed:
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Red).Bold(true).Render("✗ "+u.message) +
			"\n" + theme.Faint.Render("любая клавиша — назад"))
	}

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Overlay).
		Padding(1, 2).
		Width(46).
		Render(b.String())

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, card)
}
