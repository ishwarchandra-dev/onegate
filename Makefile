# OneGate — build & developer workflows
#
# Graph engineering workflow: see docs/graph-engineering.md
# Task board: `make graph`

BINARY  := onegate
PKG     := github.com/ishwarchandra-dev/onegate
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(PKG)/internal/version.Version=$(VERSION) \
           -X $(PKG)/internal/version.GitCommit=$(COMMIT) \
           -X $(PKG)/internal/version.BuildDate=$(DATE)

.PHONY: build run test vet fmt lint graph web-install web-dev web clean

## build: compile the onegate binary into bin/
build:
        go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

## run: build + run locally (config via flags for now)
run:
        go run -ldflags "$(LDFLAGS)" ./cmd/$(BINARY)

## test: run all Go tests
test:
        go test ./...

## vet: static analysis
vet:
        go vet ./...

## fmt: format Go sources
fmt:
        gofmt -s -w .

## lint: golangci-lint (requires https://golangci-lint.run)
lint:
        golangci-lint run

## graph: print the task-graph board (phases 0-9)
graph:
        @python3 scripts/graph_status.py

## web-install: install dashboard dependencies (bun)
web-install:
        cd web && bun install

## web-dev: run the dashboard dev server (proxies /api to :7420)
web-dev:
        cd web && bun run dev

## web: production build of the dashboard (output: web/build)
web:
        cd web && bun run build

## web-typecheck: typecheck the dashboard
web-typecheck:
        cd web && bun run typecheck

## clean: remove build artifacts
clean:
        rm -rf bin web/build web/.react-router
