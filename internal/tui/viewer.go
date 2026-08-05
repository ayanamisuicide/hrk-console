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

	"heroku-console/botproc"
	"heroku-console/internal/theme"
	"heroku-console/logfeed"
	"heroku-console/state"
)

const bootMarker = "root: Got DB"

// ViewerOpts настраивают запуск вьюера — соответствуют ключам старого
// logs.sh (--from, --skip-boot, --history, --debug).
type ViewerOpts struct {
	Bot       *botproc.Manager
	From      int // 0 — только новые строки
	SkipBoot  bool
	History   int
	ShowDebug bool
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

	vp        viewport.Model
	search    textinput.Model
	searching bool

	parser    *logfeed.Parser
	ring      []ringLine
	showDebug bool
	filter    string
	// minLevel — нижняя граница показываемых уровней. Отдельная ручка от
	// showDebug: одна отвечает за шум, другая — за "оставь только проблемы".
	minLevel logfeed.Level

	warn, err         int
	prevWarn, prevErr int

	// activity — сколько записей пришло в каждую из последних секунд;
	// рисуется sparkline'ом в шапке. Хвост фиксированной длины: сдвигается
	// на каждом тике, так что график всегда показывает одно и то же окно
	// времени независимо от того, идёт лог или молчит.
	activity    []int
	activityNow int

	// scr — отрисованное содержимое поблочно. Держит зебру, позиции
	// проблемных записей (по ним прыгают n/N) и схлопывание повторов.
	scr screen

	showHelp    bool
	showSidebar bool

	// modPick — открыт список модулей для фильтрации. Модальный, как и
	// справка: пока он на экране, клавиши принадлежат ему.
	//
	// modList — снимок списка на момент открытия, а не живая статистика:
	// лог продолжает идти, и пересчёт на каждое нажатие переставлял бы
	// строки под курсором — Enter выбирал бы не то, что видно на экране.
	modPick   bool
	modCursor int
	modList   []modStat

	botPID   int
	uptime   string
	version  string
	botAlive bool

	// watchdog — если бот падает сам по себе (не через "4 · Остановить"),
	// перезапускает его без участия человека. Выключен по умолчанию: режим
	// "1 · Подключиться" по контракту не должен трогать процесс без явного
	// согласия, и молчаливый автозапуск нарушал бы его.
	watchdog           bool
	restarting         bool
	lastRestartAttempt time.Time

	follower *logfeed.Follower
	quitting bool

	// streamIssue — почему лог не читается, если не читается. Пустая строка
	// значит "всё в порядке". Замерший лог обязан объяснять себя сам: без
	// этого экран выглядел исправным, а строки просто переставали идти.
	streamIssue string
}

func NewViewer(opts ViewerOpts) *Viewer {
	ti := textinput.New()
	ti.Placeholder = "подстрока для поиска… (re:шаблон — регулярное выражение)"
	ti.Prompt = "/ "
	ti.CharLimit = 200

	// Настройки показа переживают перезапуск консоли: opts.ShowDebug остаётся
	// явным запросом вызывающего и побеждает, если он вообще что-то попросил
	// (true) — сохранённое "выключено" не должно тише пожелания того, кто
	// вызвал вьюер напрямую.
	saved := state.Load()
	return &Viewer{
		opts:        opts,
		search:      ti,
		parser:      logfeed.NewParser(),
		showDebug:   opts.ShowDebug || saved.ShowDebug,
		showSidebar: saved.ShowSidebar,
		minLevel:    logfeed.Level(saved.MinLevel),
		watchdog:    saved.Watchdog,
		version:     opts.Bot.Version(),
		activity:    make([]int, activityWindow),
	}
}

// saveState сохраняет текущие настройки показа — вызывается на выходе, а не
// на каждом переключении: писать файл при каждом нажатии клавиши незачем.
func (v *Viewer) saveState() {
	state.Save(state.State{
		ShowDebug:   v.showDebug,
		ShowSidebar: v.showSidebar,
		MinLevel:    int(v.minLevel),
		Watchdog:    v.watchdog,
	})
}

// activityWindow — сколько секунд истории показывает sparkline.
const activityWindow = 24

