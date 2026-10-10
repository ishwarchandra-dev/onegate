# Phase 8 Gate — Performance, Security & Resilience Hardening

**Verdict: PASS** — all four gate criteria verified with linked
evidence. Phase 8 closes 8/8 nodes; the graph moves to Phase 9
(Single Binary, Distribution & v1.0).

## Gate criteria

### 1. Load tests: sustained RPS soak + burst within latency budgets (p99 tracked)

**PASS.** `docs/reports/load-soak.md` — 60 s @ 60 RPS mixed
stream/non-stream: 3600/3600 completed, 0 errors, p50 1.0 ms / p95
1.7 ms / **p99 2.3 ms**, TTFT p99 2.0 ms, and goroutine / OS-thread /
open-FD growth within noise across the window (all seven budgets
PASS); 30 s stability run same picture. `docs/reports/load-burst.md` —
burst-100 / burst-300 walls, stream churn, and slow-provider overload:
every request received an explicit answer, zero silent queuing or
drops; burst-300 p99 26.4 ms, churn converges to inflight 0, overload
bounded by provider delay with peak inflight observed exactly at the
concurrency wall. Degradation posture is documented: admission control
is explicit (per-key limits, circuits, upstream Retry-After); the
inflight gauge found dead during this phase was wired and is now part
of the standard exposition.

### 2. Chaos catalog executed; every finding fixed or node-tracked

**PASS.** `docs/reports/chaos-catalog.md` — 4/4 live scenarios matched
their written expected behavior: provider outage (full circuit
timeline through half-open recovery), mid-stream truncation
(error-frame-no-[DONE]), stall+disconnect (cancelled usage, zero
goroutine leak), fd exhaustion under RLIMIT_NOFILE=64 (explicit 502/503,
zero hangs, full recovery). Disk-full is covered by the existing unit
seam (StorageFailureResilience + bounded-queue drop-and-count tests).
Findings register: 2 observations carried (502/503 alert guidance →
p9.user-docs; fixed 10 s circuit cooldown fine as-is), **0 fix nodes
required**.

### 3. Security review complete: threat model updated, govulncheck clean

**PASS.** `docs/security/threat-model.md` v2 — 12 threat rows (T1–T12),
6 trust boundaries (incl. the new importer boundary B5), findings
register S-1…S-7 with an audit trail of file-level re-verification
(constant-time compares, cookie flags, CSRF, body caps, log redaction,
SSRF build+dial enforcement, key hygiene). All high findings fixed:
S-1 toolchain 37 reachable stdlib vulns → go1.26.9 pinned via go.mod
`toolchain`; S-2 x/sys GO-2026-5024 → v0.44.0; S-3 importer provider
validation (skip actions, 3 tests) → also found live during fuzzing
regression. govulncheck: **0 findings** (symbol + module level),
enforced in CI by the new `vuln` job.

### 4. Crash recovery: kill -9 matrix converges to explainable state

**PASS.** `docs/reports/crash-recovery.md` — 3/3 converge: idle kill →
byte-identical state; midflight kill at ~1100 req/s → **6646/6646
client-confirmed requests durable, zero loss** (unconfirmed in-flight
accounted by absence, documented); migration kill racing first-boot
migration → restart completes schema v5 and serves. Mechanism: WAL +
transactional migrations + 100 ms batch flush.

## Node evidence pack (8/8)

| Node | Deliverable | Key evidence |
|---|---|---|
| p8.load-soak ✅ | soak harness + budget report | 60s@60rps: 0 errors, p99 2.3 ms, flat resource curves; runtime gauges added |
| p8.load-burst ✅ | burst/churn/overload + degradation report | 100% explicit answers; inflight gauge wired (was dead); posture documented |
| p8.chaos-catalog ✅ | 4 live reproducers + results | 4/4 MATCH, 0 fix nodes; 2 observations carried |
| p8.crash-recovery ✅ | kill -9 matrix + reconciliation report | 3/3 converge; 6646/6646 confirmed requests durable |
| p8.security-review ✅ | threat model v2 + findings register | S-1/S-2/S-3 fixed, govulncheck 0 in CI, auth audit re-verified |
| p8.fuzzing ✅ | 15 native fuzz targets + seeded corpora | sse 2.58M execs clean; F-1 lone-CR SSE spec fix; F-2 negative-usage clamp; parity 41/41 re-proven; CI fuzz job |
| p8.perf-fixes ✅ | profile-driven optimizations + before/after tables | new full-composition benchmark; auth lookup cache (change-hook invalidation), touch coalescing, sync.Map; −53.5% / −56.3% / −43.8% ns/op (3 runs each); parity 41/41; 5 new invalidation tests |
| p8.gate ✅ | this report | all 4 criteria PASS |

## Carried observations (into Phase 9)

1. **Alert on both 502 and 503** during provider-outage windows
   (chaos observation) → fold into p9.user-docs operations section.
2. **In-memory sessions** (S-5) and per-IP rate budgets (S-6) are
   single-process by design; revisit only if a multi-instance
   deployment mode ever lands.
3. **Loopback/RFC1918 upstreams allowed by design** (S-7) with
   `NewSSRFGuard` extra-CIDR knob for hostile hosts — document in
   p9.user-docs security notes.
4. Toolchain is now pinned (`go 1.25.0` + `toolchain go1.26.9`); CI
   follows go.mod. Keep bumping patch versions via the vuln job's
   tripwire.

## Phase state

- Nodes: **8/8 done** (`tasks/phase-8.hardening.graph.yaml`)
- Graph: **77/86 nodes** across 10 phases; Phase 9 ready (all 9 nodes
  unblocked by this gate)
- All gates green at close: `go build`, `go vet`, `go test ./... -race`
  (go1.26.9), govulncheck 0, graph `--check` OK, parity 41/41,
  web build/typecheck

— graph-master, p8.gate
