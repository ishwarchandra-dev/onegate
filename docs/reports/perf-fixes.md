# Optimization Cycle — p8.perf-fixes

Status: **complete** · Owner: performance-engineer · Node: p8.perf-fixes
Discipline: **profile-driven only** — every change below is justified by
a pprof ranking from the new full-composition hot-path benchmark; no
speculative micro-optimization. All benchmarks re-run 3×, all runs
reported. No correctness trades (parity corpus 41/41 after; full
`-race` suite green).

## Benchmark: the missing hot-path surface

Phase 5's pprof audit covered observability overhead; the full
production composition (ingest auth → quota gate → routing resolver →
fallback engine → translation → upstream → response path → usage
pipeline + metrics) had no benchmark. Added
`cmd/onegate/hotpath_bench_test.go` (composition root — the layering
contract forbids proxy tests importing routing/ratelimit):

- `BenchmarkHotPathNonStream` — sequential non-streaming chat completion
- `BenchmarkHotPathStream` — full SSE stream read to completion
- `BenchmarkHotPathParallel` — RunParallel non-streaming (contention view)
- `BenchmarkHotPathJSON` — response JSON parse sanity anchor

All against the in-process mock provider with the production wiring
(TTL credential cache, quota manager, usage pipeline, metrics,
per-request logging silenced to a discard handler).

## Baseline (before) — 3 runs each, go1.26.9, 2 vCPU

```
BenchmarkHotPathNonStream-2  10000  226308 ns/op  30009 B/op  412 allocs/op
BenchmarkHotPathNonStream-2  10000  211764 ns/op  29982 B/op  412 allocs/op
BenchmarkHotPathNonStream-2  10000  227464 ns/op  29992 B/op  412 allocs/op
BenchmarkHotPathStream-2      7810  357105 ns/op  44743 B/op  506 allocs/op
BenchmarkHotPathStream-2      7998  333187 ns/op  44711 B/op  506 allocs/op
BenchmarkHotPathStream-2      7498  355980 ns/op  44684 B/op  506 allocs/op
BenchmarkHotPathParallel-2   14919  163689 ns/op  30120 B/op  414 allocs/op
BenchmarkHotPathParallel-2   15012  155052 ns/op  30092 B/op  413 allocs/op
BenchmarkHotPathParallel-2   15555  154438 ns/op  30095 B/op  414 allocs/op
```

## Profile findings (pprof on the benchmark)

| Finding | Evidence | Action |
|---|---|---|
| **P-1**: every request paid a SQLite `GetByHash` roundtrip for key auth | `Verifier.Verify` 15.91% cum CPU; `scanVKey` 2.83% of allocated objects; sqlite frames across the top of the CPU profile | **Fixed** — verified-key cache (below) |
| **P-2**: `TouchLastUsed` spawned one goroutine + one UPDATE per request | verify.go fire-and-forget block visible in profile; `selectgo` 3.33% | **Fixed** — coalesced to ≤ 1 write per key per second |
| **P-3**: `quotaProxy.keys` serialized all requests through one mutex (2 map ops per request) | `internal/sync.Mutex.Lock` 1.67% flat at 2 vCPU; worse at higher core counts | **Fixed** — `sync.Map` (disjoint-key pattern: each request stores/deletes its own trace ID) |
| `Header.Clone` 6.7% alloc-space | all call sites inside `net/http` server internals (WriteHeader snapshot) | not gateway code — no action |
| `url.parse` 3.2% alloc-space | `http.NewRequest` per upstream call; base URLs are stable but parsing is cheap and the transport re-parses anyway | below optimization threshold — no action |
| usage pipeline `InsertBatch` 22.9% cum (after-fix profile) | background batch writer goroutine, **off the request path** (enqueue is the only hot-path cost, already bounded by design in p5) | by design — no action |

## Changes

### P-1 Verified-key cache (internal/auth, internal/storage)

