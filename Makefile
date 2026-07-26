GO ?= go
BIN := bin/hkc

# Версия берётся из git-тега: у сборки из рабочего дерева она получает
# суффикс с числом коммитов и -dirty, поэтому «у меня другая версия»
# видно сразу, без сверки хешей.
VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test vet clean install

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/hkc

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
