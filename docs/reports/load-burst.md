# p8.load-burst — Burst & Stream-Churn Degradation Report

Executor: `scripts/loadtests/burst.py` (p8.load-burst). Same topology as
the soak (real binary, mock provider, seeded via the management API);
scenarios fire coordinated concurrency walls rather than paced load.

## Results (2026-10-09)

| Scenario | N | OK | Errors | p50 | p95 | p99 | max | Wall |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| burst-100 | 100 | 100 | 0 | 2.0 ms | 4.9 ms | 6.6 ms | 9.8 ms | 0.06 s |
| burst-300 | 300 | 300 | 0 | 2.0 ms | 5.5 ms | 26.4 ms | 29.9 ms | 0.24 s |
| stream-churn | 200 | 200 | 0 | 0.8 ms | 4.6 ms | 10.1 ms | 26.0 ms | — |
| slow-overload | 60 | 60 | 0 | 504.5 ms | 510.2 ms | 510.9 ms | 511.9 ms | 0.53 s |

## Degradation posture (deliverable)

**Every request in every scenario received an explicit answer.** There
are no silent drops, no unbounded hangs, and no ambiguous failures:

1. **Burst walls (100, 300 simultaneous)**: the gateway absorbs the wall
   with a latency tail proportional to the wall size (p99 6.6 ms →
   26.4 ms) and zero errors. No request was queued past the wall's
   natural drain time — completion wall times (0.06 s / 0.24 s) show the
   requests execute in parallel, not serialized through any implicit
   queue.
2. **Stream churn** (150 rapid sequential short streams + 50 parallel):
   post-churn state converges — in-flight gauge returns to 0 within 3 s,
   goroutines/FDs settle to baseline. No connection or goroutine
   accumulation from short-lived streams.
3. **Slow-provider overload** (60 concurrent against a 500 ms provider):
   latency is **bounded by the provider delay** (p99 510.9 ms ≈ the
   mock's 500 ms + gateway overhead), not by queueing; the
   `onereq_inflight_requests` gauge peaked at exactly 60 — degradation
   is *observable* in real time, and every response was a real 200.

### Queue limits & 503 posture

The gateway has no implicit internal queue: Go's HTTP server accepts
connections (OS backlog), and each request executes immediately in its
own goroutine. Admission control is therefore *delegated and explicit*:

- **Per-key concurrency limits** (`limits.concurrency`) reject with
  429 `concurrency_limit_exceeded` + `Retry-After` (verified by parity
  corpus case CC-40).
- **Circuit breakers** reject unreachable models with 503
  `overloaded_error` (CC-32).
- **Upstream 429s** surface with the provider's own `Retry-After`
  (CC-17/CC-18).

A *global* in-flight ceiling (503 when the whole gateway exceeds N
concurrent requests) was considered and deliberately not added: with
per-key limits as the operator-facing control and the inflight gauge as
the observability surface, a global cap would add a new failure mode
without a workload in evidence needing it. Recorded as a p8.gate
observation for reconsideration with real deployment data.

## Fix landed during this node

`onereq_inflight_requests` was defined but never incremented (dead
gauge). Now wired at the data-plane ingress (`quotaProxy.Execute`,
cmd/onegate/adapters.go) — every proxied request increments/decrements
per protocol, which is what made the slow-overload peak observable.

## Reproducing

```bash
python3 scripts/loadtests/burst.py
```
