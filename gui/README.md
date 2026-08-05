# gui

Нативное GUI-окно для того же Telegram-юзербота [Heroku](https://github.com/coddrago/Heroku),
что и [`hkc`](../README.md) — отдельный Go-модуль внутри этого же репозитория,
переиспользующий `botproc`/`logfeed`/`state` из корня напрямую (через `replace`
в `go.mod`, см. `../README.md#состав`), но вместо терминального TUI — окно на
Wails: Go-бэкенд + вёрстка в нативном webview. Визуал сознательно повторяет
TUI-консоль: тёмная btop/htop-палитра, градиентные meter-бары модулей, те же
контролы (debug, порог показа, авто-перезапуск бота).

Бот по умолчанию ищется в `~/Heroku`, каталог переопределяется переменной
`HEROKU_DIR` — как и у `hkc`.

## Разработка

`wails dev` — Vite hot-reload фронтенда + доступ к Go-методам из devtools на
`http://localhost:34115`.

## Сборка

На Ubuntu/Mint 24.04 доступен только `webkit2gtk-4.1` (пакета `-4.0` в
репозиториях больше нет), поэтому собирать нужно с тегом:

```sh
wails build -tags webkit2_41
```

Бинарник — `build/bin/hrk-console-gui`.
