#!/usr/bin/env bash
# Точка входа с рабочего стола: открывает окно терминала с меню.
# Аргумент 1/2/3 пропускает меню и сразу включает нужный режим.

DIR="$(dirname "$(readlink -f "$0")")"
source "$DIR/ui.sh"

CMD="'$DIR/menu.sh' $*; exec bash"

# 44 строки — чтобы под меню поместился полный арт из assets/banner.txt (31
# строка). Если окно окажется ниже, menu.sh сам покажет компактный логотип.
if ! open_window "Heroku · Logs" "#0d1117" "$CMD" 44; then
    # kitty/alacritty не нашлись — работаем в текущем терминале
    exec bash -c "$CMD"
fi
