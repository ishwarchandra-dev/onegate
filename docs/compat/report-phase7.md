# Parity Report — Phase 7

OneGate vs OmniRoute v3.8.52. Published by the compatibility auditor
(p7.parity-report) as the phase sign-off artifact. Evidence base:

- `docs/compat/checklist.md` — 79 rows, every row carrying legacy
  behavior, status, and an evidence link.
- `test/parity/corpus.json` — 41 replay cases (CC-01…CC-41) covering the
  top-20 error contract, all stream event orders, all cross-protocol
  pairings, quotas, fallback, circuits, and client-disconnect semantics.
- `docs/reports/parity-replay.md` — the latest harness run:
  **41/41 cases pass, 0 blocker diffs, 0 cosmetic diffs** (baseline at
  harness bring-up was 9/41 with 32 blocker diffs, every one mapped to a
  catalogued checklist gap and closed by p7.parity-fixes).
- `go test ./... -race` — 23 packages green (includes the adapter
  contract tests updated to the corrected parity semantics).

## Sign-offs

### Proxy surface (areas A, B, D) — SIGNED

Endpoints, headers, and streaming event orders match the legacy wire
contract byte-for-byte under normalized comparison:

- All six native endpoints (A-1…A-6) plus `GET /v1/models` (A-7),
  healthz, and metrics gating.
- Request-ID echo/generation (B-1/B-2), SSE framing headers (B-3),
  `Retry-After` on quota and terminal-provider 429s (B-5/B-6), no CORS
  (B-7), chunked SSE without Content-Length (B-9).
- Stream orders: OpenAI role→delta→finish→`[DONE]` (D-1), tool-call
  index/argument framing (D-2), error frame terminates without `[DONE]`
  (D-3), Anthropic message_start early usage (D-4), ping passthrough
  (D-5), thinking/signature deltas (D-6), Gemini finishReason+usage
  terminal chunk (D-7), mid-stream failure after first byte renders a
  native error event with no retry and no `[DONE]` (D-8), no empty
  deltas (D-9), per-attempt usage attribution (D-10).

Evidence: CC-01…CC-03, CC-06…CC-15, CC-33.

### Routing & access (area C, G) — SIGNED

The production data plane now routes through the Phase 4 engine
(registry snapshot, key scopes, fallback policies, circuit health,
quota gates) — the single root cause behind 16 catalogued blockers:

- Quotas C-6…C-9 (RPM/TPM/spend/concurrency) render 429 +
  `Retry-After` with per-limit codes before any upstream work.
- Resolve errors render protocol-correct envelopes: 404 unknown model
  (C-10), 403 scope denials (C-11/C-12), 503 no-healthy-targets after
  the circuit opens (C-23).
- Upstream failures: auth → 502 `upstream_authentication` with
  cross-target retry (C-16), 5xx exhaustion → 502 (C-18), 429
  passthrough with preserved Retry-After (C-17), 529 → 503
  `overloaded_error` (C-19), context-length passthrough (C-20),
  invalid upstream body → 502 `upstream_invalid_response`.
- Client disconnect cancels upstream and records `cancelled` usage
  (C-22); gateway timeout classified by the same transport path
  (C-21, unit evidence).
- Cross-protocol: every client×provider pairing (G-1…G-4), aliasing
  (G-5), per-key policy override (G-6), provider model remap with
  canonical echo (G-7).

Evidence: CC-04, CC-11, CC-17…CC-23, CC-27…CC-32, CC-34…CC-41.

### Virtual keys (areas C-1…C-5, migration) — SIGNED

Ingest auth semantics verified (missing/malformed/unknown/revoked/expired
→ 401/401/401/403/403 with the legacy codes and messages, per-protocol
envelopes). Migration: legacy scrypt hashes are unrecoverable by design;
`onegate import-keys` re-mints keys preserving all metadata, issues raw
keys exactly once to a 0600 file, imports revoked keys as revoked, and
keeps legacy→new id mappings for idempotency.

Evidence: CC-16, CC-19, CC-20, CC-36, CC-37; internal/importer tests.

### Config (area E) — SIGNED

Runtime config is OneGate's own surface (ADR 002: same discovery order,
env overrides, precedence, hot reload — E-1…E-7 all parity). Legacy data
sections migrate through `onegate import` (E-8): dry-run by default,
per-entity diff report, idempotent re-import (0 changes), unmapped
fields enumerated, legacy file byte-identical after import.

Evidence: config tests; importer tests; live CLI smoke.

### CLI (area F) — SIGNED

`onegate` serves; `onegate import` / `onegate import-keys` migrate;
`-version`, flags, help, exit codes, and signal handling match the
legacy surface (F-1…F-7).

## Cosmetic divergence register

All four `partial` rows carry their migration notes (guardrail
satisfied — no undocumented divergence):

| Row | Divergence | Note |
| --- | --- | --- |
| A-11 | 405 method-mismatch bodies: Go ServeMux text vs legacy JSON envelope | Status code identical; documented in the upgrade guide (p9.user-docs) |
| B-8 | No idle SSE keep-alive pings < 60 s | No OmniRoute client depended on pings under 60 s idle; revisit as p8 hardening input if load tests surface idle-connection drops |
| F-3 | `-version` prints the OneGate banner, not `omniroute/3.8.52` | Intentional: the binary is not the legacy product; `onegate import` prints the mapping note |
| E-9 | `$OMNIROUTE_*` env names are not aliased at serve time | Aliasing would mask typos in the new namespace; import-time compatibility only |

## Blocker count

**Zero.** 73 parity, 4 partial (cosmetic, noted above), 1 n/a
(management API is additive), 79 rows total. The replay corpus passes
41/41 with zero unexplained diffs; the residual-diff classification
logged zero cosmetic diffs on the final run.

## Known limitations (tracked, non-parity)

1. **Alias-keyed pricing**: spend caps price by the canonical model the
   client requested; requests made through an alias whose canonical id
   is not in the price table debit zero. Client-visible behavior is
   unchanged (parity); this is a revenue-accounting gap. Candidate
   p8 hardening input.
2. **In-memory sessions/quotas**: dashboard sessions and quota windows
   are per-process (by design, p6 gate); a restart clears rate windows.
   Documented in the dashboard docs.
