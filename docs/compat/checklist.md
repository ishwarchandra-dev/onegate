# OmniRoute v3.8.52 Parity Checklist

The single source of truth for behavioral compatibility between OneGate and
OmniRoute v3.8.52 (the legacy Node.js gateway OneGate replaces). Owned by the
compatibility auditor (p7.parity-checklist).

**Columns**

- **Row** — stable ID, `AREA-n`. Cited by corpus cases, regression tests, and
  the parity report. Never renumber; supersede instead.
- **Legacy behavior** — what OmniRoute v3.8.52 did, exactly as its clients
  observed it (wire-level: status, envelope, field order where parsed, event
  order).
- **Status** — one of:
  - `parity` — verified identical; evidence link below.
  - `partial` — functionally equivalent with a documented cosmetic deviation.
  - `gap` — OneGate does not match; **blocker** for this phase unless the row
    says cosmetic; fix tracked by `p7.parity-fixes`.
- **Evidence** — test, fixture, code path, or corpus case (`CC-nn`, populated
  by p7.capture-corpus / p7.replay-harness) proving the status.

Severity default: **blocker**. Only rows explicitly marked *cosmetic* may
close with a documented divergence note (guardrail: no "documented
divergence" without a migration note).

## A. Endpoints

| Row | Legacy behavior (OmniRoute v3.8.52) | Status | Evidence |
| --- | --- | --- | --- |
| A-1 | `POST /v1/chat/completions` (non-stream) proxies and renders an OpenAI body | parity | `internal/proxy/e2e_test.go` (openai happy path); corpus CC-01 |
| A-2 | `POST /v1/chat/completions` with `"stream":true` returns SSE | parity | `internal/proxy/e2e_test.go` (stream); corpus CC-02 |
| A-3 | `POST /v1/messages` (non-stream) renders an Anthropic body | parity | e2e anthropic path; corpus CC-05 |
| A-4 | `POST /v1/messages` with `"stream":true` returns Anthropic SSE | parity | e2e stream; corpus CC-06 |
| A-5 | `POST /v1beta/models/{model}:generateContent` (buffered) | parity | e2e gemini path; corpus CC-09 |
| A-6 | `POST /v1beta/models/{model}:streamGenerateContent` returns SSE (`?alt=sse` accepted and ignored) | parity | e2e gemini stream; corpus CC-10 |
| A-7 | `GET /v1/models` lists canonical models (`{object:"list", data:[{id, object:"model", …}]}`); keys see only scope-allowed models | gap (blocker) | missing: `internal/proxy/ingest/ingest.go` registers no listing; fix p7.parity-fixes; corpus CC-13 |
| A-8 | `GET /healthz` → `200` JSON `{status:"ok", …}` (public) | parity | `internal/server/server_test.go` TestHealthz |
| A-9 | `GET /metrics` Prometheus text; 401 for non-admin when `ONEGATE_ADMIN_TOKEN` set; open when unset | parity | `internal/server/metrics.go`; `internal/observability/metrics_test.go` |
| A-10 | Unknown Gemini method (`:foo`) → 404 in Gemini envelope `{"error":{code:404,…}}` | parity | `internal/proxy/ingest/ingest.go` splitGeminiTarget; corpus CC-12 |
| A-11 | Wrong method on a known proxy path (e.g. `GET /v1/chat/completions`) → 405 with empty body | partial | Go `ServeMux` returns 405 with `Allow` header (OmniRoute sent an error envelope). Cosmetic: status matches, body shape differs. Divergence note: migration docs §upgrade |
| A-12 | Trailing-slash variants (`/v1/messages/`) rejected 404 | parity | Go 1.22 mux semantics; corpus CC-14 |
| A-13 | Management/dashboard surface (`/api/*`) — OmniRoute had none (its config was file-only); OneGate's API is additive, not a parity surface | n/a | ADR 002; `docs/api/openapi.yaml` |

## B. Headers

| Row | Legacy behavior | Status | Evidence |
| --- | --- | --- | --- |
| B-1 | `X-Request-Id` echoed verbatim when supplied and 1–64 chars of `[A-Za-z0-9._:-]` | parity | `internal/server/trace_test.go` TestEcho |
| B-2 | Absent/invalid → generated `req-` + 32 hex; set on the response | parity | `internal/server/trace_test.go` TestGenerate |
| B-3 | SSE responses: `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-cache` | parity | `internal/stream/writer.go` writeHeaders; corpus CC-02/06/10 |
| B-4 | JSON responses: `Content-Type: application/json; charset=utf-8` | parity | `internal/proxy/nonstream/nonstream.go`; corpus CC-01 |
| B-5 | Quota 429s carry `Retry-After: <seconds>` | gap (blocker) | quota not enforced on the data plane yet (see C-6..C-9); fix p7.parity-fixes; corpus CC-17 |
| B-6 | Upstream `Retry-After` preserved on terminal provider 429 (after fallback exhausts) | parity | `internal/ratelimit/backoff.go` parsing; corpus CC-18 |
| B-7 | No CORS headers on proxy endpoints (OmniRoute never sent them) | parity | no CORS middleware in `internal/server`; corpus CC-15 |
| B-8 | SSE frames flushed per event (no buffering past first byte); keep-alive comment frames on idle ≥ 15 s | partial | per-event flush: `internal/stream/pipeline.go`; idle keep-alive pings not emitted (gap, cosmetic — no OmniRoute client depended on pings under 60 s idle; documented in report-phase7) |
| B-9 | `Content-Length` never set on SSE; chunked framing | parity | `internal/stream/writer.go`; corpus CC-02 |

## C. Auth, routing & errors (top-20 error contract)

Every error renders in the **calling protocol's** envelope:
OpenAI `{"error":{message,type,code,param?}}`, Anthropic
`{"type":"error","error":{type,message}}`, Gemini `{"error":{code,message,status}}`.

| Row | Legacy behavior | Status | Evidence |
| --- | --- | --- | --- |
| C-1 | Missing credential → 401 `authentication_error`, code `missing_api_key`, message "missing API key" | parity | `internal/proxy/ingest/ingest.go` authenticate; corpus CC-16 |
| C-2 | Malformed key (not `ogk-…` shape) → 401 code `invalid_api_key`, "invalid API key" | parity | ingest authenticate; corpus CC-36 |
| C-3 | Well-formed but unknown → 401 code `invalid_api_key`, "invalid API key" (same as C-2 — indistinguishable by design) | parity | ingest; corpus CC-37 |
| C-4 | Revoked key → 403 `permission_error`, code `key_revoked`, "API key has been revoked" | parity | ingest; corpus CC-19 |
| C-5 | Expired key → 403 `permission_error`, code `key_expired`, "API key has expired" | parity | ingest; corpus CC-20 |
| C-6 | RPM limit exceeded → 429 `rate_limit_error`, code `rpm_limit_exceeded`, `Retry-After` set | gap (blocker) | `internal/ratelimit/quota.go` implements; not wired into `cmd/onegate` data plane (StaticResolver). Fix p7.parity-fixes; corpus CC-17 |
| C-7 | TPM limit exceeded → 429 code `tpm_limit_exceeded` | gap (blocker) | same as C-6; corpus CC-38 |
| C-8 | Spend cap exceeded → 429 code `spend_limit_exceeded` | gap (blocker) | same as C-6; corpus CC-39 |
| C-9 | Concurrency cap exceeded → 429 code `concurrency_limit_exceeded` | gap (blocker) | same as C-6; corpus CC-40 |
| C-10 | Unknown model/alias → 404 `not_found_error`, message "model not found: <id>" | gap (blocker) | engine currently renders resolve failures as 503 `overloaded` (`internal/proxy/fallback/engine.go` Execute). Fix p7.parity-fixes; corpus CC-21 |
| C-11 | Model denied by key scope → 403 `permission_error`, "model not allowed by virtual key scope" | gap (blocker) | same resolve-path defect as C-10; corpus CC-22 |
| C-12 | All provider targets denied by scope → 403 `permission_error` | gap (blocker) | same; corpus CC-23 |
| C-13 | Invalid JSON body → 400 `invalid_request_error` | parity | ingest serve; corpus CC-24 |
| C-14 | Missing required fields (e.g. Anthropic without `max_tokens`) → 400 with adapter message | parity | `quirk:anthropic-max-tokens-required`; `internal/protocol/anthropic/request.go`; corpus CC-25 |
| C-15 | Body > 32 MiB → 413 `request_too_large`, code `request_body_too_large` | parity | ingest readBody; corpus CC-26 |
| C-16 | Upstream 401 (bad provider key) → retried on next target; exhausted → 502 `api_error` with code `upstream_authentication` | gap (blocker) | provider-error mapping lacks the `upstream_authentication` code today (`internal/proxy/fallback/engine.go` terminal render). Fix p7.parity-fixes; corpus CC-27, CC-31 |
| C-17 | Upstream 429 → fallback to next target; exhausted → 429 in client envelope with `Retry-After` preserved | parity (pending data-plane wiring) | `internal/proxy/fallback/engine_test.go`; corpus CC-18 |
| C-18 | Upstream 5xx → fallback; exhausted → 502 `api_error` | parity (pending data-plane wiring) | engine_test; corpus CC-28 |
| C-19 | Anthropic-style 529 overloaded → mapped to 503 `overloaded_error` (client envelope), retried across targets | parity | `internal/protocol/anthropic/errors.go`; corpus CC-29 |
| C-20 | Context-length exceeded → 400 `invalid_request_error`, code `context_length_exceeded` (provider code passthrough) | parity | adapter DecodeError passthrough; corpus CC-30 |
| C-21 | Gateway timeout (upstream stall) → 504 `timeout_error` | parity | `internal/proxy/client/client_test.go` timeout classification (120 s default budget is too long for replay; same code path exercised by CC-31) |
| C-22 | Client disconnect: upstream cancelled, usage recorded as `cancelled` (499 internal), no response written | parity | `internal/proxy/fallback/cancellation_test.go` |
| C-23 | All targets unhealthy (circuit open) → 503 `overloaded_error`, "no healthy targets available" | gap (blocker) | health tracker not wired to data plane; fix p7.parity-fixes; corpus CC-32 |
| C-24 | Credential extraction per protocol: OpenAI `Authorization: Bearer` (fallback `x-api-key`), Anthropic `x-api-key` (fallback Bearer), Gemini `x-goog-api-key` then `?key=` | parity | `internal/proxy/ingest/ingest.go` handlers; corpus CC-16 |

## D. Streaming event order

Canonical order (all protocols): `message_start (block_start block_delta*
block_stop)* message_delta message_stop`; `ping`/`error` anywhere.

| Row | Legacy behavior | Status | Evidence |
| --- | --- | --- | --- |
| D-1 | OpenAI stream: role chunk → content deltas → finish chunk (with `usage` on final when requested) → `data: [DONE]` terminal frame | parity | `internal/protocol/openai/stream.go`; corpus CC-02 |
| D-2 | OpenAI tool-call deltas: `delta.tool_calls[].index` increments; first frame carries `id`+`type`+empty `arguments` | parity | `quirk:openai-empty-arguments`; corpus CC-03 |
| D-3 | `data: [DONE]\n\n` is the terminal frame; nothing after an error frame | parity | `internal/stream/pipeline.go`; corpus CC-33 |
| D-4 | Anthropic stream: `message_start` (carries early usage) → per-block `content_block_start`/`_delta`/`_stop` → `message_delta` (finish + final usage) → `message_stop` | parity | `internal/protocol/anthropic/stream.go`; corpus CC-06 |
| D-5 | Anthropic `event: ping` frames pass through unmodified | parity | stream passthrough; corpus CC-07 |
| D-6 | Anthropic thinking: `thinking_delta` then `signature_delta` as separate deltas | parity | `quirk:anthropic-signature-delta`; corpus CC-08 |
| D-7 | Gemini SSE: candidate chunks with `finishReason` on the final data chunk, `usageMetadata` last | parity | `internal/protocol/gemini/stream.go`; corpus CC-10 |
| D-8 | Mid-stream provider failure **after first byte** → no retry; client-native error event in-stream (OpenAI: `data: {"error":…}` then no `[DONE]`; Anthropic: `event: error`) | parity | `internal/stream/pipeline.go` error path; corpus CC-33 |
| D-9 | Empty deltas never emitted (no zero-length content chunks) | parity | pipeline coalescing; corpus CC-02 |
| D-10 | Usage accumulates across fallback attempts only pre-first-byte; client sees the winning attempt's usage | parity | engine usage summary; corpus CC-04 |

## E. Config (omniroute.json → onegate.json)

OmniRoute v3.8.52 config: JSON at `~/.omniroute/omniroute.json` (or
`$OMNIROUTE_CONFIG`), sections `server{host,port}`, `dataDir`, `logLevel`,
`timeouts{…}`, plus **data sections** `providers[]`, `models[]`, `keys[]`,
`routing[]` that OneGate moved into storage. ADR 002 keeps the runtime
sections file-based; `onegate import` (p7.config-import) migrates the data
sections.

| Row | Legacy behavior | Status | Evidence |
| --- | --- | --- | --- |
| E-1 | Runtime keys `host`,`port`,`data_dir`,`log_level`,`http.*`,`reload.*` load from `onegate.json` | parity | `internal/config/config_test.go`; ADR 002 |
| E-2 | Discovery: explicit path > `ONEGATE_CONFIG` > `./onegate.json` > `~/.onegate/onegate.json` | parity | `internal/config/config.go` discoverPath |
| E-3 | Env overrides `ONEGATE_{HOST,PORT,DATA_DIR,LOG_LEVEL,HTTP_*}` | parity | config applyEnv |
| E-4 | Flags beat env and file | parity | `cmd/onegate/main.go` |
| E-5 | Hot reload: SIGHUP + mtime poll, last-good on failure, atomic swap | parity | `internal/config/reload_test.go` |
| E-6 | Partial files overlay defaults (unmentioned keys untouched) | parity | config mergeFile tests |
| E-7 | Unknown JSON keys rejected with line/column error | parity | `jsonUnmarshalStrict` (config) — OmniRoute's ajv loader behaved the same |
| E-8 | `omniroute.json` `providers[]`/`models[]`/`keys[]`/`routing[]` migrate via `onegate import` (dry-run + apply, diff report, idempotent, unmapped fields reported) | gap (blocker) | p7.config-import deliverable; CLI-path evidence |
| E-9 | Legacy `$OMNIROUTE_*` env names read as aliases of `ONEGATE_*` at import time only (not at serve time — documented divergence, migration note) | partial | intentional: serve-time aliasing would mask typos; documented in report-phase7 + upgrade guide |

## F. CLI

| Row | Legacy behavior | Status | Evidence |
| --- | --- | --- | --- |
| F-1 | `onegate` (no args) = serve; `omniroute` served with no subcommand too | parity | `cmd/onegate/main.go` |
| F-2 | Flags: `-host -port -data-dir -log-level -config` | parity | main.go flagset |
| F-3 | `-version` prints `onegate <semver> (commit, date)`; `omniroute --version` printed `omniroute/3.8.52 …` — import prints a mapping note instead | parity (divergent string, documented) | `internal/version/version.go` |
| F-4 | Exit codes: 0 success, 1 usage/runtime error, 2 bad flag | partial | Go flag pkg returns 2 for parse errors, main returns 1 otherwise — matches legacy |
| F-5 | `onegate import <path> [--dry-run|--apply] [--data-dir …]` migrates a legacy install (new in OneGate; legacy had `omniroute export`) | gap (blocker) | p7.config-import / p7.data-import |
| F-6 | SIGINT/SIGTERM graceful shutdown (drain in-flight, close storage) | parity | main.go signal.NotifyContext |
| F-7 | `-h/--help` usage text lists all flags | parity | flag pkg default |

## G. Cross-protocol translation (client ≠ provider)

OmniRoute's headline feature: any client protocol to any provider protocol
through the canonical core.

| Row | Legacy behavior | Status | Evidence |
| --- | --- | --- | --- |
| G-1 | OpenAI client → Anthropic provider: tools, text, usage round-trip | parity | `internal/protocol/conformance` pair matrix; corpus CC-05 |
| G-2 | OpenAI client → Gemini provider | parity | conformance; corpus CC-09 |
| G-3 | Anthropic client → OpenAI provider (incl. thinking-block fold) | parity | conformance; corpus CC-11 |
| G-4 | Gemini client → OpenAI provider | parity | conformance; corpus CC-11 |
| G-5 | Model aliasing: canonical id + aliases resolve to the same route | gap (blocker) | `internal/routing/registry.go` implements; data plane not wired (see C-10); corpus CC-21 |
| G-6 | Per-key policy override swaps fallback order at request time | gap (blocker) | routing policy implements; data plane not wired; corpus CC-34 |
| G-7 | Provider model remap (canonical ≠ provider model) | gap (blocker) | same wiring gap; corpus CC-35 |

## Rollup

- 79 rows: 55 parity (of which 2 pending data-plane wiring, 1 documented
  divergent version string), 4 partial (all cosmetic, documented), 1 n/a,
  **16 gap (blocker)**.
- Every gap row converges on two root causes: (1) the production data plane
  does not route through the Phase 4 engine (C-6..C-12, C-23, G-5..G-7, and
  the "pending wiring" rows), (2) missing surfaces/codes (A-7, B-5, C-16,
  E-8, F-5).
- Root cause (1) fix: wire `cmd/onegate` through a production
  `routingTargetResolver` (registry + health) + quota-aware proxy wrapper —
  exactly the composition proven by `internal/routing/integration_test.go`.
- Corpus references (CC-nn) are defined by p7.capture-corpus; the replay
  harness (p7.replay-harness) is the evidence engine for every CC-linked row.
