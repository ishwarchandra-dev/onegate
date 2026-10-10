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

.PHONY: build build-all run test vet fmt lint graph web-install web-dev web web-typecheck api-gen api-check launcher-test clean

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

## web: production build of the dashboard (output: web/build/client)
## + build-time gzip precompression (served by internal/webfs when the
## client sends Accept-Encoding: gzip; already-compressed assets are
## skipped: woff2/png/ico).
web:
	cd web && bun run build
	find web/build/client -type f \
		! -name '*.gz' ! -name '*.woff2' ! -name '*.png' ! -name '*.ico' \
		-exec gzip -9 -k {} \;

## build-all: dashboard + binary in one shot (the single-binary pipeline
## of p9.embed-pipeline; `make build` alone embeds whatever dist exists —
## on a fresh clone that is the placeholder, see ADR 006)
build-all: web build

## web-typecheck: typecheck the dashboard
web-typecheck:
	cd web && bun run typecheck

## api-gen: regenerate Go route table + TS client from docs/api/openapi.yaml
api-gen:
	python3 scripts/gen_api_routes.py
	python3 scripts/gen_api_client.py

## api-check: verify generated artifacts are not stale (CI)
api-check:
	python3 scripts/gen_api_routes.py --check
	python3 scripts/gen_api_client.py --check

## launcher-test: run the npx launcher package test suite (node:test)
launcher-test:
	cd launcher && npm test

## clean: remove build artifacts
clean:
	rm -rf bin web/build web/.react-router
