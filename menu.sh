#!/usr/bin/env bash
# Стартовое меню Heroku: выбор режима одной клавишей, без Enter.
# Можно передать 1/2/3 аргументом, чтобы пропустить меню.

DIR="$(dirname "$(readlink -f "$0")")"
source "$DIR/ui.sh"

TITLES=("Подключиться к боту" "Перезапустить бота" "Два окна" "Остановить бота")
DESCS=("не трогая процесс" "стоп и старт заново" "логи + консоль Heroku" "просто stop, без окна")
N=4
SEL=0

# ─── геометрия ────────────────────────────────────────────────────────────
C=$(cols); RW=$(rows)
BW=54
(( BW > C - 4 )) && BW=$(( C - 4 ))
INNER=$(( BW - 2 ))
LP=$(( (C - BW) / 2 ))
(( LP < 0 )) && LP=0

# Компактный логотип на случай, когда большой арт (31 строка) не влезает.
WORDMARK=(
"╦ ╦ ╔═╗ ╦═╗ ╔═╗ ╦╔═ ╦ ╦"
"╠═╣ ║╣  ╠╦╝ ║ ║ ╠╩╗ ║ ║"
"╩ ╩ ╚═╝ ╩╚═ ╚═╝ ╩ ╩ ╚═╝"
)

BAN_H=$(banner_art | wc -l)
BLOCK_H=$(( N + 7 ))            # рамка (4+N строк) + пустая + подсказка

if (( RW >= BAN_H + BLOCK_H + 2 )); then
    ART=1; TOTAL=$(( BAN_H + BLOCK_H ))
