package telegram

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Распознавание обычных фраз: «покажи ошибки», «перезапусти», «тишина на час». Слэш-команды
// остаются, но пользоваться ими не обязательно. Разбор намеренно простой и предсказуемый —
// основы слов, а не нейросеть: ответ на «выключи» должен быть одинаковым всегда. Опасные
// действия при этом всё равно требуют подтверждения.

// IntentKind — что человек просит сделать.
type IntentKind string

const (
	IntentYes      IntentKind = "yes"
	IntentNo       IntentKind = "no"
	IntentStatus   IntentKind = "status"
	IntentRun      IntentKind = "run"
	IntentRestart  IntentKind = "restart"
	IntentStop     IntentKind = "stop"
	IntentUpdate   IntentKind = "update"
	IntentLogs     IntentKind = "logs"
	IntentErrors   IntentKind = "errors"
	IntentSearch   IntentKind = "search"
	IntentMute     IntentKind = "mute"
	IntentUnmute   IntentKind = "unmute"
	IntentHelp     IntentKind = "help"
	IntentMenu     IntentKind = "menu"
	IntentApp      IntentKind = "app"
	IntentHello    IntentKind = "hello"
	IntentThanks   IntentKind = "thanks"
	IntentCancel   IntentKind = "cancel"
	IntentFallback IntentKind = "fallback"
)

// Intent — разобранная фраза: вид запроса и его параметры.
type Intent struct {
	Kind IntentKind
	// Параметры просмотра журнала.
	Limit  int
	Since  time.Duration
	Module string
	Level  string
	// Text — что искать (поиск) или остаток фразы.
	Text string
	// Параметры тишины.
	Mute    time.Duration
	MuteSet bool
	MuteAll bool
	// Words — слова фразы для подбора модуля по именам, известным журналу.
	Words []string
}

var wordSplit = regexp.MustCompile(`[^\p{L}\p{N}_.@:/-]+`)

// Normalize приводит фразу к нижнему регистру без ё и лишних знаков.
func Normalize(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	return strings.TrimSpace(text)
}

// Tokenize делит нормализованную фразу на слова.
func Tokenize(text string) []string {
	var words []string
	for _, w := range wordSplit.Split(text, -1) {
		w = strings.Trim(w, "-./:")
		if w != "" {
			words = append(words, w)
		}
	}
	return words
}

// HasStem проверяет, начинается ли хоть одно слово с одной из основ.
func HasStem(words []string, stems ...string) bool {
	for _, w := range words {
		for _, s := range stems {
			if strings.HasPrefix(w, s) {
				return true
			}
		}
	}
	return false
}

// HasWord проверяет, есть ли во фразе одно из слов целиком.
func HasWord(words []string, wanted ...string) bool {
	for _, w := range words {
		for _, x := range wanted {
			if w == x {
				return true
			}
		}
	}
	return false
}

// fillers — слова вежливости и обращения, которые не меняют смысла.
var fillers = map[string]bool{
	"бот": true, "бот,": true, "хероку": true, "heroku": true, "эй": true, "слушай": true, "пожалуйста": true,
	"плиз": true, "пж": true, "пожалуста": true, "а": true, "ну": true, "давай": true, "ка": true, "мне": true,
	"можешь": true, "можно": true, "будь": true, "добр": true, "добра": true, "быстро": true, "срочно": true,
}

var (
	numberRe = regexp.MustCompile(`\b(\d{1,3})\b`)
	sinceRe  = regexp.MustCompile(`за\s+(?:последн\p{L}*\s+)?(?:(\d+)\s*)?(час\p{L}*|мин\p{L}*|сут\p{L}*|день|дн\p{L}*|ч|м|d|h|m)`)
	moduleRe = regexp.MustCompile(`(?:module|модул\p{L}*)[:\s]+([\p{L}\p{N}_.-]+)`)
	// \b в Go понимает только латиницу, поэтому границу слова задаём явно.
	searchVerb = regexp.MustCompile(`^(найд\p{L}*|найти|поищ\p{L}*|ищи|искать|поиск|grep|search|find)(?:\s+|[:,-]+\s*|$)`)
)

