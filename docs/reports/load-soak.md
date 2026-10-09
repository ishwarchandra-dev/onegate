# p8.load-soak — Sustained RPS Soak Report

Executor: `scripts/loadtests/soak.py` (p8.load-soak). Environment: real
`onegate` binary + `cmd/mockprovider` on localhost, fresh data dir,
single provider/model/key seeded via the management API, mixed
stream/non-stream traffic (50/50), dedicated raw-socket TTFT probes.

## Results (2026-10-09)

### Primary run — 60 s @ 60 RPS target

| Metric | Value | Budget | Verdict |
| --- | --- | --- | --- |
| Completed / sent | 3600 / 3600 | — | — |
| Errors | 0 (0.00%) | < 0.1% | PASS |
| Throughput (actual) | 58.1 req/s | ≥ 90% of target | PASS |
| Latency p50 | 1.0 ms | — | — |
| Latency p95 | 1.7 ms | < 50 ms | PASS |
| Latency p99 | 2.3 ms | < 100 ms | PASS |
| TTFT p50 | 0.8 ms | — | — |
| TTFT p95 | 1.2 ms | — | — |
| TTFT p99 | 2.0 ms | < 60 ms | PASS |
| Goroutine growth (Q1→Q4) | +0.7 | \|Δ\| < 5 | PASS |
| OS-thread growth (Q1→Q4) | +0.2 | \|Δ\| < 5 | PASS |
| Open-FD growth (Q1→Q4) | +0.4 | \|Δ\| < 10 | PASS |

### Stability run — 30 s @ 30 RPS

Same picture: 900/900 completed, 0 errors, p99 2.0 ms, TTFT p99 1.3 ms,
resource growth within noise (goroutines +1.1, threads +0.4, FDs +0.5).

## Interpretation

- The localhost-mock topology isolates gateway overhead from provider
  latency: the budgets therefore measure the gateway's own cost (parse,
  route, quota, translate, stream) rather than network reality. Against
  a real provider, p99 adds provider latency; the gateway's contribution
  stays in the low milliseconds.
- Flat goroutine/FD curves across the soak window indicate no
  connection or goroutine leaks in the proxy path (the two classic
  gateway failure modes under sustained streaming load).
- The quotas/routing/usage-write paths added by p7.parity-fixes are all
  exercised: every request passed the quota gate, resolved through the
  routing registry, and produced a durable usage record (3600 records
  over the run — verified by the post-soak request count).

## Reproducing

```bash
python3 scripts/loadtests/soak.py --duration 60 --rps 60
```

Resource gauges (`onereq_go_goroutines`, `onereq_go_os_threads`,
`onereq_process_open_fds`) were added to `/metrics` for this node and
are part of the standard exposition now.
