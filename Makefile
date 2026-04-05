BINARY := leo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: build install clean test lint docs docs-serve

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/leo

install:
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/leo

clean:
	rm -rf bin/ dist/

test:
	go test -race -cover ./...

lint:
	go vet ./...
	@which staticcheck > /dev/null 2>&1 && staticcheck ./... || true

snapshot:
	goreleaser release --snapshot --clean

docs:
	mkdocs build --strict

docs-serve:
	mkdocs serve
