package main

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

type intentKind string

const (
	intentYes      intentKind = "yes"
	intentNo       intentKind = "no"
	intentStatus   intentKind = "status"
	intentRun      intentKind = "run"
	intentRestart  intentKind = "restart"
	intentStop     intentKind = "stop"
	intentUpdate   intentKind = "update"
	intentLogs     intentKind = "logs"
	intentErrors   intentKind = "errors"
	intentSearch   intentKind = "search"
	intentMute     intentKind = "mute"
	intentUnmute   intentKind = "unmute"
	intentHelp     intentKind = "help"
	intentApp      intentKind = "app"
	intentHello    intentKind = "hello"
	intentThanks   intentKind = "thanks"
	intentCancel   intentKind = "cancel"
	intentFallback intentKind = "fallback"
)

type intent struct {
	kind intentKind
	// Параметры просмотра журнала.
	limit  int
	since  time.Duration
	module string
	level  string
	// text — что искать (поиск) или остаток фразы.
	text string
	// Параметры тишины.
	mute    time.Duration
	muteSet bool
	muteAll bool
	// words — слова фразы для подбора модуля по именам, известным журналу.
	words []string
}

var wordSplit = regexp.MustCompile(`[^\p{L}\p{N}_.@:/-]+`)

// normalize приводит фразу к нижнему регистру без ё и лишних знаков.
func normalize(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	return strings.TrimSpace(text)
}

func tokenize(text string) []string {
	var words []string
	for _, w := range wordSplit.Split(text, -1) {
		w = strings.Trim(w, "-./:")
		if w != "" {
			words = append(words, w)
		}
	}
	return words
}

// hasStem проверяет, начинается ли хоть одно слово с одной из основ.
func hasStem(words []string, stems ...string) bool {
	for _, w := range words {
		for _, s := range stems {
			if strings.HasPrefix(w, s) {
				return true
			}
		}
	}
	return false
}

