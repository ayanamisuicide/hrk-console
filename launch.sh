#!/usr/bin/env bash
# Точка входа с рабочего стола: открывает окно терминала с консолью.
# Аргумент 1/2/3/4 пропускает меню и сразу включает нужный режим.
#
# Сама консоль — бинарник bin/hkc (Go + Bubble Tea). Этот скрипт остаётся
# только ради ярлыка на рабочем столе: ему нужна команда, которая сама
# открывает окно терминала, а не полагается на уже запущенный.

DIR="$(dirname "$(readlink -f "$0")")"
HKC="$DIR/bin/hkc"

if [ ! -x "$HKC" ]; then
    echo "bin/hkc не собран. Выполните: make build" >&2
    exit 1
fi

CMD="'$HKC' $*; exec bash"

open_window() {
    local title="$1" bg="$2" cmd="$3"
    if command -v ghostty > /dev/null 2>&1; then
        ghostty --title="$title" \
                --background="$bg" --foreground=#c9d1d9 \
                --font-size=11 --window-padding-x=10 --window-padding-y=10 \
                --window-width=104 --window-height=34 \
                -e bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v kitty > /dev/null 2>&1; then
        kitty --title "$title" \
              -o background="$bg" -o foreground=#c9d1d9 \
              -o font_size=11 -o window_padding_width=10 \
              -o remember_window_size=no \
              -o initial_window_width=104c -o initial_window_height=34c \
              bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v alacritty > /dev/null 2>&1; then
        alacritty --title "$title" -e bash -c "$cmd" > /dev/null 2>&1 &
        disown
    else
        return 1
    fi
}

if ! open_window "Heroku · Logs" "#0d1117" "$CMD"; then
    # эмулятор не нашёлся — работаем в текущем терминале
    exec "$HKC" "$@"
fi
