# Chaos Catalog — p8.chaos-catalog

Reproducers: `scripts/chaos/chaos.py` (live scenarios, each boots a real
gateway + mock provider). Disk-full is covered at the unit level (see
entry 5). **Run of 2026-10-09: 4/4 live scenarios match their written
expected behavior; every catalog entry is closed with either "matches"
or a linked fix.**

## Catalog

### 1. Provider outage (live) — MATCHES

**Injection**: mock provider process killed mid-traffic, restarted
after the outage window.
**Timeline observed**: healthy 200 → outage window: explicit 502
(`api_error`, transport failures classified) mixing to 503 → circuit
opens after consecutive failures: clean 503
`{"error":{"message":"no healthy targets available","type":"overloaded_error"}}`
→ provider restored + 10 s cooldown: half-open probe succeeds → 200.
**Verdict**: degradation explicit and observable at every step;
recovery requires no operator action.

### 2. Stream truncation (live) — MATCHES

**Injection**: provider model `sc-midstream` emits the first SSE frame,
then aborts the connection.
**Observed**: client receives the first frame, then a native
`{"error":…}` SSE frame; **no `[DONE]` sentinel**; the very next
request succeeds normally (no lingering per-connection state).
**Verdict**: matches the parity contract (checklist D-8; corpus CC-33).

### 3. Provider stall + client disconnect (live) — MATCHES

**Injection**: provider model `sc-stall` hangs for 30 s; the client
disconnects at 800 ms.
**Observed**: usage record written with `status=cancelled`; goroutines
return to the pre-experiment baseline (10 → 10) after the stalled
upstream times out — no leak from the abandoned request.
**Verdict**: cancellation propagates end-to-end (client → gateway →
upstream), usage accounting stays truthful.

### 4. File-descriptor exhaustion (live) — MATCHES

**Injection**: gateway launched with `RLIMIT_NOFILE=64`; 80 concurrent
requests against a 2-second provider model (connections held open so
the ceiling is genuinely reached).
**Observed**: 27 requests served 200; the remainder answered with
explicit 502/503 envelopes; **zero transport failures, zero hangs**
(total wall 2.0 s — bounded by the provider delay, not the failure);
after the burst drains, sequential requests all return 200. No deadlock
or wedged accept loop under fd starvation.
**Verdict**: degradation is explicit and the gateway self-heals.

### 5. Disk full / storage write failure (unit) — MATCHES (existing evidence)

A full disk manifests as SQLite write errors in the usage pipeline.
The designed behavior — never block or fail the proxy path, drop and
count — is verified by
`internal/observability/usage_test.go: TestUsagePipeline_StorageFailureResilience`
(writer failures isolated, pipeline keeps draining) and
`TestUsagePipeline_DropAndCountUnderLoad` (bounded queue overflow →
`dropped` counter, zero proxy-path impact). Live disk-filling is not
reproducible without mount privileges in CI; the unit seam is the
faithful equivalent.

## Findings register

No fix nodes required: every scenario matched its written expected
behavior. Two observations worth carrying:

1. **Outage window mixes 502/503**: during the first seconds of an
   outage, clients see 502 (transport failure, last attempt) before the
   circuit opens and the answer stabilizes on 503. This is correct
   semantics (the two errors say different things) but dashboards
   should alert on both. Noted for p9.user-docs.
2. **Circuit cooldown is fixed at 10 s** (`DefaultHealthConfig`); the
   half-open probe added no visible latency on recovery. No change
   needed; configurable cooldown is a future option if operators ask.

## Reproducing

```bash
python3 scripts/chaos/chaos.py                 # all scenarios
python3 scripts/chaos/chaos.py --scenario fd-exhaust
```
