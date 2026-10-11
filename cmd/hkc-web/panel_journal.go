package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"heroku-console/logfeed"
)

// Журнал панели — второй источник ленты журнала рядом с heroku.log. В него пишутся вывод
// процесса бота, которого нет в heroku.log (падение до запуска логирования, фатальный traceback,
// «Killed»), действия людей и автоматики панели и её события (бот упал, автовосстановление,
// обновление). Формат строк тот же, что у бота, поэтому лента, поиск, шкала и Telegram читают
// его теми же средствами.

const (
	panelJournalName = "panel.log"
	// panelJournalMax — размер файла, после которого он переименовывается в panel.log.1.
	panelJournalMax = 4 << 20
	// Модули записей журнала панели: по ним их можно отфильтровать (module:process).
	moduleProcess = "process"
	modulePanel   = "panel"
)

type panelJournal struct {
	mu   sync.Mutex
	path string
	max  int64
	subs map[chan logfeed.Entry]struct{}
}

func newPanelJournal(path string) *panelJournal {
	return &panelJournal{path: path, max: panelJournalMax, subs: map[chan logfeed.Entry]struct{}{}}
}

// write добавляет запись и рассылает её подписчикам потока журнала. Перевод строки внутри
// сообщения заменяется пробелом: продолжения записи идут в trace.
func (p *panelJournal) write(at time.Time, level, module, message string, trace []string) {
	if p == nil {
		return
	}
	message = strings.Join(strings.Fields(strings.ReplaceAll(message, "\n", " ")), " ")
	if message == "" {
		message = "(пусто)"
	}
	stamp := at.Local().Format("2006-01-02 15:04:05")
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %s: %s\n", stamp, level, module, message)
	for _, line := range trace {
		line = strings.TrimRight(line, "\r\n")
		// Строка вывода, похожая на заголовок записи, иначе разорвала бы запись на две.
		if _, ok := logfeed.ParseLine(line); ok {
			line = " " + line
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if info, err := os.Stat(p.path); err == nil && info.Size()+int64(b.Len()) > p.max {
		_ = os.Rename(p.path, p.path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		log.Printf("журнал панели: %v", err)
		return
	}
	f, err := os.OpenFile(p.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("журнал панели: %v", err)
		return
	}
	_, err = f.WriteString(b.String())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		log.Printf("журнал панели: %v", err)
		return
	}
	// Подписчикам уходит та же запись, что прочитает лента: с ID и разобранным временем.
	entries := logfeed.ParseEntries(strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n"))
	if len(entries) == 0 {
		return
	}
	entry := entries[0]
	for ch := range p.subs {
		select {
		case ch <- entry:
		default: // медленный читатель пропускает запись, но не тормозит панель
		}
	}
}

// subscribe возвращает канал новых записей журнала панели; cancel отписывает.
func (p *panelJournal) subscribe() (<-chan logfeed.Entry, func()) {
	ch := make(chan logfeed.Entry, 64)
	if p == nil {
		return ch, func() {}
	}
	p.mu.Lock()
	p.subs[ch] = struct{}{}
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.subs, ch)
		p.mu.Unlock()
	}
}

// journal — журнал для ленты: heroku.log с ротациями и архивом и журнал панели.
func (s *server) journal() logfeed.Journal {
	bot := logfeed.Source{Path: s.bot.LogFile, Archive: s.logArchiveDir()}
	if s.panelLog == nil {
		return logfeed.Journal{Sources: []logfeed.Source{bot}}
	}
	return logfeed.Journal{Sources: []logfeed.Source{bot, {Path: s.panelLog.path, Archive: s.logArchiveDir()}}}
}

// --- События и действия ---

// panelEventLevel переводит важность уведомления в уровень записи журнала.
func panelEventLevel(severity string) string {
	switch severity {
	case "critical":
		return "ERROR"
	case "warning":
		return "WARNING"
	}
	return "INFO"
}

// recordPanelEvent пишет в журнал событие панели: падение и запуск бота, автовосстановление,
// пороги ресурсов, выход обновления. Проверочное уведомление в журнал не попадает.
func (s *server) recordPanelEvent(event alertEvent) {
	if s.panelLog == nil || event.Kind == "test" {
		return
	}
	message := event.Title
	if detail := strings.TrimSpace(event.Message); detail != "" {
		first, _, _ := strings.Cut(detail, "\n")
		message += " — " + first
	}
	at := event.Time
	if at.IsZero() {
		at = time.Now()
	}
	s.panelLog.write(at, panelEventLevel(event.Severity), modulePanel, truncateRunes(message, 500), nil)
}

// panelActionLabels — действия, которые важны рядом с записями бота: кто и когда управлял
// ботом, панелью и журналом. Вход, пользователи, инвайты и токены остаются только в аудите.
var panelActionLabels = map[string]string{
	"bot.start":         "Запуск бота",
	"bot.stop":          "Остановка бота",
	"bot.restart":       "Перезапуск бота",
	"bot.recover":       "Автовосстановление перезапустило бота",
	"panel.restart":     "Перезапуск панели",
	"update.install":    "Установка обновления панели",
	"update.rollback":   "Возврат прошлой версии панели",
	"update.cancel":     "Отмена обновления панели",
	"config.update":     "Изменены настройки бота",
	"config.delete":     "Удалён параметр настроек бота",
	"watchdog.update":   "Изменено автовосстановление",
	"watchdog.resume":   "Сброшена серия попыток автовосстановления",
	"alerts.update":     "Изменены уведомления",
	"telegram.settings": "Изменено управление из Telegram",
	"telegram.mute":     "Включена тишина уведомлений",
	"telegram.unmute":   "Уведомления снова включены",
	"backup.restore":    "Восстановлена копия доступа",
	"log.level":         "Изменён уровень журнала модуля",
	"log.rule":          "Изменено правило журнала",
}

// recordPanelAction пишет в журнал действие из аудита, если оно из списка panelActionLabels.
func (s *server) recordPanelAction(event auditEvent) {
	if s.panelLog == nil {
		return
	}
	label, ok := panelActionLabels[event.Action]
	if !ok && strings.HasPrefix(event.Action, "update.") {
		label, ok = "Обновление панели", true
	}
	if !ok {
		return
	}
	message := label
	if actor := strings.TrimSpace(event.Actor); actor != "" {
		message += " · " + actor
	}
	if detail := strings.TrimSpace(event.Detail); detail != "" {
		message += ": " + detail
	}
	level := "INFO"
	if event.Action == "bot.recover" || event.Action == "bot.stop" {
		level = "WARNING"
	}
	at := event.Time
	if at.IsZero() {
		at = time.Now()
	}
	s.panelLog.write(at, level, modulePanel, truncateRunes(message, 500), nil)
}

// truncateRunes обрезает строку до n символов, не разрывая UTF-8.
func truncateRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n-1]) + "…"
}

