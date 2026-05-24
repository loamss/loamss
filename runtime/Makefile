.PHONY: all build build-go console-build console-clean test test-race lint fmt vet tidy clean install help

# Version metadata, baked into the binary via -ldflags.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -ldflags "\
	-X github.com/loamss/loamss/runtime/internal/cli.version=$(VERSION) \
	-X github.com/loamss/loamss/runtime/internal/cli.commit=$(COMMIT) \
	-X github.com/loamss/loamss/runtime/internal/cli.buildDate=$(BUILD_DATE)"

BINARY      := bin/loamss
PREFIX      ?= /usr/local
CONSOLE_SRC := ../console
CONSOLE_DST := internal/console/dist

all: build

## build: build the console bundle, embed it, and compile the loamss binary
build: console-build build-go

## build-go: compile the loamss binary without rebuilding the console
##           (use after a recent console-build, or when iterating on Go)
build-go:
	@mkdir -p bin
	go build $(LDFLAGS) -o $(BINARY) ./cmd/loamss

## console-build: produce the Next.js static export and copy it into
##                internal/console/dist/ so //go:embed picks it up.
##                Requires `bun` on PATH; skipped automatically when
##                the console source tree isn't present (e.g. partial
##                checkouts that vendor only the runtime).
console-build:
	@if [ ! -d "$(CONSOLE_SRC)" ]; then \
		echo "console source not found at $(CONSOLE_SRC); using existing dist/ as-is"; \
		exit 0; \
	fi
	@if ! command -v bun >/dev/null 2>&1; then \
		echo "ERROR: bun not on PATH. Install it (https://bun.sh) or run 'make build-go' to compile without rebuilding the console."; \
		exit 1; \
	fi
	@echo "building console (bun run build) in $(CONSOLE_SRC)..."
	@cd $(CONSOLE_SRC) && bun install --frozen-lockfile && bun run build
	@echo "copying $(CONSOLE_SRC)/out/* -> $(CONSOLE_DST)/"
	@rm -rf $(CONSOLE_DST)
	@mkdir -p $(CONSOLE_DST)
	@cp -R $(CONSOLE_SRC)/out/. $(CONSOLE_DST)/
	@# Re-create the .gitkeep marker — the Next.js export doesn't
	@# carry one and git refuses to track empty directories.
	@touch $(CONSOLE_DST)/.gitkeep

## console-clean: restore the dist/ placeholder so future Go-only
##                builds get the "console not built" landing instead
##                of a stale production bundle.
console-clean:
	@rm -rf $(CONSOLE_DST)
	@mkdir -p $(CONSOLE_DST)
	@git checkout -- $(CONSOLE_DST)/index.html $(CONSOLE_DST)/404.html 2>/dev/null || true
	@echo "console dist restored to placeholder (commit a real build with 'make console-build')"

## test: run unit tests
test:
	go test ./...

## test-race: run tests with the race detector
test-race:
	go test -race ./...

## lint: run golangci-lint
lint:
	golangci-lint run

## fmt: format the codebase
fmt:
	gofmt -s -w .

## vet: run go vet
vet:
	go vet ./...

## tidy: refresh go.mod and go.sum
tidy:
	go mod tidy

## clean: remove build artifacts
clean:
	rm -rf bin/

## install: install the binary to $(PREFIX)/bin (default: /usr/local/bin)
install: build
	install -m 0755 $(BINARY) $(PREFIX)/bin/loamss

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | sort
