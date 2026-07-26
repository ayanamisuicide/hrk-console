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

# Лог живёт в окне между двумя закреплёнными строками: сверху рамка с
# названием и состоянием бота, снизу — со счётчиками и подсказками. Обе
# рисуются вне области прокрутки и перерисовываются раз в секунду, поэтому
# аптайм в шапке тикает, а сама шапка никуда не уезжает.
#
# Ширину считаем по тексту без ANSI-кодов (${#s} считал бы escape-по-
# следовательности), а саму строку собираем в переменную и печатаем через
# '%s': в сообщениях и подписях встречается '%', и формат printf на них
# споткнулся бы.
HEADER_H=1
(( BARE )) && HEADER_H=0

# Раскладка обеих рамок: "УГОЛ─ левое  ────  правое ─УГОЛ"
#   3 + левое + 1 + заполнитель + 1 + правое + 3 = ширина окна
frame_fill() {   # $1 длина левого, $2 длина правого -> HLINE
    local fill=$(( TERM_COLS - 8 - $1 - $2 ))
    (( fill < 1 )) && fill=1
    hline "$fill"
}

draw_header() {
    (( STICKY )) && (( HEADER_H )) || return
    local plain col up
    if [ -n "$TICK_PID" ]; then
        up=$(bot_uptime "$TICK_PID")
        plain="● live · ${VERSION:-?} · $up"
        col="${GRN}●${R} ${C_META}live${R} ${D}·${R} ${C_META}${VERSION:-?}${R} ${D}·${R} ${C_META}$up${R}"
    else
        plain="○ не запущен"
        col="${RED}○${R} ${C_META}не запущен${R}"
    fi
    frame_fill 14 ${#plain}      # 14 — длина "HEROKU USERBOT"
    tput sc
    tput cup 0 0; tput el
    printf '%s' "${C_RULE}╭─ ${B}${MAG}HEROKU USERBOT${R} ${C_RULE}${HLINE}${R} ${col} ${C_RULE}─╮${R}"
    tput rc
}

draw_footer() {
    (( STICKY )) || return
    local pid="$TICK_PID" plain col right up
    if [ -n "$pid" ]; then
        up=$(bot_uptime "$pid")
        plain="● pid $pid · ⏱ $up"
        col="${GRN}●${R} ${C_META}pid $pid${R} ${D}·${R} ${C_META}⏱ $up${R}"
    else
        plain="○ не запущен"
        col="${RED}○${R} ${C_META}не запущен${R}"
    fi
    plain+=" · ⚠ $RL_WARN ✗ $RL_ERR"
    col+=" ${D}·${R} ${YEL}⚠ $RL_WARN${R} ${RED}✗ $RL_ERR${R}"

    if (( SHOW_DEBUG )); then
        right="d — debug: виден · q — выход"
    else
        right="d — debug: скрыт · q — выход"
    fi

    frame_fill ${#plain} ${#right}
    tput sc
    tput cup $(( TERM_ROWS - 1 )) 0; tput el
    printf '%s' "${C_RULE}╰─ ${col} ${C_RULE}${HLINE}${R} ${D}${GRY}${right}${R} ${C_RULE}─╯${R}"
    tput rc
}

# Запасной вариант, когда закрепить строки нечем (не терминал или он не
# умеет csr): шапка просто печатается один раз в поток.
header() {
    local right="○ не запущен"
    bot_alive && right="● live · ${VERSION:-?} · $(bot_uptime)"
    echo
    rule_label "HEROKU USERBOT" "$right"
    echo
}

# Отбивка перезапуска: бот перезапустили из Telegram, началась новая сессия.
restart_header() {
    local s
    hline $(( TERM_COLS > 1 ? TERM_COLS - 1 : 1 )); s="${HLINE//─/━}"
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
    draw_header
    draw_footer
}

fit_geometry() { TERM_COLS=$(cols); TERM_ROWS=$(rows); }

# Область прокрутки: строки между рамками. Всё, что печатает лог, крутится
# только здесь, а нулевая строка и последняя остаются за рамками нетронутыми.
set_scroll_region() { tput csr "$HEADER_H" $(( TERM_ROWS - 2 )); }

# ─── буфер последних строк ────────────────────────────────────────────────
# Храним сырые строки, а не уже отрисованные: при другой ширине окна они
# переносятся иначе, так что перерисовывать нужно заново из исходника.
RING=()
RING_MAX=400
ring_push() {
    RING+=("$1")
    # Подрезаем с запасом, а не на каждой строке: пересборка массива стоит
    # обхода целиком, и делать её на каждую строку лога незачем.
    (( ${#RING[@]} > RING_MAX + 200 )) && RING=("${RING[@]: -RING_MAX}")
    return 0
}

# Заполнить область лога заново из буфера. Через filter_visible, чтобы взять
# ровно экран **видимых** строк: при скрытом DEBUG сырой хвост — почти сплошной
# urllib3, и экран остался бы полупустым. Счётчики warning/error сохраняем:
# строки проигрываются повторно, и без этого каждый ресайз их удваивал бы.
repaint_log() {
    # На строку меньше, чем рядов в области: перевод строки после последней
    # записи прокрутил бы её на единицу и первая строка зря уехала бы вверх.
    local avail=$(( TERM_ROWS - HEADER_H - 2 ))
    (( avail < 1 )) && return
    (( ${#RING[@]} == 0 )) && return
    local w="$RL_WARN" e="$RL_ERR" l
    while IFS= read -r l; do render_line "$l"; done < <(
        printf '%s\n' "${RING[@]}" | filter_visible | tail -n "$avail"
    )
    RL_WARN="$w"; RL_ERR="$e"
}

# Ресайз обрабатываем не в самом обработчике сигнала, а флагом. Обработчик
# может сработать посреди чтения строки, и тяжёлая перерисовка оттуда мешалась
# бы с выводом; заодно поток сигналов при перетягивании рамки мышью схлопывается
# в одну перерисовку.
RESIZE_PENDING=0
on_winch() { RESIZE_PENDING=1; }

# Терминал при смене размера переливает содержимое сам: строки переносятся по
# новой ширине, прежняя область прокрутки сбрасывается, и рамки оказываются
# посреди текста. Чинить это по кускам бесполезно — собираем экран заново.
do_resize() {
    RESIZE_PENDING=0
    fit_geometry
    (( STICKY )) || return
    tput csr 0 $(( TERM_ROWS - 1 ))   # снять прежнюю область до очистки
    clear
    set_scroll_region
    tput cup "$HEADER_H" 0
    repaint_log
    draw_header
    draw_footer
}

# Разделитель внутри потока ("последние записи", "живой поток"): подпись у
# левого края, дальше линейка до конца окна. Так он читается как граница
# секции, а не как ещё одна строка лога со странным отступом.
section() {
    local label="$1" fill
    fill=$(( TERM_COLS - ${#label} - 5 ))
    (( fill < 1 )) && fill=1
    hline "$fill"
    printf '%s── %s %s%s\n' "$D$GRY" "$label" "$HLINE" "$R"
}

# ─── стартовый экран ──────────────────────────────────────────────────────
fit_geometry

# Экран чистим осознанно: шаги запуска из меню своё уже отработали, а лог
# должен начинаться с чистого листа внутри своей рамки. Курсор ставим на
# первую строку области прокрутки, чтобы поток пошёл под шапкой, а не поверх.
if (( STICKY )); then
    clear
    set_scroll_region
    tput cup "$HEADER_H" 0
else
    (( BARE )) || header    # закрепить нечем — печатаем шапку разово в поток
fi

if (( INTERACTIVE )); then
    STTY_SAVE=$(stty -g 2>/dev/null)
    # -icanon: клавиша приходит сразу, без Enter. min 1 time 0 (а не min 0)
    # важно: при min 0 чтение возвращалось бы мгновенно и вхолостую, и цикл
    # ниже, который пережидает тишину на клавиатуре, сжёг бы ядро впустую.
    stty -echo -icanon min 1 time 0 2>/dev/null
fi
trap on_winch WINCH

# ─── история для контекста ────────────────────────────────────────────────
# Берём с запасом сырых строк и отбираем видимые: при скрытом DEBUG хвост
# файла — почти сплошной urllib3, и без отбора «последние 40» схлопнулись бы
# в пару записей.
if (( HISTORY > 0 )); then
    section "последние записи"
    while IFS= read -r l; do ring_push "$l"; render_line "$l"; done < <(
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
    ring_push "$l"
    if [[ "$l" == *"$BOOT_MARKER"* ]]; then
        if [ "$SKIP_BOOT" = 1 ]; then
            SKIP_BOOT=0          # это наша собственная загрузка
        else
            restart_header
        fi
    fi
    render_line "$l"
}

handle_key() {   # 1 — пора выходить
    case "$1" in
        d|D|в|В) toggle_debug ;;
        q|Q|й|Й) return 1 ;;
    esac
    return 0
}

tick
last_tick=$SECONDS

if (( INTERACTIVE )); then
    while true; do
        (( RESIZE_PENDING )) && do_resize

        # "read -t 0" только спрашивает, есть ли что читать, и ничего не
        # забирает. Читать саму строку с таймаутом нельзя: если он сработает
        # на её середине, bash отдаст огрызок и вернёт ошибку, а хвост придёт
        # следующим чтением — строка молча ломается пополам.
        if read -t 0 -u 3 2>/dev/null; then
            if ! IFS= read -r -u 3 line; then
                # Больше 128 — чтение прервал сигнал (тот же SIGWINCH при
                # растягивании окна), поток при этом жив. Без этой проверки
                # любой ресайз выглядел бы как конец лога и закрывал вьюер.
                (( $? > 128 )) && continue
                break
            fi
            handle_line "$line"
            key_wait=0.001                    # поток идёт — клавиатуру лишь опрашиваем
        else
            key_wait=0.1                      # тишина — на клавиатуре и ждём
        fi

        if IFS= read -rsn1 -t "$key_wait" key 2>/dev/null; then
            handle_key "$key" || break
        fi

        if (( SECONDS != last_tick )); then
            tick
            last_tick=$SECONDS
        fi
    done
else
    # Вывод не в терминал: ни клавиатуры, ни подвала — остаётся простое
    # блокирующее чтение до конца потока.
    while IFS= read -r -u 3 line; do handle_line "$line"; done
fi
