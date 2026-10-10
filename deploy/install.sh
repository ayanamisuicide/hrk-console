#!/usr/bin/env bash
# Устанавливает hrk-console из последнего стабильного релиза: git-копия исходников на main,
# проверенный бинарник из GitHub Releases и службы systemd (панель и обновления).
#
#   curl -fsSL https://github.com/ayanamisuicide/hrk-console/releases/latest/download/install.sh | sudo bash
#
# Параметры задаются переменными окружения (через sudo VAR=… bash) или ключами:
#   --heroku-dir DIR    каталог бота (HEROKU_DIR), по умолчанию ~пользователя/Heroku
#   --user NAME         учётная запись службы панели (HKC_SERVICE_USER), по умолчанию владелец каталога бота
#   --dir DIR           каталог установки (HKC_SOURCE_DIR), по умолчанию /opt/hrk-console
#   --addr HOST:PORT    адрес панели (HKC_WEB_ADDR), по умолчанию 127.0.0.1:8080
#   --version vX.Y.Z    конкретный релиз вместо последнего
#   --dry-run           только проверить окружение, скачать и сверить релиз, ничего не меняя
#
# Повторный запуск не трогает существующий /etc/hkc/hkc.env и административный токен:
# он переустанавливает бинарник выбранного релиза и шаблоны служб.
set -euo pipefail

REPOSITORY="https://github.com/ayanamisuicide/hrk-console"
HEROKU_DIR="${HEROKU_DIR:-}"
SERVICE_USER="${HKC_SERVICE_USER:-}"
SOURCE_DIR="${HKC_SOURCE_DIR:-/opt/hrk-console}"
WEB_ADDR="${HKC_WEB_ADDR:-127.0.0.1:8080}"
VERSION="${HKC_VERSION:-}"
DRY_RUN=0
ENV_FILE=/etc/hkc/hkc.env
UPDATE_DIR=/var/lib/hkc/updates

say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mошибка:\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --heroku-dir) HEROKU_DIR="${2:?}"; shift 2 ;;
    --user) SERVICE_USER="${2:?}"; shift 2 ;;
    --dir) SOURCE_DIR="${2:?}"; shift 2 ;;
    --addr) WEB_ADDR="${2:?}"; shift 2 ;;
    --version) VERSION="${2:?}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help)
      echo "использование: install.sh [--heroku-dir DIR] [--user NAME] [--dir DIR] [--addr HOST:PORT] [--version vX.Y.Z] [--dry-run]"
      exit 0 ;;
    *) die "неизвестный параметр: $1" ;;
  esac
done

# ---------- Проверка окружения ----------
[ "$(uname -s)" = Linux ] || die "нужен Linux или WSL"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "нет сборки для архитектуры $(uname -m); поддерживаются amd64 и arm64" ;;
esac
for tool in git curl tar sha256sum python3 install sed; do
  command -v "$tool" >/dev/null 2>&1 || die "не найдена команда $tool; установите её (например: apt install git curl python3)"
done
if [ "$DRY_RUN" = 0 ]; then
  [ "$(id -u)" = 0 ] || die "запустите установщик от root: curl … | sudo bash"
  [ -d /run/systemd/system ] || die "systemd не запущен; в WSL включите его в /etc/wsl.conf ([boot] systemd=true) и перезапустите WSL"
fi

# Пользователь, вызвавший sudo, — разумный владелец бота по умолчанию.
invoker="${SUDO_USER:-$(id -un)}"
home_of() { getent passwd "$1" | cut -d: -f6; }
if [ -z "$HEROKU_DIR" ]; then
  HEROKU_DIR="$(home_of "$invoker")/Heroku"
fi
[ -d "$HEROKU_DIR" ] || die "каталог бота $HEROKU_DIR не найден; укажите --heroku-dir"
HEROKU_DIR="$(cd "$HEROKU_DIR" && pwd -P)"
[ -x "$HEROKU_DIR/venv/bin/python3" ] || [ -x "$HEROKU_DIR/.venv/bin/python3" ] ||
  warn "в $HEROKU_DIR нет venv/.venv: панель не сможет запустить бота, пока окружение не создано"
if [ -z "$SERVICE_USER" ]; then
  SERVICE_USER="$(stat -c %U "$HEROKU_DIR")"
fi
case "$SERVICE_USER" in
  ''|*[[:space:]]*) die "некорректное имя пользователя службы: '$SERVICE_USER'" ;;