`Verifier` now caches hash → record. **Correctness mechanism**: the
`VirtualKeyRepo` fires a change hook after every semantic mutation
(create / status / scopes / limits / meta / delete — never
`TouchLastUsed`), and the Verifier drops its entire cache on any fire.
The gateway process is the only writer, so a revocation observed by the
repo is reflected by the very next `Verify` — identical semantics to
the uncached lookup. Expiry stays a per-request time check against the
cached record. Only active, unexpired records are cached (rejected
states keep paying the storage lookup — conservative side). Cache
bounded at 4096 entries (overshoot clears; natural size is the
operator's key count).

The repo became instance-stable (`Store.vkeys`, created at `Open`) so
the hook registration target survives; `VirtualKeys()` keeps its
signature.

### P-2 Coalesced last-used touches (internal/auth)

The cache entry carries `lastTouch`; the fire-and-forget UPDATE spawns
only when > 1 s has elapsed (double-checked under the write lock so
concurrent requests for one key don't double-spawn). `last_used_ms` is
dashboard display metadata; losing sub-second granularity changes
nothing observable. Crash-loss semantics unchanged (best-effort before,
best-effort now).

### P-3 quotaProxy lock-free key map (cmd/onegate/adapters.go)

`map[string]domain.VirtualKey` + `sync.Mutex` → `sync.Map`. Each
request Stores and Deletes its own unique trace ID (disjoint access);
`onUsage` does a single `Load`. Fast path holds no lock.

## After — 3 runs each (same machine, same flags)

```
BenchmarkHotPathNonStream-2  22964  104565 ns/op  27907 B/op  346 allocs/op
BenchmarkHotPathNonStream-2  22972  105276 ns/op  27876 B/op  346 allocs/op
BenchmarkHotPathNonStream-2  22876  107040 ns/op  27874 B/op  346 allocs/op
BenchmarkHotPathStream-2     15297  155524 ns/op  42630 B/op  440 allocs/op
BenchmarkHotPathStream-2     16527  164245 ns/op  42599 B/op  440 allocs/op
BenchmarkHotPathStream-2     15710  143585 ns/op  42576 B/op  440 allocs/op
BenchmarkHotPathParallel-2   26109   97827 ns/op  27907 B/op  346 allocs/op
BenchmarkHotPathParallel-2   26959   86560 ns/op  27881 B/op  346 allocs/op
BenchmarkHotPathParallel-2   27373   87207 ns/op  27883 B/op  346 allocs/op
```

## Before/after summary (median of 3)

| Benchmark | ns/op before | ns/op after | Δ time | allocs/op before | after | Δ allocs | B/op before | after |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| NonStream | 226,308 | 105,276 | **−53.5%** | 412 | 346 | **−16.0%** | 30,009 | 27,876 |
| Stream | 355,980 | 155,524 | **−56.3%** | 506 | 440 | **−13.0%** | 44,743 | 42,630 |
| Parallel | 155,052 | 87,207 | **−43.8%** | 413 | 346 | **−16.2%** | 30,092 | 27,881 |

Re-profile after: `Verifier.Verify` no longer appears among top gateway
frames (was 15.91% cum); the remaining CPU is `net/http` transport
machinery (inherent) and the background usage-pipeline writer (off the
request path, bounded by design).

## Correctness evidence

- Full `go test ./... -race` green (incl. 5 new cache-invalidation
  tests: revocation / scopes / limits / expiry / deletion each take
  effect on the very next Verify — `internal/auth/verify_cache_test.go`)
- Parity replay corpus: **41/41** after all changes
- go vet + govulncheck clean; graph integrity OK
- No timeout, cap, or error-handling path was touched

## Reproducing

```bash
go test ./cmd/onegate -bench BenchmarkHotPath -benchmem -count 3
go test ./cmd/onegate -bench BenchmarkHotPathNonStream -cpuprofile cpu.out -memprofile mem.out
```

— performance-engineer, p8.perf-fixes
