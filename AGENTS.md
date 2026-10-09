# Repository Guidelines

Guide for AI assistants working in this repo. OneGate is a single-binary LLM
gateway in Go (module `github.com/ishwarchandra-dev/onegate`, a rewrite of
OmniRoute v3.8.52) that proxies OpenAI / Anthropic / Gemini behind one
endpoint with virtual keys, routing, fallback, quotas, usage analytics, and
an embedded React dashboard (`web/`). Work is organized as a DAG of nodes in
`tasks/phase-N.<name>.graph.yaml`, not a backlog.

## Project Overview

- **What it is**: reverse proxy + gateway for LLM provider APIs. Clients call
  OpenAI / Anthropic / Gemini-native endpoints (`/v1/chat/completions`,
  `/v1/messages`, `/v1beta/models/{model}:generateContent`) with a OneGate
  virtual key (`ogk-` prefix); the gateway authenticates, routes to a
  provider target, translates protocols, streams SSE, and records usage.
- **Composition root**: `cmd/onegate/main.go` wires config → logger →
  storage → auth verifier → usage pipeline → metrics → fallback engine →
  server; it is the only place allowed to import everything. Default
  listener `127.0.0.1:7420` (health at `GET /healthz`).
- **Dashboard**: `web/` — React Router 7 (framework mode), served by Vite in
  dev; embedding into the binary via `go:embed` is planned for Phase 9 and
  is **not yet wired** (no `go:embed` in any `.go` file today; the server
  serves a placeholder at `/`).
- **Sole external Go dependency**: `modernc.org/sqlite` v1.34.x (pure Go,
  CGO_ENABLED=0). No new dependencies without an ADR. `go.uber.org/goleak`
  is used by tests only.

## Architecture & Data Flow

Layered, dependencies point inward. Layering is **enforced** by tests, not
just review (`internal/routing/layering_test.go` AST-parses imports of every
production file; the protocol package has its own TestLayering).

```
cmd/onegate → internal/server → internal/proxy → internal/routing
                               internal/proxy → internal/protocol → internal/domain
                               internal/proxy → internal/stream
              internal/server → internal/{observability,version}
              internal/api → internal/{storage,auth,observability,routing,domain,proxy/client}
              internal/auth → internal/storage → internal/domain
```

The management plane (`internal/api`, Phase 6) is a sibling of
`internal/proxy/ingest`: registered on the same mux by the composition
root, it owns `/api/*` (dashboard control plane). It may import storage,
auth, observability, routing, and `internal/proxy/client` (SSRF-guarded
probes) but never `internal/server` or `internal/config` (callers pass
resolved values — `api.SystemConfig` — through `api.Options`).

Hard rules (from `docs/architecture.md` + ADRs in `docs/adr/`):

- `internal/domain` imports nothing internal (stdlib only). All canonical
  types live in `internal/domain/{domain,protocol}.go`: `Request`,
  `Response`, `StreamEvent`, `GatewayError`, `Message`/`ContentBlock`, plus
  sentinel errors (`ErrNoCredential`, `ErrKeyRevoked`, …) and domain types
  (`VirtualKey`, `RoutingRule`, `Usage`). Unix-ms timestamps, integer
  micro-USD money — floats are forbidden in usage/pricing paths.
- **Provider wire types never leave `internal/protocol/`** (ADR 004).
  Everything crossing a boundary is a canonical type. Adapters
  (`internal/protocol/{openai,anthropic,gemini}`) are pure functions:
  `DecodeRequest`/`EncodeRequest`, `DecodeResponse`/`EncodeResponse`,
  `DecodeError`/`EncodeError`, `StreamDecoder`/`StreamEncoder`. They may
  import only stdlib, `internal/domain`, and sibling protocol packages.
- `internal/routing` is pure decision code: may import **only** stdlib +
  `internal/domain` (AST-enforced). It reaches persistence/health/latency
  through interfaces (`StorageSource`, `HealthView`, `LatencyView`);
  `internal/storage/routing_source.go` adapts the store. Registry is an
  immutable `Snapshot` swapped atomically by a storage watcher.
- **Proxy/routing never import `internal/storage`.** Usage writes flow
  through `observability.UsagePipeline`: bounded channel (10k), background
  batch writer (batch 100, flush 100ms), strictly non-blocking
  drop-and-count on overflow — never on the hot path.
- **Retries/fallback happen only before the first byte** reaches the
  client. Enforced by `stream.Writer.Written()` (streaming) and the
  buffered path's `httptest.ResponseRecorder`-then-commit in
  `internal/proxy/fallback/engine.go`. Once bytes are written, fallback is
  forbidden and mid-stream errors become client-native SSE error frames.
- Client disconnect cancels the upstream request via `context`
  (`context.Canceled` → 499, non-retryable, in `client.classifyTransportError`).
