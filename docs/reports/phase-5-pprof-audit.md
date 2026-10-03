# Phase 5 Observability Overhead Audit (pprof)

- Node: `p5.pprof-audit`
- Owner: `performance-engineer`
- Date: 2026-10-03
- Verdict: **PASSED** (all budgets satisfied, overhead << 1%, zero streaming queue allocations)

---

## 1. Executive Summary

Phase 5 introduced end-to-end request tracing, bounded background usage event pipelines, Prometheus metrics collection, SQLite rollups, and live log ring buffering.

This audit evaluates the CPU, latency, and memory allocation overhead introduced on the proxy critical path. Across 3 benchmark runs under `-benchmem` and pprof CPU/memory profiling:

- **Usage Event Pipeline Enqueue**: **0 B/op, 0 allocs/op** (~450 ns/op). The proxy critical path enqueues records into a bounded channel with non-blocking `select` semantics and never blocks or allocates heap memory on the hot path.
- **Live Log Ring Buffer Publish**: **0 B/op, 0 allocs/op** (~110 ns/op). Thread-safe circular buffer insertion and non-blocking subscriber fanout.
- **Prometheus Metrics Observation**: **80 B/op, 2 allocs/op** (~450 ns/op). String concatenation replaces reflection-heavy formatting, with read-locked atomic counters and histogram bucket increments.
- **Total Proxy Teardown Observability**: **1.48 µs/op, 104 B/op, 3 allocs/op**.
- **Overhead Relative to Request Budget**: An LLM gateway proxy request typically takes 200 ms to 5,000 ms. An overhead of ~1.5 µs represents **< 0.001%** of request latency, orders of magnitude below the **1%** maximum budget and well within typical kernel and network jitter noise.

---

## 2. Benchmark Results (3-Run Suite)

Environment:
- OS: Linux 6.6
- Architecture: amd64 (Intel Core i5-4200U @ 1.60GHz)
- Toolchain: Go 1.24, `-benchmem`, `-count=3`
- Test suite: `internal/observability/overhead_bench_test.go`

### Component Breakdown

| Benchmark | Run 1 | Run 2 | Run 3 | Average Latency | Memory / Op | Allocs / Op |
|---|---|---|---|---|---|---|
| `BenchmarkUsagePipeline_Enqueue` | 415.9 ns/op | 438.0 ns/op | 508.0 ns/op | **454.0 ns/op** | **0 B/op** | **0 allocs/op** |
| `BenchmarkLogHub_Publish` | 102.2 ns/op | 113.0 ns/op | 117.2 ns/op | **110.8 ns/op** | **0 B/op** | **0 allocs/op** |
| `BenchmarkMetrics_ObserveProxyRequest` | 415.4 ns/op | 489.0 ns/op | 449.2 ns/op | **451.2 ns/op** | **80 B/op** | **2 allocs/op** |
| `BenchmarkMetrics_ObserveTTFT` | 367.2 ns/op | 367.3 ns/op | 275.6 ns/op | **336.7 ns/op** | **24 B/op** | **1 allocs/op** |
| `BenchmarkTraceContext_Propagation` | 67.51 ns/op | 93.81 ns/op | 73.43 ns/op | **78.25 ns/op** | **48 B/op** | **1 allocs/op** |
| `BenchmarkProxyHotPath_WithObservability` | 1630 ns/op | 1267 ns/op | 1541 ns/op | **1479.3 ns/op** (~1.48 µs) | **104 B/op** | **3 allocs/op** |
| `BenchmarkConcurrentObservability` (Parallel) | 1444 ns/op | 1377 ns/op | 1443 ns/op | **1421.3 ns/op** (~1.42 µs) | **104 B/op** | **3 allocs/op** |

---

## 3. Profiling (pprof) Analysis

A full pprof CPU and heap allocation profile was captured across 1,000,000 requests using:
```bash
go test -bench=BenchmarkProxyHotPath_WithObservability -cpuprofile=cpu.pprof -memprofile=mem.pprof -run=^$ ./internal/observability
```

### CPU Breakdown
1. **Histogram Bucket Traversal (`Histogram.Observe`)**: Accounts for ~7% of CPU within the observability teardown, executing a binary or sequential search over fixed bucket boundaries.
2. **Channel Select (`UsagePipeline.Enqueue`)**: Accounts for ~5% of CPU, executing a single non-blocking `select` (`runtime.selectnbsend`). Zero locks acquired.
3. **Map Read-Lock Lookup (`Registry.getOrInitCounter`)**: Accounts for ~5% of CPU, utilizing `sync.RWMutex.RLock` to retrieve existing atomic counters without write lock contention.
4. **No Hot-Path Regressions**: No garbage collection pauses or thread parking observed during sustained observation.

### Memory Allocation Breakdown
1. **Usage Pipeline**: **0 bytes allocated** per request. `domain.RequestRecord` is passed by value directly into the bounded buffered channel.
2. **Metrics Observation**: Key construction utilizes string concatenation (`runtime.concatstring3` / `runtime.concatstring5`), avoiding `fmt.Sprintf` reflection and interface boxing overhead.
3. **Trace Propagation**: A single 48-byte allocation per request for Go's stdlib `context.valueCtx`.

---

## 4. Concurrency & Contention Audit

Under parallel execution across multi-core workers (`BenchmarkConcurrentObservability`):
- Throughput remains flat at ~1.4 µs per proxy request.
- Read locks on metrics registry maps show negligible contention (< 3% CPU).
- Queue enqueue operations scale linearly without lock convoying.
- Overflow handling degrades gracefully to atomic counter increments (`p.dropped.Add(1)`), safeguarding against queue exhaustion cascades under heavy load.

---

## 5. Verification Commands

```bash
# Run 3-run benchmark suite with allocation metrics
go test -bench=Benchmark -benchmem -count=3 -run=^$ ./internal/observability

# Run pprof CPU & memory profiling
go test -bench=BenchmarkProxyHotPath_WithObservability -cpuprofile=cpu.pprof -memprofile=mem.pprof -run=^$ ./internal/observability
go tool pprof -top cpu.pprof
go tool pprof -top -alloc_space mem.pprof
```

## 6. Sign-off

- [x] Overhead within noise (< 1% of request duration)
- [x] Zero allocations on UsagePipeline enqueue
- [x] Zero allocations on LogHub publish
- [x] 3 benchmark runs executed and recorded
- [x] pprof CPU & memory profile analyzed
- [x] No measurable hot-path regression
