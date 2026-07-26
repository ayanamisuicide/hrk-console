#!/usr/bin/env bash
# Превращение сырой строки heroku.log в колонки, которые видит глаз:
#
#   00:12:10  ●  root            🪐 Heroku 2.2.2 #b9f2cb6 started
#   ^^^^^^^^  ^  ^^^^^^^^^^^^^^  ^
#   время     |  модуль          сообщение — светлое и читаемое
#             маркер уровня — единственное, что несёт насыщенный цвет
#
# Формат записи в файле:
#   2026-07-27 00:12:03 [DEBUG] git.util: sys.platform='linux'
# Дата при живом просмотре во всех строках одна и та же и съедает 11 колонок
# ширины, поэтому в кадр попадает только время.
#
# Строки без такого префикса — продолжение предыдущей записи: бот пишет
# многострочные сообщения, и стартовый баннер уходит в лог как "root: " плюс
# перевод строки плюс "🪐 Heroku ... started". Продолжение наследует уровень
# и видимость своей записи — иначе при скрытом DEBUG баннер старта повис бы
# сиротой посреди чужого вывода.

# ─── геометрия колонок ────────────────────────────────────────────────────
TIME_W=8
MOD_W=14
# Отступ, с которого начинается колонка сообщения. Под него же выравниваются
# перенесённые хвосты длинных строк, чтобы левый край текста был один.
GUTTER=$(( TIME_W + 2 + 1 + 2 + MOD_W + 2 ))

TERM_COLS=$(cols)

LOG_RE='^[0-9]{4}-[0-9]{2}-[0-9]{2} ([0-9]{2}:[0-9]{2}:[0-9]{2}) \[([A-Z]+)\] ([^ :]+): ?(.*)$'

# Что видно всегда, даже когда DEBUG скрыт. Формально эти записи пишутся
# уровнем DEBUG, но по смыслу они и есть то, ради чего открывают лог.
#
# По модулю — только root: это голос самого бота, за сессию он выдаёт
# несколько строк (Got DB, баннер версии, префикс). Ставить сюда же
# heroku.loader нельзя: он на каждом старте печатает по две строки на каждый
# из полусотни модулей ("Loading ... from filesystem"), и это ровно тот шум,
# от которого прячут DEBUG.
LOUD_MODULES='^(root|heroku\.main|heroku\.__main__)$'

# По тексту — важные события от любого модуля: итог пересборки, версия.
LOUD_MESSAGES='^(Reloaded |Heroku |🪐)'

# ─── состояние построчного разбора ────────────────────────────────────────
RL_LEVEL="INFO"     # уровень последней разобранной записи
RL_VISIBLE=1        # была ли она показана (продолжения идут за ней)
RL_WARN=0           # счётчики для подвала
RL_ERR=0
RL_PEND_TIME=""     # запись с пустым сообщением ждёт своего продолжения
RL_PEND_LVL=""
RL_PEND_MOD=""

# ─── стиль уровня ─────────────────────────────────────────────────────────
# Без подстановки команд: функция вызывается на каждую строку лога, лишний
# subshell тут превращается в заметную нагрузку на живом потоке.
level_style() {   # -> LV_GLYPH, LV_COL, LV_MSG
    case "$1" in
        DEBUG)    LV_GLYPH='·'; LV_COL="$FAINT"; LV_MSG="$C_MSG_DIM" ;;
        INFO)     LV_GLYPH='●'; LV_COL="$CYN";   LV_MSG="$C_MSG" ;;
        WARNING)  LV_GLYPH='▲'; LV_COL="$YEL";   LV_MSG="$YEL" ;;
        ERROR)    LV_GLYPH='✗'; LV_COL="$RED";   LV_MSG="$RED" ;;
        CRITICAL) LV_GLYPH='✖'; LV_COL="$CRIT";  LV_MSG="$CRIT" ;;
        *)        LV_GLYPH='·'; LV_COL="$FAINT"; LV_MSG="$C_MSG" ;;
    esac
}

