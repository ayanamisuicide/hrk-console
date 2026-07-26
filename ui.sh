#!/usr/bin/env bash
# Общие UI-хелперы для консолей Heroku.
# Лежит вне репозитория бота намеренно: heroku/log.py умеет делать
# reset_to_master()/restore_worktree(), что может снести чужие файлы в ~/Heroku.

UI_DIR="$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")"

HEROKU_DIR="${HEROKU_DIR:-$HOME/Heroku}"
# LOG_FILE вынесен в переменную окружения не только ради красоты: так вьюер
# можно натравить на любой файл и проверить отрисовку, не трогая живой лог.
LOG_FILE="${LOG_FILE:-$HEROKU_DIR/heroku.log}"
STARTUP_LOG="$HEROKU_DIR/.startup.log"   # stdout/stderr процесса — раньше улетал в /dev/null
LOCK_FILE="$HEROKU_DIR/.launch.lock"     # защита от гонки при двойном запуске старта
# Скобки вокруг первой буквы — чтобы pgrep/pkill не находили сами себя и
# соседние pgrep: их cmdline содержит "[p]ython3 -m heroku", а этому регэкспу
# такая строка не соответствует. Без этого bot_alive ловил фантомные pid.
BOT_PATTERN="[p]ython3 -m heroku"
BOOT_MARKER="root: Got DB"   # первая строка каждой новой сессии бота

# ─── тема ─────────────────────────────────────────────────────────────────
# Единственное место, где живут цвета: сначала палитра (какой это цвет),
# затем роли (где он применяется). Перекрашивать проект нужно здесь.
R=$'\033[0m';  B=$'\033[1m';  D=$'\033[2m'
GRN=$'\033[38;5;42m';  RED=$'\033[38;5;203m'; YEL=$'\033[38;5;221m'
CYN=$'\033[38;5;80m';  MAG=$'\033[38;5;177m'; GRY=$'\033[38;5;245m'
WHT=$'\033[38;5;255m'; SELBG=$'\033[48;5;54m'
DIM=$'\033[38;5;240m'; FAINT=$'\033[38;5;243m'; CRIT=$'\033[1;38;5;196m'

# Роли. Главное правило темы: насыщенный цвет несёт только маркер уровня,
# текст сообщения остаётся светлым и читаемым, а время и модуль уходят в
# фон. Раньше цветом заливалась вся строка — из-за этого DEBUG превращался
# в серую кашу, INFO в сплошную бирюзу, и глазу не за что было зацепиться.
C_TIME="$DIM"        # колонка времени
C_MOD="$FAINT"       # колонка модуля
C_MSG="$WHT"         # текст сообщения
C_MSG_DIM="$GRY"     # текст сообщения у DEBUG
C_CONT="$MAG"        # стрелка продолжения многострочной записи
C_RULE="$MAG"        # линейки и рамки
C_META="$GRY"        # подписи в шапке и подвале

cols() { tput cols  2>/dev/null || echo 80; }
rows() { tput lines 2>/dev/null || echo 24; }

# ─── процесс бота ─────────────────────────────────────────────────────────
bot_pids() { pgrep -f "$BOT_PATTERN" 2>/dev/null; }
bot_pid()  { pgrep -f "$BOT_PATTERN" 2>/dev/null | head -1; }
bot_alive() { pgrep -f "$BOT_PATTERN" > /dev/null 2>&1; }

