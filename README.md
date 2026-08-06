<div align="center">

# heroku-console

Консоль для Telegram-юзербота [Heroku](https://github.com/coddrago/Heroku).<br>
Запуск, остановка и живой просмотр логов — в терминале или в нативном окне.

[![CI](https://github.com/ayanamisuicide/hrk-console/actions/workflows/ci.yml/badge.svg)](https://github.com/ayanamisuicide/hrk-console/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ayanamisuicide/hrk-console?label=release)](https://github.com/ayanamisuicide/hrk-console/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ayanamisuicide/hrk-console)](go.mod)

</div>

В живом логе бота 96% строк — это `[DEBUG]` от `urllib3`, `git` и внутреннего кэша, и нужные события тонут между ними. Здесь лог разложен по колонкам, шум скрыт по умолчанию, повторы схлопнуты в счётчик `×N`, а слева видно, кто шумит, где копятся ошибки и жив ли процесс.

## Быстрый старт

```sh
git clone https://github.com/ayanamisuicide/hrk-console ~/heroku-console
cd ~/heroku-console
make build
./bin/hkc
```

Нужен [Go](https://go.dev) — если его ещё нет, команды установки есть в [документации](docs/setup.md#go).

Нативное окно вместо терминала собирается отдельно (нужен [Wails](https://wails.io), см. [подробности](docs/usage.md#gui)):

```sh
make gui
./gui/build/bin/hrk-console-gui
```

> [!IMPORTANT]
> Перед первым запуском нужно один раз войти в аккаунт Telegram вручную — консоль запускает бота в фоне и ответить на его вопросы про `api_id` и код из Telegram будет некому. Как это сделать: [docs/setup.md](docs/setup.md#вход-в-аккаунт-telegram).

## Документация

| | |
| --- | --- |
| [Установка и первый запуск](docs/setup.md) | Go, сборка, вход в аккаунт, Windows/WSL, ярлык на рабочем столе |
| [Использование](docs/usage.md) | Режимы, клавиши, GUI, как читается строка лога |
| [Устройство проекта](docs/architecture.md) | Из чего собрано и почему именно так |
| [Changelog](CHANGELOG.md) | История версий |

## Разработка

```sh
make test    # тесты
make vet     # go vet
make build
```
