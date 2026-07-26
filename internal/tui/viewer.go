// Package tui содержит экраны приложения на Bubble Tea.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"heroku-console/internal/botproc"
	"heroku-console/internal/logfeed"
	"heroku-console/internal/theme"
)

const bootMarker = "root: Got DB"

// ViewerOpts настраивают запуск вьюера — соответствуют ключам старого
// logs.sh (--from, --skip-boot, --history, --debug).
type ViewerOpts struct {
	Bot        *botproc.Manager
	From       int // 0 — только новые строки
	SkipBoot   bool
	History    int
	ShowDebug  bool
}

type ringLine struct{ raw string }

// Viewer — модель Bubble Tea для живого просмотра лога. В отличие от
// bash-версии, прокруткой и переносом строк занимается bubbles/viewport:
// никакой ручной арифметики tput csr/cup и пересборки экрана по кускам —
// весь класс багов "ресайз ломает рамку", который правился в bash-версии
// несколькими заходами, здесь не существует в принципе.
type Viewer struct {
	opts ViewerOpts

	width, height int
	ready         bool

	vp       viewport.Model
	search   textinput.Model
	searching bool

	parser    *logfeed.Parser
	ring      []ringLine
	showDebug bool
	filter    string

	warn, err int
	prevWarn, prevErr int

	botPID    int
	uptime    string
	version   string
	botAlive  bool

	rawContentBuf string // уже отрисованный текст целиком — viewport принимает контент только весь разом, не построчным дописыванием

	follower *logfeed.Follower
	quitting bool
}

func NewViewer(opts ViewerOpts) *Viewer {
	ti := textinput.New()
	ti.Placeholder = "подстрока для поиска…"
	ti.Prompt = "/ "
	ti.CharLimit = 200

	return &Viewer{
		opts:      opts,
		search:    ti,
		parser:    logfeed.NewParser(),
		showDebug: opts.ShowDebug,
		version:   opts.Bot.Version(),
	}
}

type tickMsg time.Time
type followerReadyMsg struct{ f *logfeed.Follower }
type lineMsg struct {
	raw string
	ok  bool
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (v *Viewer) startFollowing() tea.Cmd {
	return func() tea.Msg {
		from := v.opts.From
		f, err := logfeed.Follow(v.opts.Bot.LogFile, from)
		if err != nil {
			return followerReadyMsg{nil}
		}
		return followerReadyMsg{f}
	}
}

func waitForLine(f *logfeed.Follower) tea.Cmd {
	return func() tea.Msg {
		raw, ok := <-f.Lines
		return lineMsg{raw, ok}
	}
}

func (v *Viewer) Init() tea.Cmd {
	return tea.Batch(tickCmd(), v.startFollowing(), tea.EnableMouseCellMotion)
}

func (v *Viewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		widthChanged := msg.Width != v.width
		v.width, v.height = msg.Width, msg.Height
		headerH, footerH := 1, 1
		vpH := msg.Height - headerH - footerH
		if vpH < 1 {
			vpH = 1
		}
		if !v.ready {
			v.vp = viewport.New(msg.Width, vpH)
			v.ready = true
			v.loadHistory()
		} else {
			v.vp.Width, v.vp.Height = msg.Width, vpH
			if widthChanged {
				// Перенос длинных строк считался под прежнюю ширину —
				// пересобираем, иначе после ресайза текст останется
				// разложенным по старому краю окна.
				v.rebuildFromRing()
			}
		}
		v.search.Width = msg.Width - 4
		return v, nil

	case tickMsg:
		v.refreshBotState()
		return v, tickCmd()

	case followerReadyMsg:
		v.follower = msg.f
		if v.follower == nil {
			return v, nil
		}
		return v, waitForLine(v.follower)

	case lineMsg:
		if !msg.ok {
			return v, nil // поток закончился (файл исчез) — тихо ждём дальше нечего
		}
		v.feedLine(msg.raw)
		return v, waitForLine(v.follower)

	case tea.MouseMsg:
		var cmd tea.Cmd
		v.vp, cmd = v.vp.Update(msg)
		return v, cmd

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v *Viewer) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.searching {
		switch msg.Type {
		case tea.KeyEnter:
			v.filter = v.search.Value()
			v.searching = false
			v.search.Blur()
			v.rebuildFromRing()
		case tea.KeyEsc:
			v.searching = false
			v.search.Blur() // фильтр не меняем — отмена, а не применение
		default:
			var cmd tea.Cmd
			v.search, cmd = v.search.Update(msg)
			return v, cmd
		}
		return v, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		v.quitting = true
		if v.follower != nil {
			v.follower.Stop()
		}
		return v, tea.Quit
	case "d":
		v.showDebug = !v.showDebug
		v.rebuildFromRing()
	case "/", ".":
		// Поле всегда открывается пустым, а не с текущим фильтром: так
		// пустой Enter предсказуемо снимает фильтр — та же договорённость,
		// что была в bash-версии (там тоже пустой ввод сбрасывал /фильтр).
		v.searching = true
		v.search.SetValue("")
		v.search.Focus()
		return v, textinput.Blink
	case "up", "k":
		v.vp.LineUp(1)
	case "down", "j":
		v.vp.LineDown(1)
	case "pgup":
		v.vp.HalfViewUp()
	case "pgdown":
		v.vp.HalfViewDown()
	case "g", "home":
		v.vp.GotoTop()
	case "G", "end":
		v.vp.GotoBottom()
	}
	return v, nil
}