# ─── вывод ────────────────────────────────────────────────────────────────
# center "видимый текст" "версия с цветами" — печатает по центру экрана
center() {
    local plain="$1" colored="${2:-$1}" pad
    pad=$(( ( $(cols) - ${#plain} ) / 2 ))
    (( pad < 0 )) && pad=0
    printf "%*s%s\n" "$pad" "" "$colored"
}

# Отступ слева, чтобы весь вывод был выровнен по центру окна (ширина блока 64).
PAD=$(( ( $(cols) - 64 ) / 2 ))
(( PAD < 0 )) && PAD=0

step() {  # выравнивание по символам, а не по байтам (важно для кириллицы)
    local pad=$(( 48 - ${#1} ))
    (( pad < 1 )) && pad=1
    printf "%*s${CYN}▸${R} %s%*s" "$PAD" "" "$1" "$pad" ""
}
ok()   { printf "${GRN}✓ %s${R}\n" "${1:-готово}"; }
warn() { printf "${YEL}• %s${R}\n" "$1"; }
fail() { printf "${RED}✗ %s${R}\n" "$1"; }

spin() {  # spin <секунд>
    local frames='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏' i=0 end=$((SECONDS + ${1:-2}))
    printf " "
    while [ $SECONDS -lt $end ]; do
        printf "\b${D}%s${R}" "${frames:i++%10:1}"
        sleep 0.08
    done
    printf "\b"
}

banner_art() { cat "$HEROKU_DIR/assets/banner.txt" 2>/dev/null; }

hr() {
    local s
    s=$(printf '─%.0s' $(seq 1 64))
    printf "%*s${MAG}%s${R}\n" "$PAD" "" "$s"
}

# Шапка экрана: арт, разделители, подзаголовок и время.
screen_header() {
    clear
    banner_art
    echo
    hr
    printf "%*s${B}${MAG}HEROKU USERBOT${R}  ${D}·${R}  ${GRY}%s${R}\n" "$PAD" "" "$1"
    printf "%*s${D}%s${R}\n" "$PAD" "" "$(date '+%Y-%m-%d  %H:%M:%S')"
    hr
    echo
}

# rule_label "слева" "справа" [символ-угла] — линейка во всю ширину окна с
# подписью слева и статусом справа:
#   ╭─ HEROKU ───────────────────────── ● live · 2.2.2 · 1ч 12м ─╮
# Обе подписи принимаются без ANSI-кодов: цвет накладывается здесь, иначе
# ${#s} считал бы escape-последовательности и линейка разъезжалась бы.
rule_label() {
    local left="$1" right="$2" w fill dash
    w=$(cols)
    [ -n "$left" ]  && left=" $left "
    [ -n "$right" ] && right=" $right "
    fill=$(( w - 4 - ${#left} - ${#right} ))
    (( fill < 1 )) && fill=1
    dash=$(printf '─%.0s' $(seq 1 $fill))
    printf "${C_RULE}╭─${R}${B}${MAG}%s${R}${C_RULE}%s${R}${C_META}%s${R}${C_RULE}─╮${R}\n" \
           "$left" "$dash" "$right"
}

# Ровная линейка во всю ширину — низ шапки, верх подвала.
rule_full() {
    local w dash
    w=$(cols)
    dash=$(printf '─%.0s' $(seq 1 $(( w > 2 ? w - 2 : 1 ))))
    printf "${C_RULE}%s${R}\n" "$dash"
}

# ─── подсветка строк лога по уровню ───────────────────────────────────────
log_color() {
    case "$1" in
        *"[DEBUG]"*)    printf '\033[38;5;243m' ;;
        *"[INFO]"*)     printf '\033[38;5;80m'  ;;
        *"[WARNING]"*)  printf '\033[38;5;221m' ;;
        *"[ERROR]"*)    printf '\033[38;5;203m' ;;
        *"[CRITICAL]"*) printf '\033[1;38;5;196m' ;;
        *)              printf '\033[37m' ;;
    esac
}

colorize() {  # фильтр stdin -> цветной stdout
    local l
    while IFS= read -r l; do
        printf "%s%s\033[0m\n" "$(log_color "$l")" "$l"
    done
}

# ─── управление ботом ─────────────────────────────────────────────────────
stop_bot() {
    local pids
    pids=$(bot_pids) || true
    [ -z "$pids" ] && return 1

    kill $pids 2>/dev/null
    for _ in $(seq 1 50); do
        bot_alive || return 0
        sleep 0.1
    done
    pkill -9 -f "$BOT_PATTERN" 2>/dev/null
    sleep 0.5
    return 2   # пришлось убивать жёстко
}

# Запуск в фоне, отвязанно от этого окна.
# setsid обязателен: при .restart из Telegram бот делает killpg по своей
# группе процессов (heroku/_internal.py: die()) и без своей сессии утащил бы
# за собой это окно.
#
# flock на LOCK_FILE не даёт двум параллельным стартам (например, автозапуск
# + ручной клик почти одновременно) поднять два процесса бота разом. Лок
# держится, пока процесс не отвязан (несколько сотен мс), этого достаточно —
# дальше bot_alive() уже видит новый pid и защищает сам себя.
#
# Возврат: 0 — запущен, pid в stdout. 1 — venv не найден. 3 — лок занят
# (кто-то уже стартует прямо сейчас).
start_bot_detached() {
    cd "$HEROKU_DIR" || return 1

    exec 9>"$LOCK_FILE"
    if ! flock -n 9; then
        return 3
    fi

    # shellcheck disable=SC1091
    if ! source venv/bin/activate 2>/dev/null; then
        flock -u 9
        return 1
    fi

    : > "$STARTUP_LOG" 2>/dev/null
    setsid nohup python3 -m heroku > "$STARTUP_LOG" 2>&1 &
    local pid=$!
    disown

    sleep 0.3   # дать процессу зацепиться за свою группу, прежде чем снять лок
    flock -u 9
    echo "$pid"
}

log_lines() { wc -l < "$LOG_FILE" 2>/dev/null || echo 0; }

# ─── живые показатели для шапки и подвала ─────────────────────────────────
# bot_uptime [pid] — сколько живёт процесс бота: "1ч 12м" / "34м 05с" / "—".
# Pid можно передать снаружи: тот, кто опрашивает состояние раз в секунду,
# уже сходил в pgrep, и второй раз ходить незачем.
bot_uptime() {
    local pid="${1:-$(bot_pid)}" etimes h m
    [ -z "$pid" ] && { printf '—'; return; }
    etimes=$(ps -o etimes= -p "$pid" 2>/dev/null | tr -d ' ')
    [ -z "$etimes" ] && { printf '—'; return; }
    h=$(( etimes / 3600 )); m=$(( (etimes % 3600) / 60 ))
    if (( h > 0 )); then printf '%dч %02dм' "$h" "$m"
    else printf '%dм %02dс' "$m" "$(( etimes % 60 ))"
    fi
}

# bot_version — версия из последнего баннера старта в логе ("🪐 Heroku 2.2.2
# #b9f2cb6 started"). Хвост, а не весь файл: heroku.log растёт до 10 МБ, а
# нужна только последняя сессия.
bot_version() {
    local v
    v=$(tail -n 20000 "$LOG_FILE" 2>/dev/null \
        | grep -o 'Heroku [0-9]\+\.[0-9]\+\.[0-9]\+' | tail -1)
    printf '%s' "${v#Heroku }"
}

command -v notify-send > /dev/null 2>&1 && HAS_NOTIFY=1 || HAS_NOTIFY=0
notify() {  # notify "критичность" "заголовок" "текст"
    [ "$HAS_NOTIFY" = 1 ] || return 0
    notify-send -u "$1" -i "$HEROKU_DIR/assets/heroku.png" "$2" "$3" 2>/dev/null
}

# ─── открыть новое окно терминала ─────────────────────────────────────────
# open_window "Заголовок" "#цвет-фона" "команда для bash -c" [высота в строках]
open_window() {
    local title="$1" bg="$2" cmd="$3" h="${4:-34}"
    if command -v ghostty > /dev/null 2>&1; then
        ghostty --title="$title" \
                --background="$bg" --foreground=#c9d1d9 \
                --font-size=11 --window-padding-x=10 --window-padding-y=10 \
                --window-width=104 --window-height="$h" \
                -e bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v kitty > /dev/null 2>&1; then
        kitty --title "$title" \
              -o background="$bg" -o foreground=#c9d1d9 \
              -o font_size=11 -o window_padding_width=10 \
              -o remember_window_size=no \
              -o initial_window_width=104c -o initial_window_height="${h}c" \
              bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v alacritty > /dev/null 2>&1; then
        alacritty --title "$title" -e bash -c "$cmd" > /dev/null 2>&1 &
        disown
    else
        return 1
    fi
}

# Отрисовка строк лога живёт отдельным файлом — подключаем последним, чтобы
# роли темы выше уже существовали.
# shellcheck source=render.sh
source "$UI_DIR/render.sh"
