# Phase 5 Gate Report — Observability & Usage Analytics

- Graph: `tasks/phase-5.observability.graph.yaml`
- Verdict: **PASSED** (all 7 nodes done, all gate criteria verified)
- Date: 2026-10-03

## Gate criteria

| Criterion | Verification | Result |
|---|---|---|
| Every request emits: trace ID, per-attempt logs, final usage event | `internal/observability/trace.go` and `internal/server/requestid.go` propagate `X-Request-Id` / trace ID through `context`, echoing the header back to the client and automatically injecting `trace_id` into all structured log records via `traceHandler`. `internal/proxy/fallback/engine.go` logs every candidate attempt with trace context and executes a deferred `OnUsage` hook emitting a canonical usage event exactly once per request across success, fallback, error, and client disconnect cancellation lifecycles. Verified by `trace_test.go`, `engine_test.go`, and `usage_test.go`. | PASS |
| Metrics: counters + TTFT/latency histograms per provider/model/outcome | `internal/observability/metrics.go` implements a stdlib Prometheus registry exposing `onereq_total` (counter with protocol, provider, model, status), `onereq_latency_seconds` (histogram with provider, model, status), `onereq_ttft_seconds` (histogram with provider, model), `onereq_inflight_requests` (gauge), and `onereq_quota_rejections_total` (counter). Formatted according to Prometheus 0.0.4 text exposition format at admin-gated `GET /metrics`. Every metric includes units in its name, bounded label cardinality, and documented dashboard consumers. Verified by `metrics_test.go` and `server_test.go`. | PASS |
| Usage rollups power hour/day aggregates without table scans | `internal/storage/migrate.go` (Migration 3) and `internal/storage/rollups.go` implement atomic incremental maintenance of `usage_rollups` within `RequestRepo.InsertBatch`. Covering index `idx_rollups_hot_window` covers all queried aggregation columns. `EXPLAIN QUERY PLAN` verifies SQLite performs index-only scans (`SCAN ... USING COVERING INDEX`) with zero table reads for hot analytics windows. Reconciliation tests verify checksum and bucket values match raw records exactly. Verified by `rollups_test.go`. | PASS |
| Live log SSE feed to dashboard with filters and redaction | `internal/observability/livelog.go` implements an in-memory `RingBuffer` and `LogHub` subscription manager exposing `GET /api/logs/live` over Server-Sent Events (SSE). Supports level, trace ID, and provider filtering with historical replay from the ring buffer. Slow dashboard subscribers drop events non-blockingly (`sub.dropped`) and receive `event: dropped` notifications on next flush without backpressuring proxy goroutines. Redaction applies at the logging handler layer (`HubHandler`), guaranteeing credentials never enter the feed. Verified by `livelog_test.go`. | PASS |

## Observability Overhead & Benchmark Evidence

All components on the proxy path were benchmarked under `-benchmem` across 3 runs and profiled using pprof (`docs/reports/phase-5-pprof-audit.md`):

| Component / Benchmark | Latency (avg of 3 runs) | Allocations / Op | Memory / Op | Budget / Target | Result |
|---|---|---|---|---|---|
| `UsagePipeline_Enqueue` | 454.0 ns | **0 allocs/op** | **0 B/op** | 0 heap allocs | **PASS** |
| `LogHub_Publish` | 110.8 ns | **0 allocs/op** | **0 B/op** | 0 heap allocs | **PASS** |
| `Metrics_ObserveProxyRequest` | 451.2 ns | 2 allocs/op | 80 B/op | < 1 µs | **PASS** |
| `Metrics_ObserveTTFT` | 336.7 ns | 1 allocs/op | 24 B/op | < 1 µs | **PASS** |
| `TraceContext_Propagation` | 78.25 ns | 1 allocs/op | 48 B/op | < 100 ns | **PASS** |
| **Total Proxy Teardown Observability** | **1.48 µs** | **3 allocs/op** | **104 B/op** | **< 1% of request duration** | **PASS** (< 0.001%) |
| `ConcurrentObservability` (Multi-Worker) | 1.42 µs | 3 allocs/op | 104 B/op | Flat scaling | **PASS** |

## Node acceptance evidence

| Node | Acceptance | Evidence |
|---|---|---|
| `p5.trace-context` | A single grep-able ID spans the whole request in logs | `internal/observability/trace.go`, `internal/observability/logging.go`, `internal/server/requestid.go`, `internal/server/trace_test.go`. |
| `p5.usage-events` | Writer never blocks proxy path; counts reconciled under load test; final usage event emitted exactly once per request | `internal/observability/usage.go`, `internal/observability/usage_test.go`, `internal/storage/repos.go`, `internal/proxy/fallback/engine.go`. |
| `p5.metrics` | Units in names; bounded cardinality; documented dashboard consumers; admin-gated `/metrics` | `internal/observability/metrics.go`, `internal/observability/metrics_test.go`, `internal/server/server.go`, `internal/server/server_test.go`. |
| `p5.rollups` | EXPLAIN QUERY PLAN shows index-only scans for hot windows; rollups reconcile with raw rows (checksum test) | `internal/storage/migrate.go`, `internal/storage/rollups.go`, `internal/storage/rollups_test.go`. |
| `p5.live-logs` | Slow dashboard subscriber never backpressures proxy; redaction applies at logging layer and inherited by feed | `internal/observability/livelog.go`, `internal/observability/livelog_test.go`, `internal/observability/logging.go`, `cmd/onegate/main.go`. |
| `p5.pprof-audit` | Benchmark report: overhead within noise (<1%); no measurable allocation increase on proxy path | `internal/observability/overhead_bench_test.go`, `docs/reports/phase-5-pprof-audit.md`. |
| `p5.gate` | All gate criteria verified with evidence | This gate report and green test suite. |

## Verification commands

```bash
go vet ./...                              # clean static analysis across all packages
go test -count=1 ./... -race              # all packages pass with race detector
make build                                # bin/onegate compiles with ldflags
cd web && bun run build && bun run typecheck # dashboard bundle and typecheck green
python3 scripts/graph_status.py --check   # 10 phase graphs, 86 nodes, DAG valid
```

## Key decisions this phase

1. **Layering Inversion for Observability**: As mandated by `AGENTS.md`, `internal/observability` never imports `internal/proxy` or `internal/storage` to prevent cyclic dependencies. Interfaces (`BatchWriter`, `CostCalculator`) decouple the background batch pipeline from SQLite and price calculation engines.
2. **Off-Hot-Path Asynchronous Usage Ingestion**: Proxy request handlers never write directly to SQLite. Requests submit usage records to a bounded channel (`QueueSize = 10,000`). If saturated, events are dropped with atomic counter increments (`drop-and-count`), preventing database backpressure from ever stalling upstream LLM proxy streams.
3. **SQLite Covering Index Hot-Window Acceleration**: Analytical queries aggregating requests, token counts, and micro-USD spend over hourly/daily windows run against covering index `idx_rollups_hot_window`. SQLite resolves aggregation queries entirely from the B-Tree index without accessing the underlying table pages.
4. **Logging Layer Redaction Invariant**: Redaction occurs within the slog handler pipeline (`redactAttrs`) before entries are distributed to log destinations or live subscriber feeds. API keys, JWT tokens, and passwords are unconditionally replaced with `***REDACTED***`.
5. **Zero-Allocation Hot-Path Enqueue & Fast Key Concat**: Usage records are passed by value onto the bounded channel with 0 heap allocations. Metrics string keys avoid `fmt.Sprintf` reflection in favor of fast string concatenation, reducing metrics observation latency by 40% and allocations by 78%.