esac
getent passwd "$SERVICE_USER" >/dev/null || die "пользователь $SERVICE_USER не существует"
SERVICE_HOME="$(home_of "$SERVICE_USER")"
case "$SOURCE_DIR" in /*) ;; *) die "--dir должен быть абсолютным путём" ;; esac

# ---------- Выбор и проверка релиза ----------
if [ -z "$VERSION" ]; then
  # Публичная переадресация /releases/latest не требует GitHub API и токена.
  latest="$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$REPOSITORY/releases/latest")"
  VERSION="${latest##*/}"
fi
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "не удалось определить стабильный релиз (получено: '$VERSION')"
say "релиз $VERSION, архитектура linux-$ARCH, пользователь службы $SERVICE_USER"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
archive="hkc-web-$VERSION-linux-$ARCH.tar.gz"
say "скачиваем $archive"
curl -fsSL --proto '=https' -o "$work/$archive" "$REPOSITORY/releases/download/$VERSION/$archive" ||
  die "в релизе $VERSION нет сборки для linux-$ARCH"
curl -fsSL --proto '=https' -o "$work/$archive.sha256" "$REPOSITORY/releases/download/$VERSION/$archive.sha256"
read -r expected name < "$work/$archive.sha256"
[ "${name#\*}" = "$archive" ] || die "файл контрольной суммы относится к другому архиву"
[ "$(sha256sum "$work/$archive" | cut -d' ' -f1)" = "$expected" ] || die "контрольная сумма SHA-256 не совпадает"
# В архиве ровно один обычный файл hkc-web; другие пути не распаковываются.
members="$(tar -tzf "$work/$archive")"
[ "$members" = hkc-web ] || [ "$members" = ./hkc-web ] || die "неожиданное содержимое архива"
tar -xzf "$work/$archive" -C "$work" --no-same-owner "$members"
binary="$work/hkc-web"
[ -f "$binary" ] && [ ! -L "$binary" ] || die "в архиве нет обычного файла hkc-web"
[ "$(head -c4 "$binary" | od -An -tx1 | tr -d ' \n')" = 7f454c46 ] || die "hkc-web не является исполняемым файлом Linux"
chmod 0755 "$binary"
metadata="$("$binary" --version-json)"
commit="$(printf '%s' "$metadata" | python3 -c 'import json,sys; m=json.load(sys.stdin); print(m["commit"] if m.get("version")==sys.argv[1] and not m.get("modified") else "")' "$VERSION")"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "встроенная версия бинарника не совпадает с $VERSION"
say "архив проверен: SHA-256, структура и версия $VERSION (${commit:0:12})"

if [ "$DRY_RUN" = 1 ]; then
  say "проверка завершена, система не изменялась (--dry-run)"
  echo "  каталог установки: $SOURCE_DIR"
  echo "  каталог бота:      $HEROKU_DIR"
  echo "  адрес панели:      http://$WEB_ADDR"
  exit 0
fi

# ---------- Исходники ----------
git_safe() { git -c core.hooksPath=/dev/null -C "$SOURCE_DIR" "$@"; }
if [ -d "$SOURCE_DIR/.git" ]; then
  say "обновляем существующую копию $SOURCE_DIR"
  [ "$(git_safe symbolic-ref --short HEAD)" = main ] || die "$SOURCE_DIR: перед установкой выберите ветку main"
  [ -z "$(git_safe status --porcelain --untracked-files=normal)" ] || die "$SOURCE_DIR: есть локальные изменения; установка отменена, чтобы их сохранить"
  git_safe fetch --no-tags "$REPOSITORY.git" "refs/tags/$VERSION:refs/tags/$VERSION"
  git_safe merge --ff-only "$VERSION" || die "$SOURCE_DIR: история расходится с $VERSION"
elif [ -e "$SOURCE_DIR" ] && [ -n "$(ls -A "$SOURCE_DIR")" ]; then
  die "$SOURCE_DIR уже существует и не является копией hrk-console"
else
  say "клонируем исходники в $SOURCE_DIR"
  git clone --quiet --no-checkout "$REPOSITORY.git" "$SOURCE_DIR"
  git_safe fetch --quiet --no-tags origin "refs/tags/$VERSION:refs/tags/$VERSION"
  # Локальная main указывает ровно на релиз: Центр обновления продвигает её только fast-forward.
  git_safe checkout --quiet -B main "$VERSION"