// ringCapacity — сколько сырых строк держится для пересборки экрана. Тем же
// числом читается стартовая история: раньше история бралась на 5000 строк,
// а буфер подрезался до 2000, и после первой же новой строки три тысячи
// строк истории исчезали из поиска — на экране они ещё были, а найти их
// через "/" уже было нельзя.
const ringCapacity = 5000

type tickMsg time.Time
type followerReadyMsg struct{ f *logfeed.Follower }
type lineMsg struct {
	raw string
	ok  bool
}
type watchdogRestartMsg struct{ res botproc.StartResult }

// watchdogCooldown — не чаще какого интервала вотчдог пробует поднять бота
// заново. Без паузы падение сразу после старта (сломанный venv, битый
// модуль) превращалось бы в цикл рестартов по разу в секунду, на каждом
// тике вьюера.
const watchdogCooldown = 5 * time.Second

func watchdogRestartCmd(bot *botproc.Manager) tea.Cmd {
	return func() tea.Msg { return watchdogRestartMsg{bot.Start()} }
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
			v.vp = viewport.New(v.logWidth(), vpH)
			v.ready = true
			v.loadHistory()
		} else {
			v.vp.Width, v.vp.Height = v.logWidth(), vpH
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
		// сдвигаем окно активности: текущая секунда закрывается и уезжает влево
		v.activity = append(v.activity[1:], v.activityNow)
		v.activityNow = 0
		v.refreshBotState()
		cmds := []tea.Cmd{tickCmd()}
		if cmd := v.maybeWatchdogRestart(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return v, tea.Batch(cmds...)

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

	case watchdogRestartMsg:
		// Провал не сообщается отдельно: следующий тик увидит бота всё ещё
		// не живым и попробует снова после watchdogCooldown — баннер о самой
		// попытке уже написан в лог, дублировать нечего.
		v.restarting = false
		return v, nil

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

	if v.modPick {
		return v.handleModPick(msg)
	}

	// Справка перекрывает экран — любая клавиша её закрывает, иначе
	// пришлось бы помнить ещё и как из неё выйти.
	if v.showHelp {
		v.showHelp = false
		return v, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		v.quitting = true
		v.saveState()
		if v.follower != nil {
			v.follower.Stop()
		}
		return v, tea.Quit
	case "?":
		v.showHelp = true
	case "n":
		v.jumpProblem(1)
	case "N":
		v.jumpProblem(-1)
	case "r":
		v.jumpRestart(1)
	case "R":
		v.jumpRestart(-1)
	case "w":
		// Порог показа по кругу: всё → warning и выше → error и выше.
		// Одна клавиша вместо ввода фильтра: "покажи только плохое" —
		// самый частый запрос к логу, и он не должен требовать печати.
		switch v.minLevel {
		case logfeed.LevelWarning:
			v.minLevel = logfeed.LevelError
		case logfeed.LevelError:
			v.minLevel = logfeed.LevelDebug
		default:
			v.minLevel = logfeed.LevelWarning
		}
		v.rebuildFromRing()
	case "m":
		// Выбор модуля: список берётся из панели, то есть из того, что
		// реально есть на экране, а не из захардкоженного перечня.
		if mods := v.scr.moduleStats(); len(mods) > 0 {
			if len(mods) > sidebarMaxModules {
				mods = mods[:sidebarMaxModules]
			}
			v.modList = mods
			v.modPick = true
			v.modCursor = 0
		}
	case "d":
		v.showDebug = !v.showDebug
		v.rebuildFromRing()
	case "a":
		// Автоперезапуск — ручной переключатель, а не поведение по
		// умолчанию: молчаливый рестарт процесса без спроса нарушал бы
		// контракт "1 · Подключиться, не трогая процесс".
		v.watchdog = !v.watchdog
	case "s":
		// Панель отдаёт логу 21 колонку — на длинных сообщениях и трейсбеках
		// иногда нужнее место, чем статистика.
		v.showSidebar = !v.showSidebar
		v.vp.Width = v.logWidth()
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
	if len(v.ring) > ringCapacity+500 {
		v.ring = v.ring[len(v.ring)-ringCapacity:]
	}

	rec, complete := v.parser.Feed(raw)
	if !complete {
		return
	}
	v.activityNow++
	w, e := v.parser.Counts()
	v.warn, v.err = w, e

	if logfeed.Visible(rec, v.showDebug, v.filter, v.minLevel) {
		v.scr.add(rec)
		v.pushContent()
	}
}

// jumpProblem перематывает к следующей (dir=+1) или предыдущей (dir=-1)
// записи уровня WARNING и выше относительно текущей позиции.
func (v *Viewer) jumpProblem(dir int) { v.jumpTo(v.scr.problemLines(), dir) }

// jumpRestart прыгает по баннерам перезапуска.
func (v *Viewer) jumpRestart(dir int) { v.jumpTo(v.scr.bannerLines(), dir) }

// jumpTo перематывает к ближайшей позиции из lines в направлении dir.
func (v *Viewer) jumpTo(problems []int, dir int) {
	if len(problems) == 0 {
		return
	}
	// Содержимое отдаётся во viewport с отступом сверху (applyContent
	// прижимает лог к низу), поэтому позиции записей надо сдвинуть на тот же
	// отступ — иначе прыжок промахивается ровно на высоту пустоты.
	offset := 0
	if gap := v.vp.Height - v.scr.totalLines(); gap > 0 {
		offset = gap
	}
	cur := v.vp.YOffset
	if dir > 0 {
		for _, ln := range problems {
			if ln+offset > cur {
				v.vp.SetYOffset(ln + offset)
				return
			}
		}
		v.vp.GotoBottom()
		return
	}
	for i := len(problems) - 1; i >= 0; i-- {
		if problems[i]+offset < cur {
			v.vp.SetYOffset(problems[i] + offset)
			return
		}
	}
	v.vp.GotoTop()
}

// maybeWatchdogRestart решает, пора ли поднимать бота заново, и если да —
// сразу отмечает попытку в логе и возвращает команду запуска. Вызывается
// каждый тик, поэтому сам решает про кулдаун — a не полагается на то, что
// его позовут реже.
func (v *Viewer) maybeWatchdogRestart() tea.Cmd {
	if !v.watchdog || v.botAlive || v.restarting {
		return nil
	}
	if time.Since(v.lastRestartAttempt) < watchdogCooldown {
		return nil
	}
	v.restarting = true
	v.lastRestartAttempt = time.Now()
	v.appendWatchdogBanner()
	return watchdogRestartCmd(v.opts.Bot)
}

// appendWatchdogBanner отмечает в логе сам факт автоматического
// вмешательства — отдельно от обычного баннера перезапуска (он появится
// следом сам, как только бот допишет "Got DB"): без этой строки падение и
// самостоятельный подъём бота выглядели бы неотличимо от ручного рестарта.
func (v *Viewer) appendWatchdogBanner() {
	line := lipgloss.NewStyle().Foreground(theme.Red).Bold(true).
		Render(fmt.Sprintf("  ⚠  WATCHDOG: бот не отвечает — перезапускаю  ·  %s", time.Now().Format("15:04:05")))
	v.scr.addRaw("\n" + line + "\n")
	v.pushContent()
}

func (v *Viewer) appendRestartBanner() {
	line := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).
		Render(fmt.Sprintf("  ⟳  ПЕРЕЗАПУСК  ·  %s  ·  pid %d", time.Now().Format("15:04:05"), v.botPID))
	v.scr.addRaw("\n" + line + "\n")
	v.pushContent()
	v.version = v.opts.Bot.Version()
}

// pushContent отдаёт накопленные блоки во viewport. Если пользователь ничего
// не пролистывал и был внизу — остаёмся внизу и после добавления (обычное
// поведение живого лога); если он поднялся вверх почитать историю — позицию
// не трогаем, новая строка просто ждёт ниже, как в любом приличном
// log-вьюере.
func (v *Viewer) pushContent() {
	if !v.ready {
		return
	}
	stuck := v.vp.AtBottom()
	v.applyContent()
	if stuck {
		v.vp.GotoBottom()
	}
}

// applyContent отдаёт накопленный текст во viewport, прижимая его к низу:
// пока строк меньше, чем высота окна, сверху добавляются пустые. Иначе
// свежий лог висел бы у верхнего края с пустотой под ним, а привычно —
// когда новые строки приходят снизу, у самой статус-строки, как в терминале.
// Отступ добавляется только на отдаче: блоки остаются чистыми, иначе пустые
// строки копились бы при каждом дописывании.
func (v *Viewer) applyContent() {
	content := v.scr.text()
	if gap := v.vp.Height - v.scr.totalLines(); gap > 0 {
		content = strings.Repeat("\n", gap) + content
	}
	v.vp.SetContent(content)
}

// rebuildFromRing пересобирает весь видимый контент заново из буфера сырых
// строк — нужно и при смене фильтра, и при переключении DEBUG, поскольку
// оба меняют, что вообще должно быть видно на уже нарисованном экране.
func (v *Viewer) rebuildFromRing() {
	v.parser = logfeed.NewParser()
	v.scr.reset(v.logWidth())
	for _, rl := range v.ring {
		rec, complete := v.parser.Feed(rl.raw)
		if !complete {
			continue
		}
		if logfeed.Visible(rec, v.showDebug, v.filter, v.minLevel) {
			v.scr.add(rec)
		}
	}
	w, e := v.parser.Counts()
	v.warn, v.err = w, e
	if v.ready {
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
	lines := logfeed.TailLines(v.opts.Bot.LogFile, ringCapacity)
	// Прочитанные строки идут и в кольцевой буфер: иначе переключение
	// DEBUG/фильтра (rebuildFromRing читает только v.ring) не сможет
	// вернуть историю обратно — она просто исчезнет из вида безвозвратно,
	// хотя формально уже была загружена.
	for _, l := range lines {
		v.ring = append(v.ring, ringLine{l})
	}
	hp := logfeed.NewParser()
	v.scr.reset(v.logWidth())
	for _, l := range lines {
		rec, complete := hp.Feed(l)
		if !complete {
			continue
		}
		if logfeed.Visible(rec, v.showDebug, "", v.minLevel) {
			v.scr.add(rec)
		}
	}
	// Обрезка идёт после схлопывания: "40 записей истории" считаются по
	// показанным записям, иначе стоило боту засыпать повторами — и от
	// истории на экране осталась бы горстка строк.
	v.scr.trimTo(v.opts.History)

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
	if v.follower != nil {
		if ok, reason, since := v.follower.Alive(); !ok {
			v.streamIssue = fmt.Sprintf("%s  ·  %s", reason, humanSince(time.Since(since)))
		} else {
			v.streamIssue = ""
		}
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
	body := v.vp.View()
	if v.sidebarVisible() {
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			v.renderSidebar(v.vp.Height),
			sidebarDivider(v.vp.Height),
			body)
	}
	// Справка перекрывает всё тело целиком, вместе с панелью: это модальное
	// окно, а не ещё одна колонка.
	if v.showHelp {
		body = v.renderHelp()
	}
	if v.modPick {
		body = v.renderModPick()
	}
	return v.renderHeader() + "\n" + body + "\n" + v.renderFooter()
}

// humanSince — сколько времени длится состояние. Короткие подписи в шапке
// важнее точности: "3м" читается на лету, "3m17.4s" — нет.
func humanSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d м", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d ч %d м", int(d.Hours()), int(d.Minutes())%60)
	}
}

// handleModPick — клавиши списка модулей. Выбор ставит имя модуля обычным
// фильтром: отдельного «режима модуля» нет, поэтому снимается он тем же
// пустым Enter в поиске, что и любой другой фильтр, и объяснять две разные
// механики не нужно.
func (v *Viewer) handleModPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	mods := v.modList
	if len(mods) == 0 {
		v.modPick = false
		return v, nil
	}
	switch msg.String() {
	case "esc", "q", "m":
		v.modPick = false
	case "up", "k":
		v.modCursor = (v.modCursor - 1 + len(mods)) % len(mods)
	case "down", "j":
		v.modCursor = (v.modCursor + 1) % len(mods)
	case "enter":
		if v.modCursor < len(mods) {
			v.filter = mods[v.modCursor].name
			v.rebuildFromRing()
		}
		v.modPick = false
	}
	return v, nil
}
