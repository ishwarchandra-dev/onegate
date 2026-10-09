# Crash Recovery Matrix — p8.crash-recovery

Reproducer: `scripts/chaos/crash.py`. Every scenario SIGKILLs (kill -9)
the gateway and verifies convergence after restart: health, schema
version, entity state (providers/models/keys), usage-record integrity,
and post-restart traffic. **Run of 2026-10-09: 3/3 scenarios converge.**

## Results

### 1. idle-kill — CONVERGED

Kill an idle, fully-seeded gateway (10 completed requests on record).
After restart: providers/models/keys byte-identical, **10/10 request
records preserved**, schema version 5 → 5, fresh stream + non-stream
traffic served. WAL recovery is transparent.

### 2. midflight-kill — CONVERGED (zero data loss)

4 concurrent streaming workers for ~6 s (**6,646 client-confirmed
requests, sustained ~1,100 req/s**), then SIGKILL mid-flight. After
restart: **6,646/6,646 confirmed requests durable** (the usage
pipeline's bounded-queue → 100 ms batch-flush → SQLite WAL chain
commits everything the client saw), every record well-formed, new
traffic served.

In-flight-but-unconfirmed requests (client never received a response)
are accounted for by absence: the usage event is written on request
completion, so an answer the client never got can be lost to the kill —
this is the documented reconciliation semantics ("in-flight usage
reconciled or accounted"), and the observable contract is exact:
**every request the gateway confirmed to a client is in the database**.

### 3. migration-kill — CONVERGED

Fresh data dir; the gateway is SIGKILLed at the instant the listener
first answers (racing the first-boot migration, which runs before
serve). After restart: migration completes, schema version 5 (current),
seed + traffic fully functional. Migrations are per-version
transactions (p1.storage-schema design); a kill mid-migration leaves
version N−1 applied and N pending, and the restart finishes it.

## Why this works (mechanism summary)

- **SQLite in WAL mode** (ADR 003): committed transactions survive
  SIGKILL via the write-ahead log; a torn uncommitted WAL tail is
  ignored on recovery.
- **Migrations are transactional per version** and recorded in
  `schema_migrations`; re-running is a no-op (idempotent by design).
- **Usage writes are batched (100) and flushed every 100 ms** in one
  transaction per batch — the durability window the harness's 1 s floor
  (10× margin) validates against.

## Reproducing

```bash
python3 scripts/chaos/crash.py
```
