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
#
# В терминале вывод живой: этапы со спиннером, прогресс загрузки и общий прогресс.
# Без терминала или с NO_COLOR печатаются простые строки — их удобно читать в журналах.
set -euo pipefail

REPOSITORY="https://github.com/ayanamisuicide/hrk-console"
HEROKU_DIR="${HEROKU_DIR:-}"
SERVICE_USER="${HKC_SERVICE_USER:-}"
SOURCE_DIR="${HKC_SOURCE_DIR:-}"
WEB_ADDR="${HKC_WEB_ADDR:-127.0.0.1:8080}"
VERSION="${HKC_VERSION:-}"
DRY_RUN=0
ENV_FILE=/etc/hkc/hkc.env
UPDATE_DIR=/var/lib/hkc/updates

usage() {
  cat <<'TEXT'
Установка hrk-console — веб-панели для юзербота Heroku.

  install.sh [параметры]

  --heroku-dir DIR    каталог бота (по умолчанию ~/Heroku того, кто вызвал sudo)
  --user NAME         от чьего имени работает панель (по умолчанию владелец каталога бота)
  --dir DIR           куда установить (по умолчанию /opt/hrk-console)
  --addr HOST:PORT    адрес панели (по умолчанию 127.0.0.1:8080)
  --version vX.Y.Z    конкретный релиз вместо последнего
  --dry-run           только проверить систему и релиз, ничего не меняя
TEXT
}

while [ $# -gt 0 ]; do
  case "$1" in
    --heroku-dir) HEROKU_DIR="${2:?}"; shift 2 ;;
    --user) SERVICE_USER="${2:?}"; shift 2 ;;
    --dir) SOURCE_DIR="${2:?}"; shift 2 ;;
    --addr) WEB_ADDR="${2:?}"; shift 2 ;;
    --version) VERSION="${2:?}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'неизвестный параметр: %s\n\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

# ---------- Оформление ----------
work="$(mktemp -d)"
LOG="$work/install.log"
: > "$LOG"
FANCY=0
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ]; then FANCY=1; fi
if [ "$FANCY" = 1 ]; then
  B=$'\033[1m'; D=$'\033[2m'; R=$'\033[0m'
  RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; BLUE=$'\033[34m'; MAGENTA=$'\033[35m'; CYAN=$'\033[36m'
  CLEAR=$'\r\033[2K'
else
  B=''; D=''; R=''; RED=''; GREEN=''; YELLOW=''; BLUE=''; MAGENTA=''; CYAN=''; CLEAR=''
fi
COLUMNS_NOW=$( (tput cols 2>/dev/null || echo 80) | head -1)
[ "$COLUMNS_NOW" -ge 40 ] 2>/dev/null || COLUMNS_NOW=80
WIDTH=$(( COLUMNS_NOW > 84 ? 84 : COLUMNS_NOW ))

TOTAL_STEPS=9
[ "$DRY_RUN" = 1 ] && TOTAL_STEPS=4
STEP=0
SPINNER_PID=""
STEP_NAME=""
STEP_STARTED=0
WARNINGS=()

