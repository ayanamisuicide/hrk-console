# hrk-console

[![CI](https://github.com/ayanamisuicide/hrk-console/actions/workflows/ci.yml/badge.svg)](https://github.com/ayanamisuicide/hrk-console/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ayanamisuicide/hrk-console?label=release)](https://github.com/ayanamisuicide/hrk-console/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ayanamisuicide/hrk-console)](go.mod)

**hrk-console 2.0** — веб-панель управления Telegram-юзерботом [Heroku](https://github.com/ZetGoHack/Heroku). Запуск, остановка, перезапуск и живой просмотр лога доступны из браузера.

Сервер работает рядом с ботом на Linux/WSL, читает `heroku.log` и управляет процессом напрямую. Пользователи работают только через сайт; TUI, нативных окон Linux/Windows и SSH-клиента в проекте больше нет.

## Быстрый старт

```sh
git clone https://github.com/ayanamisuicide/hrk-console ~/heroku-console
cd ~/heroku-console
make build

export HEROKU_DIR=/путь/к/Heroku
export HKC_ADMIN_TOKEN="$(openssl rand -hex 32)"
./bin/hkc-web
```

Откройте <http://localhost:8080/admin/> и введите значение `HKC_ADMIN_TOKEN`. Пользовательские аккаунты создаются по одноразовым инвайтам из админ-панели.

> [!IMPORTANT]
> Перед первым запуском нужно один раз войти в аккаунт Telegram вручную: веб-панель запускает бота в фоне и не может ответить на интерактивные вопросы Telegram.

## Документация

| | |
| --- | --- |
| [Веб-интерфейс](docs/web.md) | Сборка, запуск, инвайты, администрирование и публикация через HTTPS |
| [Changelog](CHANGELOG.md) | История версий |

## Разработка

```sh
make test
make vet
make build
```

Стабильные Linux-сборки веб-сервера публикуются в [GitHub Releases](https://github.com/ayanamisuicide/hrk-console/releases/latest). Открывать панель можно браузером с любой операционной системы.
