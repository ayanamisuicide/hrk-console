GO ?= go
BIN := bin/hkc-web

CGO_ENABLED ?= 0
export CGO_ENABLED

.PHONY: build test vet clean

build:
	$(GO) build -o $(BIN) ./cmd/hkc-web
	@echo "собрано: $(BIN)"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin
