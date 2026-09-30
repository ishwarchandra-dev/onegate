# ADR 005: HTTP server architecture — middleware order, timeouts, metrics seam

- Status: Accepted
- Date: 2026-09-30
- Phase: 3 (node `p3.http-server`)
- Deciders: go-engineer, graph-master

## Context

Phase 3 turns OneGate into a reverse proxy. Every proxied request will pass
through the public HTTP surface, so the decisions baked in now — middleware
order, timeout policy, panic semantics, and where metrics live — constrain
the proxy core, the streaming pipeline, and Phase 5 observability.

OmniRoute v3.8.52 ran on Express, where timeout/recovery behavior emerged
from framework defaults. The Go rewrite makes each of those defaults an
explicit, tested contract.

## Decision

### 1. Package shape: `internal/server`, config-decoupled

The package owns the router (`*http.ServeMux`, Go 1.22+ pattern syntax),
the middleware chain, and the listener timeout policy. It imports only the
standard library, `internal/domain` (error envelopes), and
`internal/observability` (trace context) — **never** `internal/config`.
The caller (`cmd/onegate`) resolves config into `server.Options`. This
keeps the phase-2 layering audit clean and the server unit-testable
without touching files or env.

Proxy endpoints (p3.ingest-endpoints) register onto the exposed
`Router.Mux()`; the chain is not per-endpoint.

### 2. Middleware order (fixed, tested contract)

```
RequestID -> AccessLog -> Recover -> handler
```

- **RequestID outermost**: it assigns the trace ID into the request
  context before anything else runs, so every downstream log line —
  access logs *and* panic logs — is grep-able by one ID per request.
- **AccessLog outside Recover**: a recovered panic still produces exactly
  one access-log line carrying the rendered 500, instead of vanishing.
  5xx lines log at ERROR level; everything else at INFO.
- **Recover innermost**: it is the last line of defense directly around
  application code, and it needs the statusWriter that AccessLog created
  to know whether a 500 is still renderable.

The order is verified behaviorally in `middleware` tests: the panic test
asserts a single access line with the echoed trace ID and status 500,
which is only true under this exact composition.

### 3. Request IDs

- Header: `X-Request-Id` (byte-identical to OmniRoute's `x-request-id`).
- Client-supplied IDs are honored iff they match `^[A-Za-z0-9._:-]{1,64}$`
  — this keeps distributed tracing usable while blocking header/response
  splitting and log injection (control chars, whitespace, over-long
  values, non-ASCII are all replaced).
- Generated IDs: `req-` + 32 hex chars of `crypto/rand`. Cryptographic
  randomness (not pseudo) because IDs appear in logs and responses where
  predictability would enable cross-tenant log forgery.
- Every response echoes the effective ID; the value also rides in the
  context for Phase 5's full trace propagation (p5.trace-context).

### 4. Panic semantics

Two cases, both deliberate:

- **Panic before first byte**: render HTTP 500 with the neutral envelope
  `{"error":{...domain.GatewayError}}`, type `internal_error`,
  `retryable=false` (a panic is a OneGate bug, likely deterministic;
  retrying invites identical-failure storms). The panic value and stack
  go to the server log only — never the response (no internals leak).
  The connection stays alive and keeps serving (proven by a raw-TCP
  keep-alive test).
- **Panic mid-stream** (response already committed): a 500 can no longer
  be delivered without corrupting the body. Log, then re-panic so
  net/http terminates the connection — the only honest signal left.
  The streaming pipeline (p3.stream-pipeline) owns graceful mid-stream
  error events; this is the backstop.

### 5. Timeout policy

Config gains an `http` section (ms, env-overridable, file-mergeable):

| Field | Default | Meaning |
|---|---|---|
| `read_header_timeout_ms` | 10000 | slowloris guard; **must** be > 0 (validated) |
| `read_timeout_ms` | 0 | whole-request read cap; 0 = off |
| `write_timeout_ms` | 0 | response write cap; 0 = off |
| `idle_timeout_ms` | 120000 | keep-alive idle cap; 0 = off |

Read/write default **off** because WriteTimeout also bounds streaming
response lifetimes — cutting a long generation mid-flight is worse than
an unbounded write. ReadTimeout, when enabled, must be ≥
ReadHeaderTimeout (validated) since it covers headers too. The values map
1:1 onto `http.Server` fields via `Router.Server(addr)`.

### 6. Metrics seam

`server.Options.Metrics` is an interface (`ObserveRequest(method, code)`,
`ObservePanic()`, `Handler()`). Phase 3 ships `miniRegistry`: bounded
cardinality counters (`onegate_http_requests_total{method,code}`,
`onegate_http_panics_total`) that make `/metrics` real end-to-end. Phase 5
(p5.metrics) injects the full Prometheus registry — TTFT/latency
histograms, per-provider counters, admin gating — as a drop-in
implementation; middleware code does not change.

## Consequences

- The chain order is a public contract: inserting new middleware (e.g.
  auth in p3.ingest-endpoints) must state and test its position.
- Streaming handlers can rely on: trace ID in context, no write deadline
  by default, and a statusWriter that implements `http.Flusher`.
- The 500 envelope shape is shared with domain.GatewayError, so protocol
  adapters render gateway-internal failures consistently later.
- `/metrics` is ungated in Phase 3 (loopback bind by default); admin
  gating arrives with p5.metrics.

## Alternatives considered

- **Per-endpoint middleware**: rejected — inconsistent chains across
  proxy vs system routes, and the order contract becomes untestable.
- **http.TimeoutHandler for per-request deadlines**: rejected for the
  proxy path (it buffers/503s mid-stream); per-request timeouts belong to
  the fallback engine (p3.fallback-chain) via context deadlines.
- **chi/echo router**: rejected — ServeMux pattern syntax covers the
  routing table (fixed paths + method matching), and the project stays
  zero-dependency for the request path.
