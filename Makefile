BINARY := leo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)
GOFLAGS := -trimpath
# Canonical install location — matches install.sh and config.DefaultRemoteLeoPath
# ($HOME/.local/bin/leo). `go install` would land in GOBIN/~/go/bin, which the
# running daemon and remote dispatch do NOT look at, so a `make install` fix
# would silently not go live. Override with `make install INSTALL_DIR=...`.
INSTALL_DIR ?= $(HOME)/.local/bin

# Lint tooling — mirror the CI Lint job so `make lint` == CI. Keep these three
# in sync with .github/workflows/ci.yml (golangci-lint action version, gosec
# GOSEC_VERSION, and the gosec -exclude list). Tools install into ./bin
# (gitignored) via version-stamped sentinels, so bumping a version here forces
# a reinstall on the next `make lint`.
TOOLBIN := $(CURDIR)/bin
GOLANGCI_VERSION := v2.12.2
GOSEC_VERSION := v2.25.0
GOSEC_EXCLUDE := G104,G204,G304,G306,G602,G702,G703,G704

.PHONY: build install clean test e2e lint fmt coverage docs docs-serve tag demo snapshot

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/leo

install:
	@mkdir -p "$(INSTALL_DIR)"
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$(INSTALL_DIR)/$(BINARY)" ./cmd/leo
	@echo "leo $(VERSION) installed to $(INSTALL_DIR)/$(BINARY)"

clean:
	rm -rf bin/ dist/ coverage.out coverage.html

test:
	go test -race -cover ./...

e2e:
	go test -tags=e2e -v -count=1 ./e2e/...

lint: $(TOOLBIN)/.golangci-$(GOLANGCI_VERSION) $(TOOLBIN)/.gosec-$(GOSEC_VERSION)
	$(TOOLBIN)/golangci-lint run
	$(TOOLBIN)/gosec -quiet -exclude=$(GOSEC_EXCLUDE) ./...

# Version-stamped sentinels: the version is baked into the target name, so a
# bump reinstalls (go install reports "dev" as its own version, so we can't
# check the binary itself). Old sentinels are cleared so stale ones don't linger.
$(TOOLBIN)/.golangci-$(GOLANGCI_VERSION):
	@rm -f $(TOOLBIN)/.golangci-*
	GOBIN=$(TOOLBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@touch $@

$(TOOLBIN)/.gosec-$(GOSEC_VERSION):
	@rm -f $(TOOLBIN)/.gosec-*
	GOBIN=$(TOOLBIN) go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	@touch $@

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

demo:
	bash docs/demo/record.sh
