# OneGate — Agent Guide

Single-binary LLM gateway in Go (rewrite of OmniRoute v3.8.52). Proxies
OpenAI / Anthropic / Gemini behind one endpoint with virtual keys, routing,
fallback, quotas, usage analytics, and an embedded React dashboard.
Module: `github.com/ishwarchandra-dev/onegate`.

## Commands

```bash
# Go (gateway)
make build                  # -> bin/onegate (ldflags inject internal/version)
make run                    # serves on 127.0.0.1:7420, GET /healthz
go vet ./... && go test ./... -race   # required before review/done
make lint                   # golangci-lint v2 (gofmt + goimports)
go test ./internal/protocol/openai/ -run TestName   # single package/test

# Dashboard (web/, uses bun — not npm/yarn)
make web-install            # bun install
make web-dev                # vite :5173, proxies /api -> :7420
cd web && bun run build && bun run typecheck

# Task graph
make graph                                  # board + ready queue
python3 scripts/graph_status.py --check     # integrity check (CI runs this)
```

CI (`.github/workflows/ci.yaml`) runs: `go build/vet/test`, `bun run build`,
and the graph `--check`. Keep all three green.

## How work is organized (graph engineering)

Work is a DAG of nodes in `tasks/phase-N.<name>.graph.yaml`, not a backlog.
Read [docs/graph-engineering.md](docs/graph-engineering.md) before changing
task files.

- Only start a node whose `depends_on` are all `done`. Lifecycle:
  `pending → in-progress → review → done` (or `blocked` with a reason).
- Each node has an `owner` — follow that charter in `.claude/agents/<owner>.md`
  (default for Go code: `go-engineer`; review: `code-reviewer`).
- **Never edit acceptance criteria** of an existing node; supersede it
  (`pN.slug-2`) instead. Node IDs are unique across all phases.
- `done` requires evidence (tests run, benchmarks where budgeted, review).
- Commit style: `Phase N (pN.node-id): short description`.
- Keep diffs scoped to the node's deliverables — no drive-by refactors.

## Architecture & layering (enforced in review)

See [docs/architecture.md](docs/architecture.md) and ADRs in `docs/adr/`.

```
cmd/onegate → internal/server → internal/proxy → internal/routing
                               internal/proxy → internal/protocol → internal/domain
                               internal/proxy → internal/stream
              internal/server → internal/{api,auth,observability,config}
              internal/api → internal/storage → internal/domain
```

- Dependencies point inward; `internal/domain` imports nothing internal.
- **Provider wire types never leave `internal/protocol/`.** Everything crosses
  boundaries as canonical types (`internal/domain/protocol.go`, ADR 004).
  Adapters decode → canonical → encode; no native passthrough.
- Proxy/routing never import `internal/storage`; usage writes go through a
  bounded background queue (drop-and-count), off the hot path.
- Retries/fallback happen **only before the first byte** reaches the client.
- Client disconnect must cancel the upstream request via `context`.
- `internal/server` uses stdlib `http.ServeMux` (Go 1.22 patterns); middleware
  order and timeouts are fixed by ADR 005.
- Config precedence: flags > env > file > defaults. Config file is JSON
  (stdlib, ADR 002). Hot reload keeps last-good config on failure.
- Dashboard only talks to `/api/*`, never to providers.

## Go conventions

- Go 1.24, **stdlib-first**. No new dependencies without an ADR. SQLite is
  `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0` — ADR 003); don't add cgo.
- Package comment on every package; doc comment on every exported symbol.
- Wrap errors with `%w` plus context; sentinel errors live with their domain.
- No `panic` outside main/init; prefer typed values over `any`/`interface{}`.
- Every goroutine selects on `ctx.Done()` (or a stop channel); no leaks.
  Timeouts on every outbound call; no unbounded queues/channels.
- Logging via `log/slog` from `observability.NewLogger`; its handler redacts
  attributes by key name, so use descriptive keys (e.g. `api_key`) and never
  smuggle secrets into messages. Attach trace IDs with
  `observability.WithTrace(ctx, logger)` / `TraceAttr(ctx)`.
- Tests: table-driven, `-race` clean. Protocol adapters use golden fixtures
  in `internal/protocol/<provider>/testdata/` (`.json`, `.sse`) compared via
  `internal/protocol/golden`; round-trips must be stable under JSON-key
  normalization. Cross-protocol checks live in `internal/protocol/conformance`.
- No commented-out code; no TODO without a corresponding graph node.
- Formatting: tabs for `.go`/Makefile, 2 spaces elsewhere, LF (`.editorconfig`).

## Dashboard (web/)

React Router 7 (framework mode) · React 19 · Tailwind CSS v4 · shadcn/ui
(radix) · lucide-react · TypeScript. Use the existing Tailwind + shadcn stack
(`web/components.json`, `web/app/components/ui/`) — don't introduce another
styling system. Format with Prettier (`bun run format`). Embedded into the Go
binary via `go:embed` in Phase 9.

## Gotchas

- Runtime state lives in `.onegate/` and `*.db*` files — gitignored; don't commit.
- `docs/architecture.md`'s ADR table numbering predates the ADR files; treat
  `docs/adr/*.md` as authoritative.
- `docs/compat/` is referenced by the README but arrives in Phase 7.
- Provider-specific behaviour notes: `docs/research/provider-quirks.md`,
  `docs/protocol-mappings.md`.