fi
[ "$(git_safe rev-parse HEAD)" = "$commit" ] || die "коммит исходников не совпадает с бинарником $VERSION"
install -d -m 0755 "$SOURCE_DIR/bin"
install -m 0755 "$binary" "$SOURCE_DIR/bin/.hkc-web.new"
mv -f "$SOURCE_DIR/bin/.hkc-web.new" "$SOURCE_DIR/bin/hkc-web"

# ---------- Окружение службы ----------
install -d -m 0700 /etc/hkc "$UPDATE_DIR"
if [ -f "$ENV_FILE" ]; then
  say "$ENV_FILE уже существует: настройки и токен сохранены"
else
  token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  auth_file="$SERVICE_HOME/.config/hkc/web-auth.json"
  # Значения в кавычках JSON: так их одинаково читают systemd и python-установщик.
  (
    umask 077
    {
      printf 'HEROKU_DIR=%s\n' "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$HEROKU_DIR")"
      printf 'HKC_ADMIN_TOKEN="%s"\n' "$token"
      printf 'HKC_WEB_ADDR="%s"\n' "$WEB_ADDR"
      printf 'HKC_AUTH_FILE=%s\n' "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$auth_file")"
      printf 'HKC_SOURCE_DIR=%s\n' "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$SOURCE_DIR")"
      printf 'HKC_SERVICE_USER="%s"\n' "$SERVICE_USER"
      printf 'HKC_UPDATE_DIR="%s"\n' "$UPDATE_DIR"
      printf 'HKC_UPDATE_ENABLED="1"\n'
      printf 'HKC_UPDATE_HEALTH_URL="http://%s"\n' "$WEB_ADDR"
      printf '# Уведомления в Telegram: токен от @BotFather и ID чата, затем systemctl restart hkc-web\n'
      printf '#HKC_TELEGRAM_BOT_TOKEN=""\n#HKC_TELEGRAM_CHAT_ID=""\n'
    } > "$ENV_FILE"
  )
  install -d -m 0700 -o "$SERVICE_USER" -g "$(id -gn "$SERVICE_USER")" "$SERVICE_HOME/.config/hkc"
  say "создан $ENV_FILE с новым административным токеном"
fi

# ---------- Службы systemd ----------
for unit in hkc-web.service hkc-update.service; do
  sed -e "s|__HKC_SOURCE_DIR__|$SOURCE_DIR|g" -e "s|__HKC_SERVICE_USER__|$SERVICE_USER|g" \
    "$SOURCE_DIR/deploy/$unit" > "/etc/systemd/system/$unit.tmp"
  chmod 0644 "/etc/systemd/system/$unit.tmp"
  mv -f "/etc/systemd/system/$unit.tmp" "/etc/systemd/system/$unit"
done
sudoers=/etc/sudoers.d/hkc-update
if [ "$SERVICE_USER" != root ]; then
  # Узкое правило: пользователь панели может только запустить службу обновления.
  printf '%s ALL=(root) NOPASSWD: /usr/bin/systemctl start --no-block hkc-update.service\n' "$SERVICE_USER" > "$sudoers.tmp"
  chmod 0440 "$sudoers.tmp"
  if command -v visudo >/dev/null 2>&1; then visudo -cf "$sudoers.tmp" >/dev/null || die "правило sudo не прошло проверку"; fi
  mv -f "$sudoers.tmp" "$sudoers"
else
  rm -f "$sudoers"
fi
systemctl daemon-reload
systemctl enable --quiet hkc-web.service
systemctl restart hkc-web.service

say "ждём ответа панели"
for _ in $(seq 1 30); do
  if curl -fsS "http://$WEB_ADDR/api/version" 2>/dev/null | grep -q "$commit"; then
    say "hrk-console $VERSION работает: http://$WEB_ADDR"
    echo
    echo "  Админка:            http://$WEB_ADDR/admin/"
    echo "  Токен администратора: sudo grep HKC_ADMIN_TOKEN $ENV_FILE"
    echo "  Журнал службы:      journalctl -u hkc-web -f"
    echo "  Перед первым фоновым запуском войдите в Telegram вручную из $HEROKU_DIR."
    exit 0
  fi
  sleep 1
done
die "панель не ответила за 30 секунд; смотрите journalctl -u hkc-web"
