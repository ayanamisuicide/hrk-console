GO ?= go
BIN := bin/hkc

.PHONY: build test vet clean install

build:
	$(GO) build -o $(BIN) ./cmd/hkc

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin

# Кладёт бинарник в ~/.local/bin, чтобы "hkc" вызывался из любого места.
install: build
	install -Dm755 $(BIN) $(HOME)/.local/bin/hkc
	@echo "установлено в ~/.local/bin/hkc"