// ParseIntent определяет, чего хочет человек. Второе значение false — фраза пустая.
func ParseIntent(raw string) (Intent, bool) {
	text := Normalize(raw)
	if text == "" {
		return Intent{}, false
	}
	all := Tokenize(text)
	var words []string
	for _, w := range all {
		if !fillers[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return Intent{Kind: IntentHello}, true
	}
	it := Intent{Words: all}
	trimmed := strings.Join(words, " ")

	// Короткие ответы на вопрос подтверждения.
	if len(words) <= 2 {
		switch strings.Join(words, " ") {
		case "да", "ага", "угу", "ок", "окей", "ok", "okay", "yes", "y", "подтверждаю", "точно", "конечно", "да давай", "да точно", "да подтверждаю", "валяй", "действуй":
			it.Kind = IntentYes
			return it, true
		case "нет", "неа", "не", "no", "n", "отмена", "отмени", "отменить", "cancel", "не надо", "не нужно", "не стоит", "не а", "стоп", "стой", "хватит":
			it.Kind = IntentNo
			return it, true
		}
	}

	// Тишина и уведомления идут первыми: «выключи уведомления» не должно останавливать бота.
	notif := HasStem(words, "уведомл", "оповещ", "алерт", "алёрт", "пуш", "тревог")
	quiet := HasStem(words, "тишин", "помолч", "замолч", "заглуш", "молчи", "mute", "тихо", "отстан", "тихий")
	dontDisturb := strings.Contains(trimmed, "не беспоко") || strings.Contains(trimmed, "не отвлека") || strings.Contains(trimmed, "не пиши") || strings.Contains(trimmed, "не присылай") || strings.Contains(trimmed, "не шли") || strings.Contains(trimmed, "хватит писать")
	on := HasStem(words, "включ", "верни", "размут", "unmute", "говор", "возобнов", "разбуд", "вклю")
	off := HasStem(words, "выключ", "отключ", "останов", "останавл", "приостанов", "пауз", "убер")
	if HasStem(words, "размут", "unmute") || strings.Contains(trimmed, "не молчи") || strings.Contains(trimmed, "хватит молч") ||
		(notif && on && !off) || (quiet && on) {
		it.Kind = IntentUnmute
		return it, true
	}
	if quiet || dontDisturb || (notif && (off || HasStem(words, "пауз", "тише"))) {
		it.Kind = IntentMute
		it.Mute, it.MuteSet = ParseMuteDuration(text, time.Now())
		it.MuteAll = HasStem(words, "совсем", "полностью", "вообще") || strings.Contains(trimmed, "даже критич")
		return it, true
	}

	// Отрицание перед действием: «не перезапускай», «не надо останавливать».
	if negated(words) && HasStem(words, "перезапуск", "рестарт", "перезагруз", "останов", "останавл", "выключ", "выруб", "запуск", "запуст", "обнов", "restart", "stop") {
		it.Kind = IntentCancel
		return it, true
	}

	switch {
	case HasStem(words, "меню", "menu", "дашборд", "dashboard") && len(words) <= 4:
		it.Kind = IntentMenu
		return it, true
	case HasStem(words, "помо", "справк", "помощ", "команд", "help", "умеешь") && len(words) <= 4:
		it.Kind = IntentHelp
		return it, true
	case len(words) <= 2 && (strings.HasPrefix(trimmed, "привет") || strings.HasPrefix(trimmed, "здаров") || strings.HasPrefix(trimmed, "здравств") || strings.HasPrefix(trimmed, "добрый") || strings.HasPrefix(trimmed, "добрая") || trimmed == "hi" || trimmed == "hello" || trimmed == "хай" || trimmed == "ку"):
		it.Kind = IntentHello
		return it, true
	case len(words) <= 3 && HasStem(words, "спасиб", "благодар", "thanks", "thx", "спс"):
		it.Kind = IntentThanks
		return it, true
	}

	if loc := searchVerb.FindStringIndex(trimmed); loc != nil {
		it.Kind = IntentSearch
		it.Text = strings.TrimSpace(trimmed[loc[1]:])
		it.Text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(it.Text, "в логах"), "в журнале"))
		it.Text = strings.Trim(strings.TrimSpace(strings.TrimPrefix(it.Text, "по")), `"'«»`)
		if it.Text == "" {
			it.Kind = IntentLogs
		}
		FillView(&it, text)
		return it, true
	}

	statusWord := HasStem(words, "статус", "состоян", "uptime", "аптайм", "ping", "пинг", "нагрузк", "сервер", "жив", "работа", "работаеш", "алив") ||
		strings.Contains(trimmed, "как дела") || strings.Contains(trimmed, "как ты") || strings.Contains(trimmed, "как бот") || strings.Contains(trimmed, "что с бот") || strings.Contains(trimmed, "что там с")
	errorWord := HasStem(words, "ошиб", "упал", "упало", "упасть", "сломал", "сломан", "traceback", "трейсбек", "исключен", "креш", "краш", "error", "проблем", "глюч", "баг") ||
		strings.Contains(trimmed, "что случилось") || strings.Contains(trimmed, "что не так") || strings.Contains(trimmed, "что пошло не так")
	logWord := HasStem(words, "лог", "журнал", "log", "запис", "событи", "последн") || strings.Contains(trimmed, "что происходит") || strings.Contains(trimmed, "что там в")

	switch {
	case HasStem(words, "перезапуск", "рестарт", "перезагруз", "restart", "перезапуст"):
		it.Kind = IntentRestart
	case HasStem(words, "останов", "останавл", "выключ", "выруб", "заверши", "прибей", "убей", "стопни", "остановка") || HasWord(words, "stop", "kill"):
		it.Kind = IntentStop
	case (HasStem(words, "обнов", "апдейт") || HasWord(words, "update", "upgrade")) && !statusWord && !HasStem(words, "лог", "журнал", "log"):
		it.Kind = IntentUpdate
	case strings.Contains(trimmed, "нов") && HasStem(words, "верси") || HasStem(words, "верси") && (HasStem(words, "какая", "последн", "актуальн", "свеж") || len(words) <= 2):
		it.Kind = IntentUpdate
	case errorWord:
		it.Kind = IntentErrors
	case logWord:
		it.Kind = IntentLogs
	case HasStem(words, "запуск", "запуст", "стартуй", "старт", "подним", "включ", "run") || HasWord(words, "start", "go"):
		it.Kind = IntentRun
	case statusWord:
		it.Kind = IntentStatus
	case HasStem(words, "панел", "приложен", "app", "мини", "webapp"):
		it.Kind = IntentApp
	default:
		it.Kind = IntentFallback
		it.Text = strings.Trim(strings.TrimSpace(raw), `"'«»`)
		return it, true
	}
	FillView(&it, text)
	if it.Kind == IntentErrors && it.Level == "" {
		it.Level = "ERROR"
	}
	return it, true
}

