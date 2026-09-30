# OneGate Architecture

OneGate is a single Go binary containing: an LLM reverse proxy, a routing
engine, embedded SQLite, a management API, and (at release) an embedded
dashboard. This page is the map; ADRs under `docs/adr/` are the decisions.

## Topology

```
                    ┌─────────────────────────────────────────────────┐
                    │                  onegate binary                  │
                    │                                                 │
 clients ──HTTP──▶  │  internal/server                                │
 (OpenAI/Anthropic/ │    ├─ /v1/*, /messages, gemini   ─ internal/proxy│
  Gemini SDKs)      │    ├─ /api/* (management)       ─ internal/api   │
                    │    ├─ /healthz, /metrics                         │
                    │    └─ / (embedded dashboard, Phase 9)            │
                    │                                                 │
                    │  internal/proxy ─▶ internal/routing              │
                    │       │             (registry, fallback,        │
                    │       │              health, quotas)            │
                    │       ▼                                          │
                    │  internal/protocol ──▶ providers (HTTPS)         │
                    │   (openai/anthropic/gemini adapters)             │
                    │       │                                          │
                    │       ▼                                          │
                    │  internal/stream (SSE pipeline)                  │
                    │                                                 │
                    │  internal/storage (SQLite WAL) ◀─ usage events   │
                    │  internal/observability (logs/metrics/traces)    │
                    │  internal/auth (virtual keys, secrets, sessions) │
                    └─────────────────────────────────────────────────┘
```

## Layering rules (enforced in review)

```
cmd/onegate
   └─▶ internal/server ─▶ internal/proxy ─▶ internal/routing
              │                 │
              │                 └─▶ internal/protocol ─▶ internal/domain
              │                 └─▶ internal/stream
              ├─▶ internal/api ─▶ internal/storage ─▶ internal/domain
              ├─▶ internal/auth
              ├─▶ internal/observability
              └─▶ internal/config
```

- **Dependencies point inward.** `internal/domain` imports nothing.
- **Provider types never escape** `internal/protocol/` — everything crosses
  boundaries as canonical domain types.
- **`internal/storage` is called by nobody on the proxy hot path** except the
  background usage writer (Phase 5).
- **The dashboard never talks to providers.** It only knows `/api/*`.

## Request lifecycle (proxy path)

1. `internal/server` accepts, assigns request ID, applies timeouts.
2. `internal/auth` verifies the virtual key (constant-time) and resolves scopes.
3. Endpoint handler detects the wire protocol → `internal/protocol` parses to
   canonical form.
4. `internal/routing` decides the ordered candidate targets (pure function of
   request + registry + health) and enforces local limits (`internal/ratelimit`).
5. `internal/proxy` executes the fallback loop: build provider request
   (protocol adapter), send, translate the stream (`internal/stream`).
   Retries happen **only before any byte reaches the client**.
6. Usage event finalized exactly once (even on cancel) → bounded queue →
   background writer → SQLite; metrics incremented; trace logged.
7. Client disconnect cancels the provider request via context propagation.

## Key decisions (ADR pointers)

| # | Decision | Status |
|---|----------|--------|
| 001 | Single binary, stdlib-first, SQLite embedded | Accepted (Phase 0) |
| 002 | Canonical internal schema; adapters translate at the edge | Accepted (Phase 2) |
| 003 | Retry/fallback only before first byte to client | Accepted (Phase 3) |
| 004 | Routing decision as pure function; hot reload via atomic pointer swap | Accepted (Phase 4) |
| 005 | Usage writes off the hot path (bounded queue, drop-and-count) | Accepted (Phase 5) |
| 006 | Dashboard embedding strategy (SSR vs SPA) | Phase 9 (`p9.embed-decision`) |

## Runtime model

- One process. Goroutine owners: HTTP server, background usage writer,
  registry watcher, health ticker, live-log hub. All context-cancellable,
  all stopped on SIGTERM within 10s (see `cmd/onegate/main.go`).
- SQLite in WAL mode: single writer (usage writer + migrations), pooled
  readers. The proxy path never holds a DB lock.
- Config: flags > env > file > defaults; hot reload on SIGHUP/file watch with
  typed change events; failed reload keeps last-good config.

## Failure posture

- Provider down / 429 / 5xx → fallback chain (pre-first-byte only), health
  cooldowns, circuit-open on consecutive failures.
- Client hangs → request timeout, cancel upstream, finalize usage.
- SQLite disk full → proxy keeps serving; usage writer counts drops loudly.
- Crash → WAL recovery; usage/keys/health converge to explainable state
  (Phase 8 crash matrix).
