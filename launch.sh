#!/usr/bin/env bash
# Точка входа с рабочего стола (Heroku Logs.desktop → Exec=launch.sh):
# без аргументов открывает окно терминала с выбором "консоль или
# приложение" и сама собирает то, что выбрано, если оно ещё не собрано.
# Аргумент 1/2/3/4 по-прежнему пропускает меню и сразу включает нужный
# режим hkc (проброс в его собственное меню подключения к боту) — это
# старое поведение, старые ярлыки/алиасы на него полагаются, и выбор
# консоль/приложение здесь ни при чём: раз номер режима указан явно,
# значит консоль уже выбрана.

DIR="$(dirname "$(readlink -f "$0")")"
HKC="$DIR/bin/hkc"
GUI="$DIR/gui/build/bin/hrk-console-gui"

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
    elif command -v xfce4-terminal > /dev/null 2>&1; then
        xfce4-terminal --title="$title" --color-bg="$bg" --color-text=#c9d1d9 \
                        --geometry=104x34 -x bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v mate-terminal > /dev/null 2>&1; then
        mate-terminal --title "$title" -x bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v gnome-terminal > /dev/null 2>&1; then
        gnome-terminal --title "$title" -- bash -c "$cmd" > /dev/null 2>&1 &
        disown
    elif command -v x-terminal-emulator > /dev/null 2>&1; then
        x-terminal-emulator -e bash -c "$cmd" > /dev/null 2>&1 &
        disown
    else
        return 1
    fi
}

# run_console/run_gui выполняются УЖЕ внутри открытого окна терминала (см.
# --console/--gui ниже) — здесь можно просто печатать и ждать, эти строки
# видит пользователь, а не куда-то теряются.

run_console() {
    if [ ! -x "$HKC" ]; then
        echo "bin/hkc не собран — собираю (make build)…"
        if ! ( cd "$DIR" && make build ); then
            echo "сборка не удалась — см. вывод выше" >&2
            exec bash # держим окно открытым, чтобы ошибку было видно
        fi
    fi
    # Не exec: после выхода из hkc окно остаётся с обычным шеллом, а не
    # закрывается сразу — тем же приёмом, что и раньше (CMD="... ; exec bash").
    "$HKC" "$@"
    exec bash
}

run_gui() {
    if [ ! -x "$GUI" ]; then
        # wails ставится через `go install` — обычный Go-бинарник в GOPATH/bin,
        # без пакетного менеджера и без sudo, тот же риск, что у make build/
        # make gui. Автоматический запуск здесь безопасен, поэтому запускаем,
        # а не просто печатаем команду.
        local wails_gopath="$(go env GOPATH 2>/dev/null)/bin/wails"
        if ! command -v wails > /dev/null 2>&1 && [ ! -x "$wails_gopath" ]; then
            echo "wails не найден — ставлю (go install .../wails@latest)…"
            if ! go install github.com/wailsapp/wails/v2/cmd/wails@latest; then
                echo "установка wails не удалась — см. вывод выше" >&2
                exec bash
            fi
        fi
        echo "GUI не собран — собираю (make gui, тянет сборку фронтенда, может занять минуту)…"
        if ! ( cd "$DIR" && make gui ); then
            # Системные библиотеки webview (libgtk, libwebkit2gtk, ...) требуют
            # sudo — их этот скрипт не ставит ни при каких условиях, даже если
            # они и есть настоящая причина провала. Подсказка, а не диагноз:
            # он такой же вероятный, как и сетевая осечка при сборке фронтенда.
            cat >&2 <<EOF
сборка GUI не удалась — см. вывод выше.

Частая причина — не хватает системных библиотек webview. Их этот скрипт
сам не ставит (нужен sudo), разово руками (см. gui/README.md):
  sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev build-essential pkg-config nodejs npm
EOF
            exec bash
        fi
    fi
    # GUI — своё окно (GTK/webkit), терминал ему не нужен: в отличие от
    # консоли, здесь не держим шелл после выхода — закрыли окно, дело сделано.
    exec "$GUI"
}

case "${1:-}" in
    --console) shift; run_console "$@" ;;
    --gui) run_gui ;;
esac

if [ $# -gt 0 ]; then
    # Старое поведение: номер режима указан явно — сразу консоль, минуя
    # выбор консоль/приложение (см. заголовок файла).
    CMD="'$DIR/launch.sh' --console $*"
    if ! open_window "Heroku · Logs" "#0d1117" "$CMD"; then
        run_console "$@" # эмулятор не нашёлся — работаем в текущем терминале
    fi
    exit 0
fi

# Без аргументов — экран выбора в новом окне терминала: он же и решит,
# собирать ли что-то, через повторный вызов этого скрипта с --console/--gui
# (см. case выше). Прямой запуск make build/make gui отсюда, до открытия
# окна, был бы не виден пользователю — открытое окно печатает вывод сборки.
PICKER="clear; echo 'HEROKU'; echo; \
echo '  1) Консоль (hkc)'; \
echo '  2) Приложение (GUI)'; echo; \
read -p 'выбор [1/2, по умолчанию 1]: ' c; \
case \"\$c\" in \
  2) exec '$DIR/launch.sh' --gui ;; \
  *) exec '$DIR/launch.sh' --console ;; \
esac"

if ! open_window "Heroku · Logs" "#0d1117" "$PICKER"; then
    # эмулятор не нашёлся — тот же выбор прямо в текущем терминале
    echo "HEROKU"
    echo
    echo "  1) Консоль (hkc)"
    echo "  2) Приложение (GUI)"
    echo
    read -r -p "выбор [1/2, по умолчанию 1]: " choice
    case "$choice" in
        2) run_gui ;;
        *) run_console ;;
    esac
fi