- `internal/server` uses stdlib `http.ServeMux` (Go 1.22 patterns).
  Middleware order is fixed by ADR 005: **RequestID → AccessLog → Recover**
  (outermost first). Read/Write timeouts default OFF so streams are never
  cut; ReadHeader must stay >0 (slowloris guard); Idle 120s.
- Config precedence: **flags > env > file > defaults** (ADR 002). Config
  file is JSON (`onegate.json`, partial-file overlay; discovery: explicit
  `-config` path > `$ONEGATE_CONFIG` > `./onegate.json` >
  `~/.onegate/onegate.json`). Hot reload (SIGHUP + mtime polling) keeps
  last-good config on failure and swaps atomically.
- Dashboard only talks to `/api/*`, never to providers.

Request flow (canonical → provider → canonical):

1. Middleware: request-ID (`X-Request-Id` honored if 1–64 chars
   `[A-Za-z0-9._:-]`, else `req-`+32 hex), access log, panic recover.
2. `internal/proxy/ingest` selects client protocol by route; extracts
   credentials per protocol (Bearer / `x-api-key` / `x-goog-api-key` /
   `?key=`); body capped 32 MiB (413); `DecodeRequest` → `domain.Request`.
3. Auth (`internal/auth/verify.go`): peppered HMAC-SHA256 key hash lookup;
   sentinel errors map to 401/403, rendered in the *calling* protocol's
   envelope via `ingest/render.go`.
4. `fallback.Engine.Execute` resolves targets and loops attempts; each
   attempt: `EncodeRequest(providerProto)` → `client.Do` (SSRF-guarded) →
   `DecodeResponse`/`DecodeError` → `EncodeResponse(clientProto)`. Streaming
   attempts gate retry on `Written()`.
5. `internal/stream/pipeline.go` pipes upstream SSE frames → provider
   `StreamDecoder` → `domain.StreamEvent` → client `StreamEncoder` with
   per-frame flush; TTFT captured; `[DONE]` appended for OpenAI clients.
6. Deferred usage event → `OnUsage` → `UsagePipeline.Enqueue` +
   `metrics.ObserveProxyRequest` (+ quota debit). Routing decisions come
   from `internal/routing/policy.go` (override > key overrides > key
   policy > rule > ordered) with circuit-breaker health tracking.

## Key Directories

| Path | Purpose |
| --- | --- |
| `cmd/onegate/` | Main entry point; the only composition root |
| `cmd/mockprovider/` | Standalone mock upstream provider binary (`--host`/`--port`), speaks all 3 wire protocols |
| `internal/domain/` | Canonical types + sentinel errors; imports nothing internal |
| `internal/protocol/` | OpenAI/Anthropic/Gemini adapters, `sse`, `golden`, `conformance`; wire types confined here |
| `internal/server/` | Go 1.22 `ServeMux`, middleware, request-ID/access-log/recover, `healthz`/`metrics` |
| `internal/api/` | Management API (`/api/*`): spec-generated route table, admin-token auth, rate-limit classes, provider/model/key/usage/log handlers |
| `internal/proxy/` | `ingest` (client-facing endpoints), `fallback` (engine), `nonstream` (buffered executor), `client` (upstream HTTP + SSRF guard) |
| `internal/routing/` | Pure routing core: registry, policy, health circuit breakers, storage watcher |
| `internal/stream/` | SSE pipeline: reader, flush-tracking writer, per-protocol stream adapters |
| `internal/auth/` | Virtual-key verify/manage, peppered key hashing, provider key encryption |
| `internal/ratelimit/` | RPM/TPM token buckets, spend caps, retry-after parsing/backoff, micro-USD price table |
| `internal/observability/` | slog logger with redaction + trace IDs, usage pipeline, metrics registry, live-log hub |
| `internal/storage/` | SQLite (WAL, MaxOpenConns(1)), embedded migrations, repos, hourly rollups |
| `internal/config/` | JSON config load/merge/validate + hot-reload watcher |
| `internal/mockprovider/` | In-process mock provider used by e2e/integration tests |
| `web/` | Dashboard: React Router 7 + React 19 + Tailwind v4 + shadcn/ui, bun-managed |
| `tasks/` | `phase-N.<name>.graph.yaml` task-graph DAGs (phases 0–9) |
| `scripts/` | `graph_status.py` — board printer + `--check` validator (runs in CI) |
| `docs/` | `architecture.md`, `graph-engineering.md`, `protocol-mappings.md`, `research/provider-quirks.md`, `adr/`, `gates/`, `reports/` |

## Development Commands

