# Установка и первый запуск

## Go

Нужен [Go](https://go.dev) — в репозиториях Ubuntu/Mint версия обычно отстаёт, поэтому надёжнее официальный архив:

```sh
curl -LO "https://go.dev/dl/$(curl -s https://go.dev/VERSION?m=text | head -1).linux-amd64.tar.gz"
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go*.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version
```

Альтернативы — `sudo snap install go --classic` или `sudo apt install golang-go`. Если версия окажется старше нужной, а сеть есть, `go build` сам докачает подходящий тулчейн (`GOTOOLCHAIN=auto`).

## Консоль

```sh
git clone https://github.com/ayanamisuicide/hrk-console ~/heroku-console
cd ~/heroku-console
make build      # соберёт bin/hkc
make install    # опционально: положит бинарник в ~/.local/bin/hkc
```

Бот по умолчанию ищется в `~/Heroku`, каталог переопределяется переменной `HEROKU_DIR`.

## Вход в аккаунт Telegram

Консоль поднимает окружение бота с нуля сама, но **войти в аккаунт вместо вас не может**. При первом запуске Heroku спрашивает `api_id`, `api_hash`, номер телефона и код из Telegram, а hkc и GUI запускают бота в фоне без подключённого ввода — отвечать на эти вопросы будет некому, и бот упадёт с `EOFError`.

Поэтому первый вход делается вручную, один раз.

**1.** Откройте [my.telegram.org/apps](https://my.telegram.org/apps), войдите под нужным номером, создайте приложение (название и платформа не важны) и скопируйте **App api_id** и **App api_hash**.

**2.** Запустите бота в обычном терминале:

```sh
git clone https://github.com/coddrago/Heroku ~/Heroku
cd ~/Heroku
python3 -m venv venv          # именно venv, не .venv — под этим именем его ищут hkc и GUI
source venv/bin/activate
pip install -r requirements.txt
python3 -m heroku
```

Если `~/Heroku` уже есть (его могла склонировать автонастройка `hkc`) — начните с `python3 -m venv venv`.

Введите то, что получили на шаге 1. Появился баннер `🪐 Heroku … started` — вход выполнен, нажмите `Ctrl+C`. Дальше ботом управляет `hkc`.

## Windows

hkc и GUI работают только на Linux: они запускают процесс через `bash` и читают его из `/proc`. Сам бот кросс-платформенный, поэтому есть два пути.

**WSL (рекомендуется).** `wsl --install -d Ubuntu-22.04` в PowerShell от администратора, дальше внутри Ubuntu всё как на обычном Linux — включая сборку и запуск hkc.

**Только бот, без hkc.** Управлять придётся вручную:

```powershell
git clone https://github.com/coddrago/Heroku
cd Heroku
python -m venv venv
venv\Scripts\activate
pip install -r requirements.txt
python -m heroku
```

Остановка — `Ctrl+C`, повторный запуск — `python -m heroku` из активированного `venv`.

## Ярлык на рабочем столе

`launch.sh` открывает новое окно терминала с выбором «Консоль (hkc) / Приложение (GUI)» и сама собирает то, что выбрано, если оно ещё не собрано (`make build` для консоли, `make gui` для GUI — второе требует уже поставленного `wails`, см. [gui/README.md](../gui/README.md#зависимости); системные библиотеки скрипт сам не ставит, только печатает команду). Явный номер режима (`launch.sh 1`…`4`) по-прежнему пропускает этот выбор и сразу открывает консоль в нужном режиме — старое поведение, на него могут полагаться существующие ярлыки. В репозитории лежит `Heroku Logs.desktop` с плейсхолдером вместо пути:

```sh
sed "s|/absolute/path/to/heroku-console|$PWD|" "Heroku Logs.desktop" \
    > ~/.local/share/applications/heroku-logs.desktop
```

Для иконки прямо на столе — то же самое с `~/Desktop` и `chmod +x`. Работает в KDE Plasma и Xfce (в Xfce может понадобиться подтвердить «Разрешить запуск» при первом клике); в GNOME и COSMIC значков на рабочем столе нет вовсе — там только меню приложений.