// feedLine разбирает одну новую строку лога, буферизует её для будущей
// пересборки (фильтр, изменение showDebug) и, если она проходит текущие
// правила видимости, дописывает в конец viewport.
func (v *Viewer) feedLine(raw string) {
	if strings.Contains(raw, bootMarker) {
		if v.opts.SkipBoot {
			v.opts.SkipBoot = false
		} else {
			v.appendRestartBanner()
		}
	}

	v.ring = append(v.ring, ringLine{raw})
	const ringMax = 2000
	if len(v.ring) > ringMax+500 {
		v.ring = v.ring[len(v.ring)-ringMax:]
	}

	rec, complete := v.parser.Feed(raw)
	if !complete {
		return
	}
	w, e := v.parser.Counts()
	v.warn, v.err = w, e

	if logfeed.Visible(rec, v.showDebug, v.filter) {
		v.appendRendered(renderRecord(rec, v.width))
	}
}

func (v *Viewer) appendRestartBanner() {
	line := lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true).
		Render(fmt.Sprintf("  ⟳  ПЕРЕЗАПУСК  ·  %s  ·  pid %d", time.Now().Format("15:04:05"), v.botPID))
	v.appendRendered("\n" + line + "\n")
	v.version = v.opts.Bot.Version()
}

// appendRendered дописывает готовый (уже стилизованный) кусок текста в
// конец видимого содержимого. Если пользователь ничего не пролистывал и
// был внизу — остаёмся внизу и после добавления (обычное поведение живого
// лога); если он поднялся вверх почитать историю — позицию не трогаем,
// новая строка просто ждёт ниже, как в любом приличном log-вьюере.
func (v *Viewer) appendRendered(s string) {
	if !v.ready {
		return
	}
	stuck := v.vp.AtBottom()
	if v.rawContentBuf != "" {
		v.rawContentBuf += "\n"
	}
	v.rawContentBuf += s
	v.applyContent()
	if stuck {
		v.vp.GotoBottom()
	}
}

