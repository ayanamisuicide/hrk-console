# Можно подменить команду Go; результат сборки всегда находится в bin.
GO ?= go
BIN := bin/hkc-web
# Версия и коммит встраиваются в бинарник для /api/version и проверок установщика.
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD)

# Сборка без C-зависимостей подходит для переносимого Linux-релиза.
CGO_ENABLED ?= 0
export CGO_ENABLED

.PHONY: build test vet clean

# Сборка включает статические ресурсы через go:embed.
build:
	$(GO) build -ldflags "-X main.buildVersion=$(VERSION) -X main.buildCommit=$(COMMIT)" -o $(BIN) ./cmd/hkc-web
	@echo "собрано: $(BIN)"

# Быстрые тесты всех пакетов; проверка гонок запускается отдельно в Linux/WSL.
test:
	$(GO) test ./...

# Статические проверки ошибок использования конструкций Go.
vet:
	$(GO) vet ./...

# Удаляет только каталог результатов сборки, не исходники и не данные бота.
clean:
	rm -rf bin