```bash
# Go (gateway)
make build                  # -> bin/onegate (ldflags inject internal/version Version/GitCommit/BuildDate)
make run                    # serve on 127.0.0.1:7420; GET /healthz
go vet ./... && go test ./... -race   # REQUIRED gate before review/done
make test                   # go test ./... (no -race; use the gate above)
make lint                   # golangci-lint v2 (gofmt + goimports formatters)
make fmt                    # gofmt -s -w .
go test ./internal/protocol/openai/ -run TestName   # single package/test

# Dashboard (web/ — bun, never npm/yarn)
make web-install            # bun install
make web-dev                # vite :5173, proxies /api -> 127.0.0.1:7420
cd web && bun run build && bun run typecheck   # required dashboard gate

# Task graph
make graph                  # board + ready queue
python3 scripts/graph_status.py --check        # integrity check; CI runs this

# Management API contract (docs/api/openapi.yaml is the source of truth)
make api-gen                # regenerate internal/api/spec_gen.go + web client
make api-check              # CI: fail when generated artifacts are stale
```

CI (`.github/workflows/ci.yaml`, push to main + all PRs) runs three parallel
jobs — **go** (`go build/vet/test`), **web** (`bun install` + `bun run
build`), **graph** (`graph_status.py --check`). Keep all three green. `-race`
and `make lint` are local gates (AGENTS.md, phase-gate docs), not CI jobs.

## Code Conventions & Common Patterns

- **Go 1.24, stdlib-first.** No chi/gin/echo; router is `net/http`. No new
  deps without an ADR; no cgo (`CGO_ENABLED=0`).
- Package comment on every package; doc comment on every exported symbol.
- Errors: wrap with `fmt.Errorf("...: %w", err)`; sentinels live with their
  domain (`domain.ErrUnknownKey`, `routing.ErrModelNotFound`,
  `storage.ErrNotFound`). Cross-layer typed errors (`client.Error`,
  `nonstream.Error`) carry `domain.GatewayError`, extracted via `errors.As`.
  Map sentinels by identity (`switch err { case domain.ErrNoCredential: }`).
- Concurrency: every goroutine selects on `ctx.Done()` or a stop channel
  closed once via `sync.Once`; background workers drain on Stop. Bounded
  queues everywhere; overflow is drop-and-count (atomic counters), never
  blocking. Fire-and-forget hot-path goroutines wrap in `defer recover()`.
  Timeouts on every outbound call. No `panic` outside main/init.
- Logging: `log/slog` via `observability.NewLogger*` only — its handler
  redacts sensitive keys (`api_key`, `token`, `secret`, …) and injects
  `trace_id` from ctx. Use descriptive keys and
  `observability.TraceAttr(ctx)`; never put secrets in messages.
- Dependency injection via interfaces at layer seams (`Authenticator`,
  `Proxy`, `TargetResolver`, `StorageSource`, `BatchWriter`); composition
  happens only in `cmd/onegate`. `internal/server` never imports config —
  callers resolve plain `Options` structs.
- Tests use injectable clocks (`nowMS`, `SetClock`, `UsagePipeline.Flush()`)
  for determinism; no wall-clock sleeps in assertions.
- Dashboard: use the existing Tailwind + shadcn stack (`web/components.json`,
  `web/app/components/ui/`); add components via
  `bunx --bun shadcn@latest add <component>`; format with Prettier
  (`bun run format` — no semicolons, double quotes, 2 spaces, width 80).
  Strict TypeScript, path alias `~/*` → `./app/*`.
- Formatting: tabs for `.go`/Makefile, 2 spaces elsewhere, LF
  (`.editorconfig`). No commented-out code; no TODO without a graph node.
- IDs: trace `req-…`, virtual keys `ogk-…`, storage `prov_…`.

### Task-graph process (read `docs/graph-engineering.md` first)

- Work items are nodes with `id` (`p<phase>.<slug>`, unique across all
  phases), `owner`, `status`, `depends_on`, `deliverables`, `acceptance`.
  Lifecycle: `pending → in-progress → review → done` (or `blocked` with a
  reason). Only start a node whose `depends_on` are all `done`.
- **Never edit acceptance criteria** of an existing node — supersede it
  (`pN.slug-2`). Every phase closes via its `p{N}.gate` node. `done`
  requires linked evidence (tests run, benchmarks, review).
- Owners follow the charter in `.claude/agents/<owner>.md` (default for Go
  code: `go-engineer`; review: `code-reviewer`; graph files: `graph-master`).
- Commits: `Phase N (pN.node-id): short description`.
- Keep diffs scoped to the node's deliverables — no drive-by refactors.

## Important Files