else
    ART=0; TOTAL=$(( ${#WORDMARK[@]} + 1 + BLOCK_H ))
fi

# Весь блок центрируем по вертикали
TOP0=$(( (RW - TOTAL) / 2 )); (( TOP0 < 0 )) && TOP0=0
if (( ART )); then TOP=$(( TOP0 + BAN_H )); else TOP=$(( TOP0 + ${#WORDMARK[@]} + 1 )); fi

DASH=$(printf '─%.0s' $(seq 1 $INNER))

# ─── примитивы отрисовки ──────────────────────────────────────────────────
ctr_in() {  # текст, центрированный внутри ширины INNER
    local s="$1" w="$2" l
    l=$(( (w - ${#s}) / 2 )); (( l < 0 )) && l=0
    local r=$(( w - l - ${#s} )); (( r < 0 )) && r=0
    printf "%*s%s%*s" "$l" "" "$s" "$r" ""
}

put() { tput cup "$1" "$LP"; }

border_top() { put $((TOP));       printf "${MAG}╭%s╮${R}" "$DASH"; }
border_mid() { put $((TOP+3));     printf "${MAG}├%s┤${R}" "$DASH"; }
border_bot() { put $((TOP+4+N));   printf "${MAG}╰%s╯${R}" "$DASH"; }

row_title() {
    put $((TOP+1))
    printf "${MAG}│${R}${B}${WHT}%s${R}${MAG}│${R}" "$(ctr_in "HEROKU USERBOT" $INNER)"
}

row_status() {
    local txt col
    if bot_alive; then
        txt="● бот запущен · pid $(bot_pid)"; col="$GRN"
    else
        txt="○ бот не запущен"; col="$GRY"
    fi
    put $((TOP+2))
    printf "${MAG}│${R}${col}%s${R}${MAG}│${R}" "$(ctr_in "$txt" $INNER)"
}

draw_opt() {
    local i="$1" num=$(( $1 + 1 )) title desc left right gap
    title="${TITLES[$i]}"; desc="${DESCS[$i]}"
    left="  $num  $title"; right="$desc  "
    gap=$(( INNER - ${#left} - ${#right} ))
    (( gap < 1 )) && gap=1

    put $(( TOP + 4 + i ))
    if (( i == SEL )); then
        printf "${MAG}│${R}${SELBG}${B}${WHT}%s%*s${R}${SELBG}${GRY}%s${R}${MAG}│${R}" \
               "$left" "$gap" "" "$right"
    else
        printf "${MAG}│${R}  ${B}${MAG}%s${R}  ${WHT}%s${R}%*s${D}${GRY}%s${R}${MAG}│${R}" \
               "$num" "$title" "$gap" "" "$right"
    fi
}

draw_hint() {
    local h="↑↓ выбор   ·   Enter   ·   1-$N сразу   ·   q выход"
    tput cup $((TOP+4+N+2)) 0
    center "$h" "${D}${GRY}${h}${R}"
}

# ─── появление меню ───────────────────────────────────────────────────────
appear() {
    clear
    if (( ART )); then
        tput cup "$TOP0" 0
        banner_art
    else
        local i=0 l
        for l in "${WORDMARK[@]}"; do
            tput cup $(( TOP0 + i )) 0
            center "$l" "${B}${MAG}${l}${R}"
            i=$(( i + 1 ))
        done
    fi
    border_top;            sleep 0.04
    row_title;             sleep 0.04
    row_status;            sleep 0.04
    border_mid;            sleep 0.05
    for (( i = 0; i < N; i++ )); do draw_opt $i; sleep 0.07; done
    border_bot;            sleep 0.04
    draw_hint
}

flash() {  # подсветить выбранный пункт перед запуском
    local i="$1"
    for _ in 1 2 3; do
        put $(( TOP + 4 + i ))
        printf "${MAG}│${R}${B}${GRN}%s${R}${MAG}│${R}" \
               "$(ctr_in "▸  ${TITLES[$i]}  ◂" $INNER)"
        sleep 0.07
        draw_opt "$i"
        sleep 0.07
    done
}

# ─── режимы ───────────────────────────────────────────────────────────────
# Запустить, если не запущен; выставить LOG_FROM.
# Возврат: 0 — поднялся, можно ехать дальше к логам. 1 — не поднялся,
# вызывающий должен остановиться (а не молча тащить в мёртвый лог).
ensure_running() {
    LOG_FROM=$(( $(log_lines) + 1 ))
    step "Запускаю Heroku"
    local pid rc
    pid=$(start_bot_detached); rc=$?

    if [ "$rc" = 3 ]; then
        warn "запуск уже идёт в другом окне — жду"
        for _ in $(seq 1 40); do bot_alive && break; sleep 0.25; done
        if bot_alive; then ok "pid $(bot_pid)"; return 0; fi
        fail "не дождался — попробуйте ещё раз"
        return 1
    fi
    if [ "$rc" != 0 ] || [ -z "$pid" ]; then
        fail "не удалось запустить (venv?)"
        notify critical "Heroku bot" "Не удалось запустить: venv не найден"
        return 1
    fi

    spin 2
    if kill -0 "$pid" 2>/dev/null; then
        ok "pid $pid"
        return 0
    fi

    fail "процесс не поднялся"
    echo
    printf "%*s${D}─── последние строки вывода ───${R}\n" "$PAD" ""
    tail -n 20 "$STARTUP_LOG" 2>/dev/null | colorize
    notify critical "Heroku bot" "Процесс не поднялся после старта"
    return 1
}

# Пауза с любой клавишей — для терминальных состояний (ошибка, стоп), чтобы
# окно не закрылось/не продолжило раньше, чем человек прочитал вывод.
press_any_key() {
    echo
    printf "%*s${D}Нажмите любую клавишу для выхода...${R}" "$PAD" ""
    read -rsn1
}

mode_attach() {
    screen_header "подключение и живые логи"
    step "Проверяю процессы"
    if bot_alive; then
        ok "уже запущен, pid $(bot_pid)"
        step "Подключаюсь к сессии"; ok "без перезапуска"
        sleep 0.4
        exec "$DIR/logs.sh" --history 40
    fi
    warn "не запущен"
    ensure_running || { press_any_key; exit 1; }
    sleep 0.5   # дать прочитать итог запуска: logs.sh чистит экран под себя
    exec "$DIR/logs.sh" --from "$LOG_FROM" --skip-boot
}

mode_restart() {
    screen_header "перезапуск и живые логи"
    step "Проверяю процессы"
    if bot_alive; then
        warn "найден: $(bot_pids | tr '\n' ' ')"
        step "Останавливаю старый процесс"
        stop_bot; case $? in
            0) ok "остановлен" ;;
            2) ok "принудительно остановлен" ;;
        esac
    else
        ok "не запущен"
    fi
    ensure_running || { press_any_key; exit 1; }
    sleep 0.5   # дать прочитать итог запуска: logs.sh чистит экран под себя
    exec "$DIR/logs.sh" --from "$LOG_FROM" --skip-boot
}

mode_stop() {
    screen_header "остановка бота"
    step "Проверяю процессы"
    if bot_alive; then
        warn "найден: $(bot_pids | tr '\n' ' ')"
        step "Останавливаю"
        stop_bot; case $? in
            0) ok "остановлен" ;;
            2) ok "принудительно остановлен" ;;
        esac
    else
        ok "не запущен"
    fi
    press_any_key
    exit 0
}

mode_two_windows() {
    screen_header "два окна: логи и родная консоль"
    step "Проверяю процессы"
    if bot_alive; then
        warn "найден: $(bot_pids | tr '\n' ' ')"
        step "Останавливаю старый процесс"
        stop_bot; case $? in
            0) ok "остановлен" ;;
            2) ok "принудительно остановлен" ;;
        esac
    else
        ok "не запущен"
    fi

    LOG_FROM=$(( $(log_lines) + 1 ))

    step "Открываю родную консоль Heroku"
    if open_window "Heroku · Консоль" "#12161f" "'$DIR/bot-console.sh'"; then
        ok "новое окно"
    else
        fail "терминал не найден"; exec bash
    fi

    step "Жду старта бота"
    for _ in $(seq 1 60); do
        bot_alive && break
        sleep 0.25
    done
    spin 1
    if bot_alive; then ok "pid $(bot_pid)"; else warn "пока не видно, смотрите второе окно"; fi

    step "Это окно становится чистым логом"
    ok "поехали"
    sleep 0.6

    clear
    exec "$DIR/logs.sh" --bare --from "$LOG_FROM" --skip-boot
}

run_choice() {
    tput cnorm
    case "$1" in
        1) mode_attach ;;
        2) mode_restart ;;
        3) mode_two_windows ;;
        4) mode_stop ;;
    esac
}

# ─── быстрый путь: режим передан аргументом ───────────────────────────────
if [[ "$1" =~ ^[1234]$ ]]; then
    run_choice "$1"
    exit 0
fi

# ─── интерактивное меню ───────────────────────────────────────────────────
tput civis
trap 'tput cnorm' EXIT INT TERM

appear

while true; do
    IFS= read -rsn1 key
    if [[ "$key" == $'\e' ]]; then
        read -rsn2 -t 0.05 rest
        key+="$rest"
    fi

    case "$key" in
        1|2|3|4)
            SEL=$(( key - 1 )); draw_opt "$SEL"
            flash "$SEL"; CHOICE="$key"; break ;;
        $'\e[A'|k|K|л|Л)
            old=$SEL; SEL=$(( (SEL - 1 + N) % N )); draw_opt "$old"; draw_opt "$SEL" ;;
        $'\e[B'|j|J|о|О)
            old=$SEL; SEL=$(( (SEL + 1) % N )); draw_opt "$old"; draw_opt "$SEL" ;;
        "")
            flash "$SEL"; CHOICE=$(( SEL + 1 )); break ;;
        q|Q|й|Й|$'\e')
            tput cnorm; clear; exit 0 ;;
    esac
done

run_choice "$CHOICE"