func hasWord(words []string, wanted ...string) bool {
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

// parseIntent определяет, чего хочет человек. Второе значение false — фраза пустая.
func parseIntent(raw string) (intent, bool) {
	text := normalize(raw)
	if text == "" {
		return intent{}, false
	}
	all := tokenize(text)
	var words []string
	for _, w := range all {
		if !fillers[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return intent{kind: intentHello}, true
	}
	it := intent{words: all}
	trimmed := strings.Join(words, " ")

	// Короткие ответы на вопрос подтверждения.
	if len(words) <= 2 {
		switch strings.Join(words, " ") {
		case "да", "ага", "угу", "ок", "окей", "ok", "okay", "yes", "y", "подтверждаю", "точно", "конечно", "да давай", "да точно", "да подтверждаю", "валяй", "действуй":
			it.kind = intentYes
			return it, true
		case "нет", "неа", "не", "no", "n", "отмена", "отмени", "отменить", "cancel", "не надо", "не нужно", "не стоит", "не а", "стоп", "стой", "хватит":
			it.kind = intentNo
			return it, true
		}
	}

	// Тишина и уведомления идут первыми: «выключи уведомления» не должно останавливать бота.
	notif := hasStem(words, "уведомл", "оповещ", "алерт", "алёрт", "пуш", "тревог")
	quiet := hasStem(words, "тишин", "помолч", "замолч", "заглуш", "молчи", "mute", "тихо", "отстан", "тихий")
	dontDisturb := strings.Contains(trimmed, "не беспоко") || strings.Contains(trimmed, "не отвлека") || strings.Contains(trimmed, "не пиши") || strings.Contains(trimmed, "не присылай") || strings.Contains(trimmed, "не шли") || strings.Contains(trimmed, "хватит писать")
	on := hasStem(words, "включ", "верни", "размут", "unmute", "говор", "возобнов", "разбуд", "вклю")
	off := hasStem(words, "выключ", "отключ", "останов", "останавл", "приостанов", "пауз", "убер")
	if hasStem(words, "размут", "unmute") || strings.Contains(trimmed, "не молчи") || strings.Contains(trimmed, "хватит молч") ||
		(notif && on && !off) || (quiet && on) {
		it.kind = intentUnmute
		return it, true
	}
	if quiet || dontDisturb || (notif && (off || hasStem(words, "пауз", "тише"))) {
		it.kind = intentMute
		it.mute, it.muteSet = parseMuteDuration(text, time.Now())
		it.muteAll = hasStem(words, "совсем", "полностью", "вообще") || strings.Contains(trimmed, "даже критич")
		return it, true
	}

	// Отрицание перед действием: «не перезапускай», «не надо останавливать».
	if negated(words) && hasStem(words, "перезапуск", "рестарт", "перезагруз", "останов", "останавл", "выключ", "выруб", "запуск", "запуст", "обнов", "restart", "stop") {
		it.kind = intentCancel
		return it, true
	}

	switch {
	case hasStem(words, "помо", "справк", "помощ", "команд", "help", "умеешь") && len(words) <= 4:
		it.kind = intentHelp
		return it, true
	case len(words) <= 2 && (strings.HasPrefix(trimmed, "привет") || strings.HasPrefix(trimmed, "здаров") || strings.HasPrefix(trimmed, "здравств") || strings.HasPrefix(trimmed, "добрый") || strings.HasPrefix(trimmed, "добрая") || trimmed == "hi" || trimmed == "hello" || trimmed == "хай" || trimmed == "ку"):
		it.kind = intentHello
		return it, true
	case len(words) <= 3 && hasStem(words, "спасиб", "благодар", "thanks", "thx", "спс"):
		it.kind = intentThanks
		return it, true
	}

	if loc := searchVerb.FindStringIndex(trimmed); loc != nil {
		it.kind = intentSearch
		it.text = strings.TrimSpace(trimmed[loc[1]:])
		it.text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(it.text, "в логах"), "в журнале"))
		it.text = strings.Trim(strings.TrimSpace(strings.TrimPrefix(it.text, "по")), `"'«»`)
		if it.text == "" {
			it.kind = intentLogs
		}
		fillView(&it, text)
		return it, true
	}

	statusWord := hasStem(words, "статус", "состоян", "uptime", "аптайм", "ping", "пинг", "нагрузк", "сервер", "жив", "работа", "работаеш", "алив") ||
		strings.Contains(trimmed, "как дела") || strings.Contains(trimmed, "как ты") || strings.Contains(trimmed, "как бот") || strings.Contains(trimmed, "что с бот") || strings.Contains(trimmed, "что там с")
	errorWord := hasStem(words, "ошиб", "упал", "упало", "упасть", "сломал", "сломан", "traceback", "трейсбек", "исключен", "креш", "краш", "error", "проблем", "глюч", "баг") ||
		strings.Contains(trimmed, "что случилось") || strings.Contains(trimmed, "что не так") || strings.Contains(trimmed, "что пошло не так")
	logWord := hasStem(words, "лог", "журнал", "log", "запис", "событи", "последн") || strings.Contains(trimmed, "что происходит") || strings.Contains(trimmed, "что там в")

	switch {
	case hasStem(words, "перезапуск", "рестарт", "перезагруз", "restart", "перезапуст"):
		it.kind = intentRestart
	case hasStem(words, "останов", "останавл", "выключ", "выруб", "заверши", "прибей", "убей", "стопни", "остановка") || hasWord(words, "stop", "kill"):
		it.kind = intentStop
	case (hasStem(words, "обнов", "апдейт") || hasWord(words, "update", "upgrade")) && !statusWord && !hasStem(words, "лог", "журнал", "log"):
		it.kind = intentUpdate
	case strings.Contains(trimmed, "нов") && hasStem(words, "верси") || hasStem(words, "верси") && (hasStem(words, "какая", "последн", "актуальн", "свеж") || len(words) <= 2):
		it.kind = intentUpdate
	case errorWord:
		it.kind = intentErrors
	case logWord:
		it.kind = intentLogs
	case hasStem(words, "запуск", "запуст", "стартуй", "старт", "подним", "включ", "run") || hasWord(words, "start", "go"):
		it.kind = intentRun
	case statusWord:
		it.kind = intentStatus
	case hasStem(words, "панел", "приложен", "app", "мини", "webapp"):
		it.kind = intentApp
	default:
		it.kind = intentFallback
		it.text = strings.Trim(strings.TrimSpace(raw), `"'«»`)
		return it, true
	}
	fillView(&it, text)
	if it.kind == intentErrors && it.level == "" {
		it.level = "ERROR"
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

// fillView достаёт параметры просмотра журнала: сколько записей, за какое время, какой модуль
// и уровень. Модуль без префикса подбирается позже по списку известных журналу имён.
func fillView(it *intent, text string) {
	if m := sinceRe.FindStringSubmatch(text); m != nil {
		n := 1
		if m[1] != "" {
			n, _ = strconv.Atoi(m[1])
		}
		unit := m[2]
		switch {
		case strings.HasPrefix(unit, "час"), unit == "ч", unit == "h":
			it.since = time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "сут"), strings.HasPrefix(unit, "дн"), unit == "день", unit == "d":
			it.since = time.Duration(n) * 24 * time.Hour
		default:
			it.since = time.Duration(n) * time.Minute
		}
		text = strings.Replace(text, m[0], " ", 1)
	}
	if m := numberRe.FindStringSubmatch(text); m != nil {
		it.limit, _ = strconv.Atoi(m[1])
	}
	if m := moduleRe.FindStringSubmatch(text); m != nil {
		it.module = m[1]
	}
	switch {
	case hasStem(it.words, "предупрежд", "warn", "варнинг"):
		it.level = "WARNING"
	case hasStem(it.words, "ошиб", "error"):
		it.level = "ERROR"
	}
}

// moduleFrom подбирает модуль из слов фразы по именам, известным журналу: целиком или по
// последнему сегменту («updates» → «heroku.updates»). Слова вроде «логи» не совпадут ни с чем.
func moduleFrom(words []string, known []string) string {
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

// stripMention убирает обращение к боту (@name) и возвращает, было ли оно.
func stripMention(text, username string) (string, bool) {
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