// negated — перед действием стоит «не»: «не перезапускай», «не надо останавливать».
func negated(words []string) bool {
	for i, w := range words {
		if w == "не" || w == "нет" || w == "ненадо" || w == "no" || w == "dont" || w == "don't" {
			return i < len(words)-1
		}
		if strings.HasPrefix(w, "отмен") || strings.HasPrefix(w, "забуд") {
			return true
		}
	}
	return false
}

// FillView достаёт параметры просмотра журнала: сколько записей, за какое время, какой модуль
// и уровень. Модуль без префикса подбирается позже по списку известных журналу имён.
func FillView(it *Intent, text string) {
	if m := sinceRe.FindStringSubmatch(text); m != nil {
		n := 1
		if m[1] != "" {
			n, _ = strconv.Atoi(m[1])
		}
		unit := m[2]
		switch {
		case strings.HasPrefix(unit, "час"), unit == "ч", unit == "h":
			it.Since = time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "сут"), strings.HasPrefix(unit, "дн"), unit == "день", unit == "d":
			it.Since = time.Duration(n) * 24 * time.Hour
		default:
			it.Since = time.Duration(n) * time.Minute
		}
		text = strings.Replace(text, m[0], " ", 1)
	}
	if m := numberRe.FindStringSubmatch(text); m != nil {
		it.Limit, _ = strconv.Atoi(m[1])
	}
	if m := moduleRe.FindStringSubmatch(text); m != nil {
		it.Module = m[1]
	}
	switch {
	case HasStem(it.Words, "предупрежд", "warn", "варнинг"):
		it.Level = "WARNING"
	case HasStem(it.Words, "ошиб", "error"):
		it.Level = "ERROR"
	}
}

// ModuleFrom подбирает модуль из слов фразы по именам, известным журналу: целиком или по
// последнему сегменту («updates» → «heroku.updates»). Слова вроде «логи» не совпадут ни с чем.
func ModuleFrom(words []string, known []string) string {
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		for _, name := range known {
			lower := strings.ToLower(name)
			if lower == w || strings.HasSuffix(lower, "."+w) {
				return name
			}
		}
	}
	return ""
}

// StripMention убирает обращение к боту (@name) и возвращает, было ли оно.
func StripMention(text, username string) (string, bool) {
	if username == "" {
		return text, false
	}
	tag := "@" + strings.ToLower(username)
	lower := strings.ToLower(text)
	if !strings.Contains(lower, tag) {
		return text, false
	}
	var out []rune
	for i := 0; i < len(text); {
		if strings.HasPrefix(lower[i:], tag) {
			i += len(tag)
			continue
		}
		r := []rune(text[i:])[0]
		out = append(out, r)
		i += len(string(r))
	}
	return strings.TrimFunc(string(out), func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == ':' }), true
}