| File | Role |
| --- | --- |
| `cmd/onegate/main.go` | Entry point; all wiring. Flags `-host -port -data-dir -log-level -config -version` |
| `internal/domain/{domain,protocol}.go` | Canonical model; the contract everything else speaks |
| `internal/protocol/protocol.go` | Adapter layering contract (what adapters may import) |
| `internal/proxy/ingest/ingest.go` | Client-facing endpoints; auth + body caps + decode |
| `internal/proxy/fallback/engine.go` | Retry/fallback engine; the first-byte gate |
| `internal/proxy/client/{client,ssrf}.go` | Upstream HTTP (pooled transport, mandatory SSRF guard) |
| `internal/stream/pipeline.go` | SSE translate pipeline; TTFT; ctx-aware |
| `internal/routing/{registry,policy,health}.go` | Routing core + circuit breakers |
| `internal/observability/{logging,usage}.go` | Redacted slog + trace IDs; usage queue |
| `internal/config/{config,reload}.go` | JSON config, precedence, hot reload |
| `internal/storage/{storage,repos}.go` | SQLite open (WAL) + repositories |
| `Makefile`, `go.mod`, `.golangci.yml`, `.editorconfig` | Build + tooling config |
| `web/vite.config.ts`, `web/package.json`, `web/components.json` | Dashboard toolchain |
| `tasks/*.graph.yaml`, `scripts/graph_status.py` | Task graph + validator |
| `docs/adr/*.md` | ADRs 002–005 (config JSON, SQLite driver, canonical schema, HTTP server) |

## Runtime / Tooling Preferences

- **Go 1.24** (`go.mod` + `go.work`); **CGO_ENABLED=0**; SQLite via
  `modernc.org/sqlite` (ADR 003). Runtime state lives in `.onegate/`
  (default data dir): `onegate.db` + `master.key` — gitignored, never commit.
- **Dashboard uses bun**, not npm/yarn (lockfile `web/bun.lock`). `web/Dockerfile`
  is stale (npm-based, references a nonexistent package-lock.json) — ignore it.
- Admin/metrics gating via `ONEGATE_ADMIN_TOKEN` env var; other env vars are
  `ONEGATE_{CONFIG,HOST,PORT,DATA_DIR,LOG_LEVEL,HTTP_*_TIMEOUT_MS}`.
- `make lint` requires a locally installed golangci-lint v2 (not in CI).

## Testing & QA

- **Go stdlib `testing` only** — no testify/gomock. Table-driven tests with
  `t.Run(tc.name, ...)`; **no `t.Parallel()`** anywhere. `httptest` for all
  HTTP seams; hand-rolled fakes (`fakeProxy`, `captured`, `StaticResolver`).
- **Golden fixtures** (`internal/protocol/golden/golden.go`): adapters must
  round-trip byte-stable under JSON-key normalization — `Decode(fixture)` →
  `Encode` → `golden.Diff(fixture, re) == ""`. Fixtures are
  `internal/protocol/<provider>/testdata/*.json` (requests/responses/errors)
  and `*.sse` (streams). `TestDecodeTolerantForms` covers wire variants where
  byte-stability is explicitly not required.
- **Cross-protocol conformance** (`internal/protocol/conformance/`): one
  canonical fixture set through every adapter and every adapter pair, plus
  `TestLossyTableDocumented` (lossy table in `lossy.go` must match
  `docs/protocol-mappings.md`) and `TestQuirkDocAnchors` (quirk anchors must
  match `docs/research/provider-quirks.md`).
- **Leak checks**: `defer goleak.VerifyNone(t, goleak.IgnoreCurrent())` in
  e2e (`internal/proxy/e2e_test.go`) and cancellation tests. Harnesses:
  `setupE2E(t)` (full stack with real temp-dir SQLite + `mockprovider`),
  `newStack(t)` (ingest-level), `openMigrated(t)` (storage).
- `-race` is a hard gate (`go test ./... -race`); races and flaky tests are
  always blockers. Phase gates use `go test -count=1 ./... -race`.
- Benchmarks with `b.ReportAllocs()` guard hot-path budgets (TTFT overhead
  <10ms, zero-alloc usage enqueue): `internal/observability/overhead_bench_test.go`,
  `internal/stream/ttft_bench_test.go`. Perf work: 3 runs, >10% regression
  blocks done.
- Coverage expectations live in the qa-engineer charter (≥90% domain/protocol,
  ≥80% overall); no CI coverage gate or web test runner exists — the web gate
  is `bun run build && bun run typecheck` only.

## Gotchas

- `docs/architecture.md`'s ADR table numbering predates the ADR files; treat
  `docs/adr/*.md` (002–005) as authoritative.
- `docs/compat/` is referenced by the README but arrives in Phase 7.
- Provider-specific behavior notes: `docs/research/provider-quirks.md`,
  `docs/protocol-mappings.md`.
- The management API contract is `docs/api/openapi.yaml`; route table
  (`internal/api/spec_gen.go`) and web client (`web/app/lib/api/`) are
  GENERATED from it — run `make api-gen` after spec changes, never edit
  by hand (CI's `make api-check` fails on drift).
- Dashboard embedding (`go:embed web/build`) is Phase 9 work — do not wire
  it early.
