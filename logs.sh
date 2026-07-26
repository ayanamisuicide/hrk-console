#!/usr/bin/env bash
# Живой просмотр heroku.log колонками, с закреплённым подвалом и
# переключением DEBUG на лету.
#
#   --bare        без шапки (для окна, где кроме лога ничего нет)
#   --from N      начать с N-й строки файла (по умолчанию — с конца)
#   --skip-boot   первую загрузку не считать перезапуском (мы её сами вызвали)
#   --history N   сначала показать N последних видимых записей как контекст
#   --debug       сразу показывать DEBUG (по умолчанию скрыт, клавиша d)
#   --no-debug    оставлено для совместимости: это и есть поведение по умолчанию

source "$(dirname "$(readlink -f "$0")")/ui.sh"

BARE=0; FROM=""; SKIP_BOOT=0; HISTORY=0; SHOW_DEBUG=0

while [ $# -gt 0 ]; do
    case "$1" in
        --bare)      BARE=1 ;;
        --from)      FROM="$2"; shift ;;
        --skip-boot) SKIP_BOOT=1 ;;
        --history)   HISTORY="$2"; shift ;;
        --debug)     SHOW_DEBUG=1 ;;
        --no-debug)  SHOW_DEBUG=0 ;;
    esac
    shift
done

# ─── возможности терминала ────────────────────────────────────────────────
# Закреплённый подвал держится на скроллящейся области (csr): лог крутится в
# верхних строках, нижние две принадлежат подвалу. Если терминал так не
# умеет или окно слишком низкое — просто печатаем поток, без подвала.
TERM_COLS=$(cols); TERM_ROWS=$(rows)
INTERACTIVE=0; [ -t 1 ] && [ -t 0 ] && INTERACTIVE=1
STICKY=0
if (( INTERACTIVE )) && (( TERM_ROWS >= 10 )) && tput csr 0 1 > /dev/null 2>&1; then
    STICKY=1
fi

STTY_SAVE=""; TAIL_PID=""

cleanup() {
    if (( STICKY )); then
        tput csr 0 $(( TERM_ROWS - 1 )) 2>/dev/null
        tput cup $(( TERM_ROWS - 1 )) 0 2>/dev/null
    fi
    [ -n "$STTY_SAVE" ] && stty "$STTY_SAVE" 2>/dev/null
    tput cnorm 2>/dev/null
    [ -n "$TAIL_PID" ] && kill "$TAIL_PID" 2>/dev/null
    echo
}
trap cleanup EXIT INT TERM

# ─── шапка и подвал ───────────────────────────────────────────────────────
VERSION="$(bot_version)"

header() {
    local right="○ не запущен"
    bot_alive && right="● live · ${VERSION:-?} · $(bot_uptime)"
    echo
    rule_label "HEROKU USERBOT" "$right"
    echo
}

