# Phase 7 Gate — Compatibility & Migration

**Verdict: PASS** — all four gate criteria verified with linked evidence.
Phase 7 closes 8/8 nodes; the graph moves to Phase 8 (Hardening).

## Gate criteria

### 1. Parity checklist: 100% of blocker rows green, cosmetic rows triaged

**PASS.** `docs/compat/checklist.md` final state: 79 rows — 73 parity,
4 partial (all cosmetic, each with a divergence note + migration or
tracking reference), 1 n/a (management API is additive surface), **0
blocker gaps**. Every row carries legacy behavior, status, and an
evidence link (unit test, adapter contract, corpus case, or live CLI
run).

### 2. Traffic replay diff harness clean on captured corpus

**PASS.** `python3 scripts/parity/replay.py` → **41/41 cases pass, 0
blocker diffs, 0 cosmetic diffs** (`docs/reports/parity-replay.md`,
gate-time run 2026-10-09). The corpus covers the top-20 error contract
(C-1…C-23), every stream event order (D-1…D-10), every cross-protocol
pairing (G-1…G-7), quota/fallback/circuit/disconnect semantics, and the
`/v1/models` listing. Baseline at harness bring-up was 9/41 with 32
blocker diffs — every diff mapped to a catalogued checklist gap and
closed by p7.parity-fixes (the largest being the unwired data plane:
production routing now flows through the Phase 4 engine).

### 3. `onegate import` migrates a reference OmniRoute install (dry-run + apply)

**PASS.** Live evidence with a synthesized reference install
(2 providers, 1 model, 1 rule, 2 keys, 50 usage rows):

- `onegate import` — dry-run default, per-entity diff report, apply,
  **re-import → 0 changes** (idempotent), legacy file byte-identical,
  unmapped fields enumerated (`internal/importer/importer_test.go`,
  7 tests).
- `onegate import-keys --usage-db` — keys re-minted (legacy scrypt
  hashes unrecoverable by design) with metadata preserved, raw keys
  emitted exactly once to a 0600 file, revoked keys stay revoked,
  50 usage rows imported with **exact aggregate preservation** (rollup
  summary: 50 requests, 1,275,000 micro-USD — matching the legacy
  totals), skip report for unmappable rows, re-import → all skipped
  (`internal/importer/keys_test.go`, 6 tests).

### 4. Parity report published with per-area sign-offs

**PASS.** `docs/compat/report-phase7.md` — five signed areas (proxy,
routing, keys, config, CLI), a cosmetic divergence register with notes,
and two tracked non-parity limitations (alias-keyed pricing → p8 input;
in-memory session/quota windows by design).

## Verification bundle

| Check | Result |
| --- | --- |
| `go build ./...` | green |
| `go vet ./...` | green |
| `go test ./... -race -count=1` | 23 packages ok |
| `gofmt` on touched files | clean |
| `python3 scripts/graph_status.py --check` | 10 graphs, 86 nodes valid |
| Parity corpus replay | 41/41, 0 blocker, 0 cosmetic |

## Carried observations (into Phase 8+)

1. **Alias-keyed pricing** (from the parity report): spend-cap debits
   price by the canonical model id; alias-only requests against
   unpriced canonicals debit zero. Revenue-accounting gap, not a parity
   break. Input for p8 hardening or p9 user-docs disclosure.
2. **Idle SSE keep-alives** (checklist B-8, cosmetic): pings under 60 s
   idle are not emitted; revisit if p8 load tests surface idle drops
   through proxies.
3. **`onegate import-keys` sequencing**: usage import requires the key
   import to run first (skip report says so explicitly). A combined
   `--all` flow is a p9.cli-polish candidate.