// applyContent отдаёт накопленный текст во viewport, прижимая его к низу:
// пока строк меньше, чем высота окна, сверху добавляются пустые. Иначе
// свежий лог висел бы у верхнего края с пустотой под ним, а привычно —
// когда новые строки приходят снизу, у самой статус-строки, как в терминале.
// Отступ добавляется только на отдаче: rawContentBuf остаётся чистым, иначе
// пустые строки копились бы при каждом дописывании.
func (v *Viewer) applyContent() {
	content := v.rawContentBuf
	lines := 0
	if content != "" {
		lines = strings.Count(content, "\n") + 1
	}
	if gap := v.vp.Height - lines; gap > 0 {
		content = strings.Repeat("\n", gap) + content
	}
	v.vp.SetContent(content)
}

// rebuildFromRing пересобирает весь видимый контент заново из буфера сырых
// строк — нужно и при смене фильтра, и при переключении DEBUG, поскольку
// оба меняют, что вообще должно быть видно на уже нарисованном экране.
func (v *Viewer) rebuildFromRing() {
	v.parser = logfeed.NewParser()
	var b strings.Builder
	for _, rl := range v.ring {
		rec, complete := v.parser.Feed(rl.raw)
		if !complete {
			continue
		}
		if logfeed.Visible(rec, v.showDebug, v.filter) {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(renderRecord(rec, v.width))
		}
	}
	w, e := v.parser.Counts()
	v.warn, v.err = w, e
	if v.ready {
		v.rawContentBuf = b.String()
		v.applyContent()
		v.vp.GotoBottom()
	}
}

func (v *Viewer) loadHistory() {
	if v.opts.History <= 0 {
		return
	}
	// история читается напрямую из файла отдельным разбором, чтобы не
	// путать счётчики "живого" парсера с уже показанным прошлым
	lines := logfeed.TailLines(v.opts.Bot.LogFile, 5000)
	// Прочитанные строки идут и в кольцевой буфер: иначе переключение
	// DEBUG/фильтра (rebuildFromRing читает только v.ring) не сможет
	// вернуть историю обратно — она просто исчезнет из вида безвозвратно,
	// хотя формально уже была загружена.
	for _, l := range lines {
		v.ring = append(v.ring, ringLine{l})
	}
	hp := logfeed.NewParser()
	var visible []string
	for _, l := range lines {
		rec, complete := hp.Feed(l)
		if !complete {
			continue
		}
		if logfeed.Visible(rec, v.showDebug, "") {
			visible = append(visible, renderRecord(rec, v.width))
		}
	}
	if len(visible) > v.opts.History {
		visible = visible[len(visible)-v.opts.History:]
	}
	w, e := hp.Counts()
	v.warn, v.err = w, e
	// prevWarn/prevErr стартуют от той же базы: иначе первый же тик увидит
	// warn/err из ИСТОРИИ как "новые события с момента запуска" и позвонит
	// в колокол по строкам, которые пользователь и так уже видит на экране.
	v.prevWarn, v.prevErr = w, e
	// Живой парсер должен продолжить счёт от этой базы, а не с нуля —
	// иначе первая же новая строка перезапишет v.warn/v.err своими
	// собственными (нулевыми) счётчиками и история потеряется из подвала.
	v.parser.SetCounts(w, e)
	v.rawContentBuf = strings.Join(visible, "\n")
	v.applyContent()
	v.vp.GotoBottom()
}

func (v *Viewer) refreshBotState() {
	pid := botproc.PID()
	v.botAlive = pid != 0
	v.botPID = pid
	if v.botAlive {
		v.uptime = botproc.Uptime(pid)
	} else {
		v.uptime = "—"
	}
	if v.warn > v.prevWarn || v.err > v.prevErr {
		fmt.Print("\a")
	}
	v.prevWarn, v.prevErr = v.warn, v.err

	title := fmt.Sprintf("Heroku · ⚠ %d ✗ %d", v.warn, v.err)
	if !v.botAlive {
		title = "Heroku · не запущен"
	}
	fmt.Printf("\033]0;%s\007", title)
}

func (v *Viewer) View() string {
	if !v.ready {
		return "запуск…"
	}
	if v.quitting {
		return ""
	}
	return v.renderHeader() + "\n" + v.vp.View() + "\n" + v.renderFooter()
}
