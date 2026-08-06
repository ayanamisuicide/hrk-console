GO ?= go
BIN := bin/hkc

# Проект не использует cgo (никаких import "C" — /proc и syscall читаются
# напрямую), но go build включает cgo сам, если в PATH нашёлся gcc. На
# системах с gcc, но без заголовков libc (build-essential не ставился
# отдельно — обычное дело на минимальных Ubuntu/WSL-образах) сборка падает
# на компиляции runtime/cgo, хотя самому проекту он не нужен вообще.
# release.yml уже отключает cgo явно — здесь то же самое, чтобы `make build`
# работал одинаково что в CI, что на голой системе.
CGO_ENABLED ?= 0
export CGO_ENABLED

# Версия берётся из git-тега: у сборки из рабочего дерева она получает
# суффикс с числом коммитов и -dirty, поэтому «у меня другая версия»
# видно сразу, без сверки хешей.
VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo dev)
# Путь к исходникам вшивается в бинарник: после `make install` он лежит в
# ~/.local/bin и сам по себе репозиторий рядом не находит, а прогон тестов
# при старте без исходников невозможен.
LDFLAGS := -X main.version=$(VERSION) -X heroku-console/internal/preflight.repoRoot=$(CURDIR)

.PHONY: build gui test vet clean install

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/hkc

# GUI собирается своим тулчейном (wails тянет ещё и сборку фронтенда), а сам
# он живёт в GOPATH/bin, которого может не быть в PATH — поэтому зовём по
# полному пути, а не рассчитываем, что "wails" найдётся сам.
#
# webkit2_41: на Ubuntu/Mint 24.04 в репозиториях остался только
# webkit2gtk-4.1, а wails по умолчанию ищет -4.0 и без тега не собирается.
gui:
	cd gui && $(shell $(GO) env GOPATH)/bin/wails build -tags webkit2_41
	@echo "собрано: gui/build/bin/hrk-console-gui"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin

# Кладёт бинарник в ~/.local/bin, чтобы "hkc" вызывался из любого места.
install: build
	install -Dm755 $(BIN) $(HOME)/.local/bin/hkc
	@echo "версия: $(VERSION)"
	@echo "установлено в ~/.local/bin/hkc"
