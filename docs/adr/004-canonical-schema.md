# ADR 004: Canonical protocol schema; adapters translate at the edge

- Status: Accepted (Phase 2, node p2.canonical-schema)
- Date: 2026-09-30

## Context

OneGate accepts client requests in three wire protocols (OpenAI Chat
Completions, Anthropic Messages, Google Gemini generateContent) and can
serve them from providers speaking any of the three. Two architectures
were on the table:

1. **Native passthrough** — route the request body as-is whenever client
   and provider protocols match; translate only on protocol mismatch.
2. **Canonical at the edge** — every request is decoded into an internal
   canonical schema (internal/domain/protocol.go) at ingest; every
   provider request is encoded from canonical; every provider response or
   stream is decoded to canonical and re-encoded into the client's
   protocol.

OmniRoute v3.8.52 grew both approaches side by side (the audit found a
dead parallel "domain layer" coexisting with passthrough paths), which
doubled the translation matrix and made parity untestable.

## Decision

**Canonical at the edge, always.** Every byte crossing the proxy boundary
is parsed into domain types; nothing is forwarded opaque. Pure functions
per protocol adapter:

```
client wire ──Decode──▶ domain.Request ──Encode──▶ provider wire
provider wire ──Decode──▶ domain.Response/StreamEvent ──Encode──▶ client wire
```

This yields 3+3 adapters instead of a 3×3 (soon n×n) translation matrix,
one place to enforce validation, and one schema for routing, quotas, and
usage metering to read.

## Consequences

- **Strictness over fidelity on unknown fields.** Fields a protocol
  supports that canonical does not model are dropped, never forwarded
  blindly. Every drop is enumerated in docs/protocol-mappings.md
  (lossy-mapping tables) so the Phase 7 compatibility audit can price the
  gap against OmniRoute behavior.
- **Byte-stable goldens.** Tool-call arguments, JSON schemas, and any
  verbatim payload are carried as raw JSON strings (json.RawMessage /
  pre-serialized strings), so decode→encode cycles are stable under
  JSON-key normalization without re-marshalling drift.
- **Streaming is event-normalized.** All native streams translate into
  the canonical event taxonomy (message_start / block_start /
  block_delta / block_stop / message_delta / message_stop / ping /
  error), modeled on the most expressive native protocol (Anthropic SSE).
  The client-facing encoder re-renders native frames per protocol.
- **Cross-protocol translation is free** (OpenAI SDK in, Gemini provider
  out) — the primary OmniRoute feature this architecture preserves.

## Deliberate exclusions (with rationale)

| Exclusion | Rationale |
|-----------|-----------|
| OpenAI `n > 1` choices | AI coding tools never use it; multi-choice responses break the single-content-block streaming model. Parsed, ignored after first choice. |
| Audio blocks (input/output) | No adapter demand today; revisit with ADR if a provider adds it. |
| Anthropic 5m/1h cache-TTL split | Collapsed into a single `cache_write_tokens` counter; pricing impact is nil at current price sheets. |
| OpenAI legacy `functions`/`function_call` | Deprecated upstream; canonical carries `tools`/`tool_choice` only. Adapters accept the legacy shape at decode time. |
| Gemini inline request blobs (`request_schema`, etc.) | Superseded by the unified schema surface OneGate targets. |
| Logprob payloads in responses | Canonical carries the *request* flags (logprobs/top_logprobs) for pass-through; response bodies drop the payloads. Tabulated in the mapping tables. |

## Alternatives considered

- **Native passthrough with on-demand translation** (OmniRoute's de facto
  mode): fewer translations when protocols match, but every feature
  (fallback, quota metering, request rewriting) then needs per-protocol
  code paths, and cross-protocol requests still pay the matrix cost.
- **JSON-tree rewriting (jq-style)**: no schema to maintain, but
  validation, usage extraction, and golden testing all become
  stringly-typed guesswork.