# Подвал перерисовывается раз в секунду поверх двух нижних строк. Курсор
# сохраняем и возвращаем, иначе он бы уехал из области прокрутки и следующая
# строка лога легла бы в подвал.
draw_footer() {
    (( STICKY )) || return
    local pid="$TICK_PID" state left right pad up
    if [ -n "$pid" ]; then
        up=$(bot_uptime "$pid")
        state="${GRN}●${R} ${C_META}pid $pid${R}  ${D}·${R}  ${C_META}⏱ $up${R}"
        left="● pid $pid  ·  ⏱ $up"
    else
        state="${RED}○${R} ${C_META}не запущен${R}"
        left="○ не запущен"
    fi
    state+="  ${D}·${R}  ${YEL}⚠ $RL_WARN${R}  ${RED}✗ $RL_ERR${R}"
    left+="  ·  ⚠ $RL_WARN  ✗ $RL_ERR"

    if (( SHOW_DEBUG )); then
        right="d — debug: виден  ·  q — выход"
    else
        right="d — debug: скрыт  ·  q — выход"
    fi
    pad=$(( TERM_COLS - ${#left} - ${#right} - 2 ))
    (( pad < 1 )) && pad=1

    tput sc
    tput cup $(( TERM_ROWS - 2 )) 0; tput el
    printf '%s%s%s' "$C_RULE" "$FOOTER_RULE" "$R"
    tput cup $(( TERM_ROWS - 1 )) 0; tput el
    printf ' %b%*s%s%s%s' "$state" "$pad" "" "$D$GRY" "$right" "$R"
    tput rc
}

# Отбивка перезапуска: бот перезапустили из Telegram, началась новая сессия.
restart_header() {
    local s
    s=$(printf '━%.0s' $(seq 1 $(( TERM_COLS > 1 ? TERM_COLS - 1 : 1 ))))
    printf '\n%s%s%s\n' "$C_RULE" "$s" "$R"
    printf '%s%s  ⟳  ПЕРЕЗАПУСК%s%s  ·  %s  ·  pid %s%s\n' \
           "$B" "$MAG" "$R" "$D$GRY" "$(date '+%H:%M:%S')" "$(bot_pid)" "$R"
    printf '%s%s%s\n\n' "$C_RULE" "$s" "$R"
    VERSION="$(bot_version)"
}

toggle_debug() {
    SHOW_DEBUG=$(( 1 - SHOW_DEBUG ))
    # отбивка в потоке — видно, с какого места изменился состав строк
    if (( SHOW_DEBUG )); then section "debug включён"; else section "debug скрыт"; fi
    draw_footer
}

# Раз в секунду опрашиваем процесс — один раз на всех, кому это нужно.
# Отдельно висевший health-watcher (он жил в соседней панели и красил её
# рамку) уехал вместе с ней, но само наблюдение нужно: если бот падает не по
# нашей команде, об этом надо сказать вслух.
TICK_PID=""
BOT_WAS=""
tick() {
    TICK_PID=$(bot_pid)
    if [ -n "$TICK_PID" ]; then
        [ "$BOT_WAS" = dead ] && notify normal "Heroku bot" "Процесс снова поднялся"
        BOT_WAS=ok
    else
        [ "$BOT_WAS" = ok ] && notify critical "Heroku bot" "Процесс упал"
        BOT_WAS=dead
    fi
    draw_footer
}

# Ширина линейки подвала меняется только при ресайзе — считаем её там, а не
# на каждой перерисовке раз в секунду.
FOOTER_RULE=""
fit_geometry() {
    TERM_COLS=$(cols); TERM_ROWS=$(rows)
    FOOTER_RULE=$(printf '─%.0s' $(seq 1 $(( TERM_COLS > 1 ? TERM_COLS - 1 : 1 ))))
}

on_resize() {
    fit_geometry
    if (( STICKY )); then
        tput csr 0 $(( TERM_ROWS - 3 ))
        draw_footer
    fi
}

# Разделитель внутри потока ("последние записи", "живой поток"): подпись у
# левого края, дальше линейка до конца окна. Так он читается как граница
# секции, а не как ещё одна строка лога со странным отступом.
section() {
    local label="$1" fill
    fill=$(( TERM_COLS - ${#label} - 5 ))
    (( fill < 1 )) && fill=1
    printf '%s── %s %s%s\n' "$D$GRY" "$label" "$(printf '─%.0s' $(seq 1 $fill))" "$R"
}

# ─── стартовый экран ──────────────────────────────────────────────────────
fit_geometry

# Область прокрутки задаётся до первой печати, иначе шапка окажется выше неё
# и уедет за верхний край первым же экраном лога. Экран чистим осознанно:
# шаги запуска из меню своё уже отработали, а лог должен начинаться с чистого
# листа под своей шапкой.
if (( STICKY )); then
    clear
    tput csr 0 $(( TERM_ROWS - 3 ))
    tput cup 0 0
fi

(( BARE )) || header

if (( INTERACTIVE )); then
    STTY_SAVE=$(stty -g 2>/dev/null)
    stty -echo -icanon min 0 time 0 2>/dev/null
fi
trap on_resize WINCH

# ─── история для контекста ────────────────────────────────────────────────
# Берём с запасом сырых строк и отбираем видимые: при скрытом DEBUG хвост
# файла — почти сплошной urllib3, и без отбора «последние 40» схлопнулись бы
# в пару записей.
if (( HISTORY > 0 )); then
    section "последние записи"
    while IFS= read -r l; do render_line "$l"; done < <(
        tail -n 5000 "$LOG_FILE" 2>/dev/null | filter_visible | tail -n "$HISTORY"
    )
    section "живой поток"
fi

# ─── живой поток ──────────────────────────────────────────────────────────
# tail -F (а не -f) — переживает ротацию: RotatingFileHandler в heroku/log.py
# крутит heroku.log на 10 МБ.
if [ -n "$FROM" ]; then TAIL_ARGS=(-n "+$FROM"); else TAIL_ARGS=(-n 0); fi

# Отдельный дескриптор, а не пайп в while: stdin остаётся свободным под
# клавиатуру, иначе переключать debug на лету было бы нечем.
exec 3< <(tail -F "${TAIL_ARGS[@]}" -- "$LOG_FILE" 2>/dev/null)
TAIL_PID=$!

handle_line() {
    local l="$1"
    if [[ "$l" == *"$BOOT_MARKER"* ]]; then
        if [ "$SKIP_BOOT" = 1 ]; then
            SKIP_BOOT=0          # это наша собственная загрузка
        else
            restart_header
        fi
    fi
    render_line "$l"
}

tick
last_tick=$SECONDS

while true; do
    if IFS= read -r -t 0.2 -u 3 line; then
        handle_line "$line"
    else
        # больше 128 — таймаут чтения; иначе поток кончился и ждать нечего
        (( $? <= 128 )) && break
    fi

    if (( INTERACTIVE )) && IFS= read -rsn1 -t 0.001 key 2>/dev/null; then
        case "$key" in
            d|D|в|В) toggle_debug ;;
            q|Q|й|Й) break ;;
        esac
    fi

    if (( SECONDS != last_tick )); then
        tick
        last_tick=$SECONDS
    fi
done