# Длинные точечные пути в колонку не влезают и несут мало смысла: важно
# "какая подсистема", а не полный питоновский путь до неё.
short_module() {   # -> SM
    local m="$1"
    case "$m" in
        urllib3.*)  m="urllib3"  ;;
        telethon.*) m="telethon" ;;
        asyncio.*)  m="asyncio"  ;;
        herokutl.*) m="herokutl" ;;
    esac
    m="${m#heroku.modules.}"
    m="${m#heroku.}"
    (( ${#m} > MOD_W )) && m="${m:0:MOD_W-1}…"
    SM="$m"
}

# ─── перенос длинных сообщений ────────────────────────────────────────────
# Хвост уезжает под колонку сообщения, а не к левому краю окна: так блок
# текста читается как один абзац и не ломает колоночную сетку.
wrap_text() {   # $1 текст, $2 ширина -> WRAP_LINES
    local text="$1" width="$2" line="" word
    WRAP_LINES=()
    (( width < 24 )) && width=24
    # noglob: в логах полно '*' и '?' (Popen-дампы, урлы), без этого цикл
    # for развернул бы их в имена файлов из текущего каталога.
    set -f
    for word in $text; do
        if [ -z "$line" ]; then
            line="$word"
        elif (( ${#line} + 1 + ${#word} <= width )); then
            line="$line $word"
        else
            WRAP_LINES+=("$line")
            line="$word"
        fi
        # одно слово длиннее строки (длинный урл, хеш) — режем жёстко
        while (( ${#line} > width )); do
            WRAP_LINES+=("${line:0:width}")
            line="${line:width}"
        done
    done
    set +f
    [ -n "$line" ] && WRAP_LINES+=("$line")
    (( ${#WRAP_LINES[@]} == 0 )) && WRAP_LINES=("")
}

# ─── отрисовка одной строки ───────────────────────────────────────────────
# Печатает уже готовый к показу кусок; если запись отфильтрована, не печатает
# ничего. Ширину колонок выравниваем вручную через ${#s}: bash-овый printf
# считает "%-*s" в байтах, а в модулях и сообщениях встречается юникод.
render_line() {
    local raw="$1" time lvl mod msg cont=0

    if [[ "$raw" =~ $LOG_RE ]]; then
        time="${BASH_REMATCH[1]}"; lvl="${BASH_REMATCH[2]}"
        mod="${BASH_REMATCH[3]}";  msg="${BASH_REMATCH[4]}"

        RL_LEVEL="$lvl"
        case "$lvl" in
            WARNING)        RL_WARN=$(( RL_WARN + 1 )) ;;
            ERROR|CRITICAL) RL_ERR=$(( RL_ERR + 1 )) ;;
        esac

        if [ "$lvl" = DEBUG ] && [ "${SHOW_DEBUG:-0}" != 1 ] \
           && ! [[ "$mod" =~ $LOUD_MODULES ]] \
           && ! [[ "$msg" =~ $LOUD_MESSAGES ]]; then
            RL_VISIBLE=0
            RL_PEND_TIME=""
            return
        fi
        RL_VISIBLE=1

        # Пустое сообщение — это заголовок многострочной записи (тот самый
        # "root: " перед баннером). Придерживаем его: текст приедет следующей
        # строкой и встанет в ту же строку экрана, а не разложится на пустую
        # шапку плюс висящее продолжение.
        if [ -z "$msg" ]; then
            RL_PEND_TIME="$time"; RL_PEND_LVL="$lvl"; RL_PEND_MOD="$mod"
            return
        fi
        RL_PEND_TIME=""
    else
        # продолжение предыдущей записи
        [ "${RL_VISIBLE:-1}" = 1 ] || return
        msg="$raw"
        if [ -n "$RL_PEND_TIME" ]; then
            time="$RL_PEND_TIME"; lvl="$RL_PEND_LVL"; mod="$RL_PEND_MOD"
            RL_PEND_TIME=""
        else
            cont=1; time=""; lvl="$RL_LEVEL"; mod=""
        fi
    fi

    level_style "$lvl"
    short_module "$mod"

    local tpad=$(( TIME_W - ${#time} ));   (( tpad < 0 )) && tpad=0
    local mpad=$(( MOD_W  - ${#SM} ));     (( mpad < 0 )) && mpad=0

    wrap_text "$msg" $(( TERM_COLS - GUTTER - 1 ))

    local first=1 l
    for l in "${WRAP_LINES[@]}"; do
        if (( first )) && (( ! cont )); then
            printf '%s%s%*s%s  %s%s%s  %s%s%*s%s  %s%s%s\n' \
                "$C_TIME" "$time" "$tpad" "" "$R" \
                "$LV_COL" "$LV_GLYPH" "$R" \
                "$C_MOD" "$SM" "$mpad" "" "$R" \
                "$LV_MSG" "$l" "$R"
        elif (( first )); then
            # физически новая строка внутри одной записи (трейсбек, дамп)
            printf '%*s%s↳%s %s%s%s\n' \
                $(( GUTTER - 2 )) "" "$C_CONT" "$R" "$LV_MSG" "$l" "$R"
        else
            # мягкий перенос — просто выравниваем под колонку сообщения
            printf '%*s%s%s%s\n' "$GUTTER" "" "$LV_MSG" "$l" "$R"
        fi
        first=0
    done
}

# ─── отбор видимых строк из готового файла ────────────────────────────────
# Для "--history N" нужно N видимых записей, а не N сырых строк: при скрытом
# DEBUG хвост файла — это почти сплошной urllib3, и без отбора история
# схлопнулась бы в одну-две строки. Правило здесь обязано совпадать с
# фильтром в render_line выше.
filter_visible() {
    awk -v show_debug="${SHOW_DEBUG:-0}" '
        /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9] \[/ {
            keep = 1
            if ($0 ~ /\[DEBUG\]/) {
                keep = (show_debug == 1)
                if ($0 ~ /\[DEBUG\] (root|heroku\.main|heroku\.__main__):/) keep = 1
                if ($0 ~ /\[DEBUG\] [^ ]+: (Reloaded |Heroku |🪐)/) keep = 1
            }
            if (keep) print
            next
        }
        keep { print }   # продолжение идёт за судьбой своей записи
    '
}
