GO ?= go
BIN := bin/hkc-web
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD)

CGO_ENABLED ?= 0
export CGO_ENABLED

.PHONY: build test vet clean

build:
	$(GO) build -ldflags "-X main.buildVersion=$(VERSION) -X main.buildCommit=$(COMMIT)" -o $(BIN) ./cmd/hkc-web
	@echo "собрано: $(BIN)"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin
