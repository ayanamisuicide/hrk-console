#!/usr/bin/env bash
# Дефолтная консоль Heroku: бот работает прямо в этом окне, со своим tty,
# видно его собственный вывод (баннер, "🪐 Heroku ... started", INFO-логи —
# logging.StreamHandler в heroku/log.py пишет в stderr).

source "$(dirname "$(readlink -f "$0")")/ui.sh"

cd "$HEROKU_DIR" || exit 1

clear
banner_art
echo
hr
center "HEROKU USERBOT · родная консоль" \
       "  ${B}${MAG}HEROKU USERBOT${R}  ${D}·${R}  ${GRY}родная консоль бота${R}"
center "закрытие этого окна остановит бота" \
       "  ${D}закрытие этого окна остановит бота${R}"
hr
echo

# shellcheck disable=SC1091
source venv/bin/activate 2>/dev/null || { fail "venv не найден"; exec bash; }

# set -m включает управление задачами: бот получает СВОЮ группу процессов.
# Без этого .restart из Telegram (killpg в heroku/_internal.py: die()) убил бы
# и этот bash вместе с окном. Проверено: с set -m pgid ребёнка ≠ pgid шелла.
set -m

python3 -m heroku

echo
printf "${YEL}  Бот завершился (код %s). Окно оставлено открытым.${R}\n" "$?"
exec bash