// --- Архив ---

// logArchiveDir — каталог сжатых копий ротаций журнала; пусто, если архив выключен.
func (s *server) logArchiveDir() string {
	if s.dataDir == "" || s.archiveLimits().disabled {
		return ""
	}
	return filepath.Join(s.dataDir, "log-archive")
}

type archiveLimits struct {
	bytes    int64
	age      time.Duration
	disabled bool
}

// archiveLimits читает пределы архива из окружения: HKC_LOG_ARCHIVE_MB (по умолчанию 300) и
// HKC_LOG_ARCHIVE_DAYS (по умолчанию 30). HKC_LOG_ARCHIVE_MB=0 выключает архив.
func (s *server) archiveLimits() archiveLimits {
	limits := archiveLimits{bytes: 300 << 20, age: 30 * 24 * time.Hour}
	if raw := strings.TrimSpace(os.Getenv("HKC_LOG_ARCHIVE_MB")); raw != "" {
		if mb, err := strconv.Atoi(raw); err == nil && mb >= 0 {
			limits.bytes = int64(mb) << 20
			limits.disabled = mb == 0
		}
	}
	if raw := strings.TrimSpace(os.Getenv("HKC_LOG_ARCHIVE_DAYS")); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days > 0 {
			limits.age = time.Duration(days) * 24 * time.Hour
		}
	}
	return limits
}

// runLogArchive раз в 30 секунд снимает сжатую копию свежей ротации heroku.log (бот держит одну
// ротацию и при следующей перезаписывает её) и журнала панели, а также убирает старые копии.
func (s *server) runLogArchive(stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if dir := s.logArchiveDir(); dir != "" {
			limits := s.archiveLimits()
			for _, src := range s.journal().Sources {
				if err := logfeed.ArchiveRotations(src, limits.bytes, limits.age, time.Now()); err != nil {
					log.Printf("архив журнала: %v", err)
				}
			}
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
