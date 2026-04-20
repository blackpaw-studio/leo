BINARY := leo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: build install clean test e2e lint fmt coverage docs docs-serve tag

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/leo

install:
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/leo

clean:
	rm -rf bin/ dist/ coverage.out coverage.html

test:
	go test -race -cover ./...

e2e:
	go test -tags=e2e -v -count=1 ./e2e/...

lint:
	go vet ./...
	@which staticcheck > /dev/null 2>&1 && staticcheck ./... || true

fmt:
	gofmt -w .
	@which goimports > /dev/null 2>&1 && goimports -w . || true

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

snapshot:
	goreleaser release --snapshot --clean

docs:
	mkdocs build --strict

docs-serve:
	mkdocs serve

tag:
	@test -n "$(V)" || (echo "Usage: make tag V=0.1.0" && exit 1)
	git tag -a v$(V) -m "Release v$(V)"
	git push origin v$(V)
