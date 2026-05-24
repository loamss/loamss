.PHONY: all build test test-race lint fmt vet tidy clean install help

# Version metadata, baked into the binary via -ldflags.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -ldflags "\
	-X github.com/loamss/loamss/runtime/internal/cli.version=$(VERSION) \
	-X github.com/loamss/loamss/runtime/internal/cli.commit=$(COMMIT) \
	-X github.com/loamss/loamss/runtime/internal/cli.buildDate=$(BUILD_DATE)"

BINARY := bin/loamss
PREFIX ?= /usr/local

all: build

## build: compile the loamss binary into bin/
build:
	@mkdir -p bin
	go build $(LDFLAGS) -o $(BINARY) ./cmd/loamss

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