# Ширина строк считается в символах: в UTF-8 кириллица занимает два байта.
if locale -a 2>/dev/null | grep -qiE '^c\.utf-?8$'; then export LC_CTYPE=C.UTF-8; fi
pad() { local text=$1 width=$2; printf '%s%*s' "$text" $(( width > ${#text} ? width - ${#text} : 0 )) ""; }

now_ms() { date +%s%3N 2>/dev/null || echo $(( $(date +%s) * 1000 )); }

# Человекочитаемый объём: 8.4 МБ.
human() {
  local bytes=${1:-0}
  awk -v b="$bytes" 'BEGIN { split("Б КБ МБ ГБ", u, " "); i = 1; while (b >= 1024 && i < 4) { b /= 1024; i++ } printf (i == 1 ? "%d %s" : "%.1f %s"), b, u[i] }'
}

# Полоса из блоков заданной ширины: заполненная часть цветом, остальное — тенью.
bar() {
  local percent=$1 size=$2 color=${3:-$CYAN} filled empty
  filled=$(( percent * size / 100 ))
  empty=$(( size - filled ))
  printf '%s' "$color"
  [ "$filled" -gt 0 ] && printf '█%.0s' $(seq 1 "$filled")
  printf '%s%s' "$R" "$D"
  [ "$empty" -gt 0 ] && printf '░%.0s' $(seq 1 "$empty")
  printf '%s' "$R"
}

# Общий прогресс под текущим этапом.
overall_line() {
  local done=$1 percent size
  percent=$(( done * 100 / TOTAL_STEPS ))
  size=$(( WIDTH - 22 ))
  printf '  %s  %s%3d%%%s  %s%d/%d%s' "$(bar "$percent" "$size" "$MAGENTA")" "$B" "$percent" "$R" "$D" "$done" "$TOTAL_STEPS" "$R"
}

banner() {
  local title="hrk-console" subtitle="панель управления юзерботом Heroku" inner=$(( WIDTH - 6 ))
  local mode="установка"
  [ "$DRY_RUN" = 1 ] && mode="проверка без изменений"
  if [ "$FANCY" = 0 ]; then
    printf '== %s: %s ==\n' "$title" "$mode"
    return
  fi
  local line
  line=$(printf '─%.0s' $(seq 1 "$inner"))
  printf '\n  %s╭%s╮%s\n' "$MAGENTA" "$line" "$R"
  printf '  %s│%s  %s%s%s  %s·  %s%*s%s│%s\n' "$MAGENTA" "$R" "$B" "$title" "$R" "$D" "$mode" $(( inner - ${#title} - ${#mode} - 7 )) "" "$MAGENTA" "$R"
  printf '  %s│%s  %s%s%*s%s│%s\n' "$MAGENTA" "$R" "$D" "$subtitle" $(( inner - ${#subtitle} - 2 )) "" "$MAGENTA" "$R"
  printf '  %s╰%s╯%s\n\n' "$MAGENTA" "$line" "$R"
}

# Фоновая анимация текущего этапа. Если этап пишет в $work/progress «всего путь»,
# вместо таймера показывается полоса загрузки с объёмом и скоростью.
spinner_loop() {
  local frames=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏) i=0 started elapsed detail total path size percent speed
  started=$(now_ms)
  while :; do
    elapsed=$(( $(now_ms) - started ))
    detail=$(printf '%s%d.%ds%s' "$D" $(( elapsed / 1000 )) $(( elapsed % 1000 / 100 )) "$R")
    if [ -s "$work/progress" ]; then
      read -r total path < "$work/progress" || true
      # При параллельной загрузке файл собирается из частей: считаем их вместе.
      size=$(stat -c %s "$path" "$path".part* 2>/dev/null | awk '{ s += $1 } END { print s + 0 }')
      speed=$(( elapsed > 0 ? size * 1000 / elapsed : 0 ))
      if [ "${total:-0}" -gt 0 ]; then
        percent=$(( size * 100 / total ))
        [ "$percent" -gt 100 ] && percent=100
        detail="$(bar "$percent" 22 "$CYAN") $B$percent%$R $D$(human "$size") из $(human "$total") · $(human "$speed")/с$R"
      else
        detail="$D$(human "$size") · $(human "$speed")/с$R"
      fi
    fi
    printf '%s  %s%s%s %s %s\n%s%s\033[1A' "$CLEAR" "$CYAN" "${frames[i]}" "$R" "$(pad "$STEP_NAME" 22)" "$detail" "$CLEAR" "$(overall_line "$((STEP - 1))")"
    i=$(( (i + 1) % ${#frames[@]} ))
    sleep 0.08
  done
}

stop_spinner() {
  if [ -n "$SPINNER_PID" ]; then
    kill "$SPINNER_PID" 2>/dev/null || true
    wait "$SPINNER_PID" 2>/dev/null || true
    SPINNER_PID=""
  fi
  rm -f "$work/progress"
}

step() {
  STEP=$(( STEP + 1 ))
  STEP_NAME="$1"
  STEP_STARTED=$(now_ms)
  printf '\n== [%d/%d] %s\n' "$STEP" "$TOTAL_STEPS" "$STEP_NAME" >> "$LOG"
  if [ "$FANCY" = 1 ]; then
    spinner_loop &
    SPINNER_PID=$!
  else
    printf '[%d/%d] %s…\n' "$STEP" "$TOTAL_STEPS" "$STEP_NAME"
  fi
}

# Завершает этап галочкой, итогом и временем.
ok() {
  local summary="${1:-}" elapsed time gap
  elapsed=$(( $(now_ms) - STEP_STARTED ))
  stop_spinner
  if [ "$FANCY" = 1 ]; then
    time=$(printf '%d.%ds' $(( elapsed / 1000 )) $(( elapsed % 1000 / 100 )))
    # Итог этапа — после названия, время прижато к правому краю.
    gap=$(( WIDTH - 28 - ${#summary} - ${#time} ))
    [ "$gap" -gt 1 ] || gap=1
    printf '%s  %s✓%s %s %s%*s%s%s%s\n%s%s' "$CLEAR" "$GREEN" "$R" "$(pad "$STEP_NAME" 22)" "$summary" "$gap" "" "$D" "$time" "$R" "$CLEAR" "$(overall_line "$STEP")"
  else
    printf '      готово%s (%d.%d с)\n' "${summary:+: $summary}" $(( elapsed / 1000 )) $(( elapsed % 1000 / 100 ))
  fi
}

warn() {
  WARNINGS+=("$1")
  printf 'предупреждение: %s\n' "$1" >> "$LOG"
}

# Останавливает установку: крестик у этапа, причина, подсказка и хвост журнала команд.
die() {
  local reason="$1" hint="${2:-}"
  stop_spinner
  if [ "$FANCY" = 1 ]; then
    [ -n "$STEP_NAME" ] && printf '%s  %s✗%s %s\n%s' "$CLEAR" "$RED" "$R" "${STEP_NAME:-Подготовка}" "$CLEAR"
    printf '\n  %s%sНе получилось:%s %s\n' "$B" "$RED" "$R" "$reason"
    [ -n "$hint" ] && printf '  %s→ %s%s\n' "$YELLOW" "$hint" "$R"
  else
    printf 'ошибка: %s\n' "$reason" >&2
    [ -n "$hint" ] && printf 'что сделать: %s\n' "$hint" >&2
  fi
  # В хвост журнала попадает только вывод команд, без заголовков этапов и пустых строк.
  local tail_lines
  tail_lines="$(grep -v -e '^==' -e '^предупреждение:' -e '^[[:space:]]*$' "$LOG" | tail -n 8 || true)"
  if [ -n "$tail_lines" ]; then
    printf '\n  %sПоследние строки журнала:%s\n' "$D" "$R" >&2
    printf '%s\n' "$tail_lines" | sed "s/^/    $D│$R /" >&2
  fi
  cp "$LOG" /tmp/hkc-install.log 2>/dev/null && printf '\n  %sПолный журнал: /tmp/hkc-install.log%s\n' "$D" "$R" >&2
  printf '\n'
  exit 1
}

# Выполняет команду, складывая её вывод в журнал; при ошибке — понятная причина.
run() {
  local reason="$1"
  shift
  printf '$ %s\n' "$*" >> "$LOG"
  "$@" >> "$LOG" 2>&1 || die "$reason"
}

cleanup() {
  local status=$?
  stop_spinner
  [ "$FANCY" = 1 ] && printf '\033[?25h'
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'die "установка прервана" "запустите установщик снова: повторный запуск безопасен"' INT TERM
[ "$FANCY" = 1 ] && printf '\033[?25l'

banner

# ---------- 1. Система ----------
step "Проверка системы"
[ "$(uname -s)" = Linux ] || die "нужен Linux или WSL" "запустите установщик внутри WSL или на Linux-сервере"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "нет сборки для архитектуры $(uname -m)" "поддерживаются amd64 и arm64" ;;
esac
missing=()
for tool in git curl tar sha256sum python3 install sed awk; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
[ ${#missing[@]} -eq 0 ] || die "не хватает программ: ${missing[*]}" "sudo apt install ${missing[*]}"
if [ "$DRY_RUN" = 0 ]; then
  [ "$(id -u)" = 0 ] || die "нужны права root" "запустите так: curl -fsSL … | sudo bash"
  [ -d /run/systemd/system ] || die "systemd не запущен" "в WSL добавьте в /etc/wsl.conf строки [boot] и systemd=true, затем выполните wsl --shutdown в Windows"
fi
# Пользователь, вызвавший sudo, — разумный владелец бота по умолчанию.
invoker="${SUDO_USER:-$(id -un)}"
home_of() { getent passwd "$1" | cut -d: -f6; }
[ -n "$HEROKU_DIR" ] || HEROKU_DIR="$(home_of "$invoker")/Heroku"
[ -d "$HEROKU_DIR" ] || die "не найден каталог бота $HEROKU_DIR" "укажите его явно: … | sudo bash -s -- --heroku-dir /путь/к/Heroku"
HEROKU_DIR="$(cd "$HEROKU_DIR" && pwd -P)"
[ -x "$HEROKU_DIR/venv/bin/python3" ] || [ -x "$HEROKU_DIR/.venv/bin/python3" ] ||
  warn "в $HEROKU_DIR нет venv или .venv — панель не сможет запустить бота, пока окружение не создано"
[ -n "$SERVICE_USER" ] || SERVICE_USER="$(stat -c %U "$HEROKU_DIR")"
case "$SERVICE_USER" in
  ''|*[[:space:]]*) die "некорректное имя пользователя службы: '$SERVICE_USER'" "укажите --user имя" ;;
esac
getent passwd "$SERVICE_USER" >/dev/null || die "пользователь $SERVICE_USER не существует" "укажите --user имя"
SERVICE_HOME="$(home_of "$SERVICE_USER")"
# Каталог не задан явно — берём каталог действующей службы, затем прежний hkc.env,
# и только для новой установки — /opt/hrk-console. Иначе служба и hkc.env разошлись бы.
if [ -z "$SOURCE_DIR" ]; then
  current="$(systemctl show hkc-web.service --property=ExecStart --value 2>/dev/null | sed -n 's/.*path=\([^ ;]*\).*/\1/p')"
  case "$current" in */bin/hkc-web) SOURCE_DIR="${current%/bin/hkc-web}" ;; esac
fi
if [ -z "$SOURCE_DIR" ] && [ -r "$ENV_FILE" ]; then
  SOURCE_DIR="$(sed -n 's/^HKC_SOURCE_DIR=//p' "$ENV_FILE" | tail -1 | tr -d '"')"
fi
SOURCE_DIR="${SOURCE_DIR:-/opt/hrk-console}"
case "$SOURCE_DIR" in /*) ;; *) die "--dir должен быть абсолютным путём" ;; esac
ok "linux-$ARCH · пользователь $SERVICE_USER"

# ---------- 2. Релиз ----------
step "Поиск релиза"
if [ -z "$VERSION" ]; then
  # Публичная переадресация /releases/latest не требует GitHub API и токена.
  latest="$(curl -fsSIL -o /dev/null -w '%{url_effective}' "$REPOSITORY/releases/latest" 2>>"$LOG")" ||
    die "GitHub недоступен" "проверьте интернет: curl -I https://github.com"
  VERSION="${latest##*/}"
fi
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "не удалось определить стабильный релиз (получено: '$VERSION')"
ok "$VERSION"

# ---------- 3. Загрузка ----------
archive="hkc-web-$VERSION-linux-$ARCH.tar.gz"
base="$REPOSITORY/releases/download/$VERSION"
# Качает файл параллельно частями (HTTP Range). CDN релизов GitHub у части провайдеров
# режет скорость каждого соединения, и восемь соединений дают восьмикратный выигрыш.
# Если части не поддерживаются или что-то не вышло — обычная загрузка одним потоком.
download_parallel() {
  local url=$1 out=$2 size=$3 final streams=8 part index first last pids=()
  final="$(curl -fsSIL -o /dev/null -w '%{url_effective}' --proto '=https' "$url" 2>>"$LOG")" || return 1
  case "$final" in https://*) ;; *) return 1 ;; esac
  [ "$size" -ge 2097152 ] 2>/dev/null || return 1
  part=$(( (size + streams - 1) / streams ))
  for index in $(seq 0 $(( streams - 1 ))); do
    first=$(( index * part ))
    [ "$first" -lt "$size" ] || break
    last=$(( first + part - 1 ))
    [ "$last" -lt "$size" ] || last=$(( size - 1 ))
    curl -fsS --proto '=https' -r "$first-$last" -o "$out.part$index" "$final" 2>>"$LOG" &
    pids+=($!)
  done
  for index in "${pids[@]}"; do wait "$index" || return 1; done
  cat $(for index in $(seq 0 $(( ${#pids[@]} - 1 ))); do printf '%s.part%s ' "$out" "$index"; done) > "$out" || return 1
  rm -f "$out".part*
  [ "$(stat -c %s "$out")" = "$size" ]
}

step "Загрузка панели"
size=$(curl -fsIL "$base/$archive" 2>>"$LOG" | tr -d '\r' | awk 'tolower($1) == "content-length:" { value = $2 } END { print value + 0 }') || size=0
printf '%s %s\n' "$size" "$work/$archive" > "$work/progress"
if ! download_parallel "$base/$archive" "$work/$archive" "$size"; then
  rm -f "$work/$archive" "$work/$archive".part*
  curl -fsSL --proto '=https' -o "$work/$archive" "$base/$archive" 2>>"$LOG" ||
    die "в релизе $VERSION нет сборки для linux-$ARCH" "проверьте список файлов: $REPOSITORY/releases/tag/$VERSION"
fi
curl -fsSL --proto '=https' -o "$work/$archive.sha256" "$base/$archive.sha256" 2>>"$LOG" ||
  die "не удалось скачать контрольную сумму"
ok "$(human "$(stat -c %s "$work/$archive")")"

# ---------- 4. Проверка ----------
step "Проверка подлинности"
read -r expected name < "$work/$archive.sha256"
[ "${name#\*}" = "$archive" ] || die "файл контрольной суммы относится к другому архиву"
[ "$(sha256sum "$work/$archive" | cut -d' ' -f1)" = "$expected" ] || die "контрольная сумма SHA-256 не совпадает" "архив повреждён при загрузке — запустите установку ещё раз"
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
ok "SHA-256 · коммит ${commit:0:7}"

summary_line() { printf '  %s%s%s %s\n' "$D" "$(pad "$1" 14)" "$R" "$2"; }

if [ "$DRY_RUN" = 1 ]; then
  printf '\n\n  %s%s✓ Всё готово к установке.%s Система не изменялась.\n\n' "$B" "$GREEN" "$R"
  summary_line "Версия" "$VERSION"
  summary_line "Каталог" "$SOURCE_DIR"
  summary_line "Бот" "$HEROKU_DIR"
  summary_line "Панель" "http://$WEB_ADDR"
  for item in "${WARNINGS[@]}"; do printf '\n  %s! %s%s' "$YELLOW" "$item" "$R"; done
  printf '\n\n'
  exit 0
fi

# ---------- 5. Исходники ----------
git_safe() { git -c core.hooksPath=/dev/null -C "$SOURCE_DIR" "$@"; }
step "Исходники"
if [ -d "$SOURCE_DIR/.git" ]; then
  [ "$(git_safe symbolic-ref --short HEAD 2>>"$LOG")" = main ] || die "в $SOURCE_DIR выбрана не ветка main" "git -C $SOURCE_DIR checkout main"
  [ -z "$(git_safe status --porcelain --untracked-files=normal)" ] || die "в $SOURCE_DIR есть локальные изменения" "сохраните или отмените их — установщик их не трогает"
  run "не удалось получить $VERSION с GitHub" git_safe fetch --no-tags "$REPOSITORY.git" "refs/tags/$VERSION:refs/tags/$VERSION"
  run "история $SOURCE_DIR расходится с $VERSION" git_safe merge --ff-only "$VERSION"
  sources="обновлены в $SOURCE_DIR"
elif [ -e "$SOURCE_DIR" ] && [ -n "$(ls -A "$SOURCE_DIR")" ]; then
  die "$SOURCE_DIR уже существует и не является копией hrk-console" "укажите другой каталог: --dir /путь"
else
  run "не удалось скачать исходники" git clone --no-checkout "$REPOSITORY.git" "$SOURCE_DIR"
  run "не удалось получить $VERSION" git_safe fetch --no-tags origin "refs/tags/$VERSION:refs/tags/$VERSION"
  # Локальная main указывает ровно на релиз: Центр обновления продвигает её только fast-forward.
  run "не удалось переключиться на $VERSION" git_safe checkout -B main "$VERSION"
  sources="$SOURCE_DIR"
fi
[ "$(git_safe rev-parse HEAD)" = "$commit" ] || die "коммит исходников не совпадает с бинарником $VERSION"
ok "$sources"

# ---------- 6. Бинарник ----------
step "Установка панели"
install -d -m 0755 "$SOURCE_DIR/bin"
install -m 0755 "$binary" "$SOURCE_DIR/bin/.hkc-web.new"
mv -f "$SOURCE_DIR/bin/.hkc-web.new" "$SOURCE_DIR/bin/hkc-web"
ok "$SOURCE_DIR/bin/hkc-web"

# ---------- 7. Окружение ----------
step "Настройка"
install -d -m 0700 /etc/hkc
# Каталог состояния открыт на чтение: панель показывает ход обновления; резервные сборки внутри закрыты.
install -d -m 0755 "$UPDATE_DIR"
json() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"; }
if [ -f "$ENV_FILE" ]; then
  configured="настройки сохранены"
  # Каталог установки в hkc.env должен совпадать со службами: его читает служба обновления.
  if ! grep -qxF "HKC_SOURCE_DIR=$(json "$SOURCE_DIR")" "$ENV_FILE"; then
    tmp_env="$(mktemp "$ENV_FILE.XXXXXX")"
    { grep -v '^HKC_SOURCE_DIR=' "$ENV_FILE" || true; printf 'HKC_SOURCE_DIR=%s\n' "$(json "$SOURCE_DIR")"; } > "$tmp_env"
    chmod 0600 "$tmp_env"
    mv -f "$tmp_env" "$ENV_FILE"
    configured="настройки сохранены · каталог $SOURCE_DIR"
  fi
else
  token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  # Значения в кавычках JSON: так их одинаково читают systemd и python-установщик.
  (
    umask 077
    {
      printf 'HEROKU_DIR=%s\n' "$(json "$HEROKU_DIR")"
      printf 'HKC_ADMIN_TOKEN="%s"\n' "$token"
      printf 'HKC_WEB_ADDR="%s"\n' "$WEB_ADDR"
      printf 'HKC_AUTH_FILE=%s\n' "$(json "$SERVICE_HOME/.config/hkc/web-auth.json")"
      printf 'HKC_SOURCE_DIR=%s\n' "$(json "$SOURCE_DIR")"
      printf 'HKC_SERVICE_USER="%s"\n' "$SERVICE_USER"
      printf 'HKC_UPDATE_DIR="%s"\n' "$UPDATE_DIR"
      printf 'HKC_UPDATE_ENABLED="1"\n'
      printf 'HKC_UPDATE_HEALTH_URL="http://%s"\n' "$WEB_ADDR"
      printf '# Уведомления в Telegram: токен от @BotFather и ID чата, затем systemctl restart hkc-web\n'
      printf '#HKC_TELEGRAM_BOT_TOKEN=""\n#HKC_TELEGRAM_CHAT_ID=""\n'
    } > "$ENV_FILE"
  )
  install -d -m 0700 -o "$SERVICE_USER" -g "$(id -gn "$SERVICE_USER")" "$SERVICE_HOME/.config/hkc"
  configured="новый токен администратора"
fi
ok "$configured"

# ---------- 8. Службы ----------
step "Службы systemd"
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
  if command -v visudo >/dev/null 2>&1; then run "правило sudo не прошло проверку" visudo -cf "$sudoers.tmp"; fi
  mv -f "$sudoers.tmp" "$sudoers"
else
  rm -f "$sudoers"
fi
run "systemd не перечитал службы" systemctl daemon-reload
run "не удалось включить автозапуск" systemctl enable hkc-web.service
ok "hkc-web · hkc-update"

# ---------- 9. Запуск ----------
step "Запуск панели"
run "служба hkc-web не запустилась" systemctl restart hkc-web.service
started=0
for _ in $(seq 1 30); do
  if curl -fsS "http://$WEB_ADDR/api/version" 2>/dev/null | grep -q "$commit"; then
    started=1
    break
  fi
  sleep 1
done
if [ "$started" = 0 ]; then
  journalctl -u hkc-web -n 20 --no-pager >> "$LOG" 2>&1 || true
  die "панель не ответила за 30 секунд" "посмотрите журнал службы: journalctl -u hkc-web -e"
fi
ok "http://$WEB_ADDR"

# ---------- Итог ----------
if [ "$FANCY" = 1 ]; then
  printf '\n\n  %s%s✓ hrk-console %s установлена и работает%s\n\n' "$B" "$GREEN" "$VERSION" "$R"
else
  printf '\nhrk-console %s установлена и работает\n\n' "$VERSION"
fi
summary_line "Панель" "${B}http://$WEB_ADDR${R}"
summary_line "Админка" "http://$WEB_ADDR/admin/"
summary_line "Токен админа" "sudo grep HKC_ADMIN_TOKEN $ENV_FILE"
summary_line "Журнал" "journalctl -u hkc-web -f"
summary_line "Настройки" "$ENV_FILE"
printf '\n  %sДальше:%s откройте админку, введите токен и создайте инвайт для своего аккаунта.\n' "$B" "$R"
printf '  %sПеред первым фоновым запуском один раз войдите в Telegram вручную из %s.%s\n' "$D" "$HEROKU_DIR" "$R"
for item in "${WARNINGS[@]}"; do printf '\n  %s! %s%s' "$YELLOW" "$item" "$R"; done
printf '\n\n'
