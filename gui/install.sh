#!/usr/bin/env bash
# Собирает GUI и ставит его как обычное приложение: бинарник в ~/.local/bin,
# иконка в тему значков, ярлык в меню приложений и на рабочий стол.
#
# Ставится в домашний каталог, а не в /usr — sudo не нужен, и удаляется всё
# тем же скриптом с --uninstall, не оставляя следов в системных каталогах.
set -euo pipefail

APP_ID="hrk-console-gui"
APP_NAME="Heroku"
GUI_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"

BIN_DIR="$HOME/.local/bin"
ICON_DIR="$HOME/.local/share/icons/hicolor/512x512/apps"
APPS_DIR="$HOME/.local/share/applications"
DESKTOP_FILE="$APPS_DIR/$APP_ID.desktop"
DESKTOP_DIR="$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")"

if [ "${1:-}" = "--uninstall" ]; then
    rm -f "$BIN_DIR/$APP_ID" "$ICON_DIR/$APP_ID.png" "$DESKTOP_FILE" \
          "$DESKTOP_DIR/$APP_ID.desktop"
    update-desktop-database "$APPS_DIR" 2> /dev/null || true
    echo "удалено"
    exit 0
fi

# ─── сборка ───
# wails после `go install` живёт в GOPATH/bin, которого обычно нет в PATH,
# поэтому ищем и там тоже, а не только по имени команды.
WAILS="$(command -v wails || true)"
if [ -z "$WAILS" ]; then
    WAILS="$(go env GOPATH)/bin/wails"
fi
if [ ! -x "$WAILS" ]; then
    echo "wails не найден. Установите: go install github.com/wailsapp/wails/v2/cmd/wails@latest" >&2
    exit 1
fi

echo "Собираю…"
# Тот же трюк с версией, что у make build/make gui: без неё бинарник вечно
# показывал бы "dev" в GUI, даже собранный из тега релиза.
VERSION="$(cd "$GUI_DIR" && git describe --tags --dirty 2>/dev/null || echo dev)"
# webkit2_41: на Ubuntu/Mint 24.04 в репозиториях остался только
# webkit2gtk-4.1, а wails по умолчанию ищет -4.0.
(cd "$GUI_DIR" && "$WAILS" build -tags webkit2_41 -ldflags "-X main.version=$VERSION")

# ─── установка ───
install -Dm755 "$GUI_DIR/build/bin/$APP_ID" "$BIN_DIR/$APP_ID"
install -Dm644 "$GUI_DIR/build/appicon.png" "$ICON_DIR/$APP_ID.png"

mkdir -p "$APPS_DIR"
cat > "$DESKTOP_FILE" << EOF
[Desktop Entry]
Type=Application
Name=$APP_NAME
Comment=Логи и управление Heroku-юзерботом
Exec=$BIN_DIR/$APP_ID
Icon=$APP_ID
Terminal=false
Categories=System;
StartupWMClass=$APP_ID
EOF
chmod +x "$DESKTOP_FILE"

# Обновляем кеши, иначе ярлык появляется в меню только после перелогина.
update-desktop-database "$APPS_DIR" 2> /dev/null || true
gtk-update-icon-cache -f -t "$HOME/.local/share/icons/hicolor" 2> /dev/null || true

# Копия на рабочий стол. Cinnamon/KDE/Xfce требуют, чтобы файл был
# исполняемым, а Nemo и Nautilus — ещё и доверенным, иначе вместо иконки
# показывается текстовый файл или запрос подтверждения при каждом клике.
if [ -d "$DESKTOP_DIR" ]; then
    cp "$DESKTOP_FILE" "$DESKTOP_DIR/$APP_ID.desktop"
    chmod +x "$DESKTOP_DIR/$APP_ID.desktop"
    gio set "$DESKTOP_DIR/$APP_ID.desktop" metadata::trusted true 2> /dev/null || true
fi

echo
echo "Готово."
echo "  меню приложений : $APP_NAME"
echo "  рабочий стол    : $DESKTOP_DIR/$APP_ID.desktop"
echo "  из терминала    : $APP_ID"
echo
echo "Удалить: $0 --uninstall"
