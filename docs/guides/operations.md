# Operations

Running OneGate in anger: configuration, hot reload, observability,
backups, upgrades, and the OmniRoute migration path.

## Configuration

Resolution is **flags > environment > config file > defaults** (ADR
002; full flag/env/file reference in the [CLI reference](../cli.md)).
Preview any chain without booting:

```bash
onegate config                    # exactly what serve would use
ONEGATE_PORT=9000 onegate config  # preview an env override
```

The config file is partial-override JSON — any subset of keys; unset
keys keep defaults. Example `onegate.json`:

```json
{
  "host": "0.0.0.0",
  "port": 7420,
  "data_dir": "/var/lib/onegate",
  "log_level": "info",
  "http": { "read_header_timeout_ms": 10000 },
  "reload": { "enabled": true, "poll_ms": 2000 }
}
```

Keep read/write HTTP timeouts **off** (0) when streaming — they cap
stream lifetime; `read_header` is the slowloris guard and must stay
> 0. Hot reload (`reload.enabled`) applies file changes on save (mtime
poll + `SIGHUP`); a bad file keeps the last-good config and logs it —
the gateway never falls over on config.

## Observability

| Surface | Where | Notes |
|---|---|---|
| Health | `GET /healthz` | `{"status":"ok",…}` — liveness |
| Metrics | `GET /metrics` | Prometheus text; admin-gated |
| Structured logs | stdout (slog JSON) | redacted (`api_key`, `token`, `secret`…), `trace_id` on every line |
| Live logs | dashboard Logs view (SSE) or `/api/logs/live` | filter by level/trace |
| Usage analytics | dashboard Usage view | rollup-backed; CSV export |

Key metrics to alert on (from the chaos-catalog findings):

- **`onegate_upstream_5xx`-class counters — alert on both 502 and
  503** during provider-outage windows. The gateway degrades
  explicitly (circuits open, 503 no-healthy-targets) — it never
  silently queues. A 502/503 burst with recovering circuits is
  upstream trouble; a sustained one with closed circuits is config.
- TTFT histograms per provider/model (streaming latency regressions).
- Usage-pipeline drop counter (should be 0; overflow is
  drop-and-count by design — drops mean sustained >10k events in
  flight).

## Backups

Everything lives in the data directory. Back it up cold or hot:

```bash
sqlite3 /var/lib/onegate/onegate.db ".backup '/backup/onegate-$(date -u +%F).db'"
```

- `onegate.db` — providers, models, rules, keys, admin, usage history.
- `master.key` — AES-GCM master secret. **Back it up separately and
  securely**; without it the db's provider keys are unreadable (see
  [troubleshooting](troubleshooting.md)).
- Crash safety: WAL mode + transactional migrations; `kill -9` mid-
  traffic loses nothing confirmed (verified by a kill matrix at
  ~1100 req/s — 6646/6646 confirmed requests durable).

Restore = stop gateway, put files back, start. Schema migrations run
forward automatically on boot.

## Upgrades

Dedicated guide with per-release paths: [upgrading](upgrading.md)
(the v1.0.0 path is the OmniRoute v3.8.52 migration below).

- One binary, no external services. Pin versions
  (`ghcr.io/ishwarchandra-dev/onegate:1.0.0`, release URLs with
  checksums).
- The data dir migrates forward on boot; schema version is visible in
  `/healthz` and Settings.
- Rolling back: replace the binary — migrations are forward-only, so
  roll back only within a compatibility window noted in the release
  notes, or restore the db backup.

## Migrating from OmniRoute v3.8.52

```bash
onegate import  omniroute.json --apply --data-dir /var/lib/onegate    # providers, models, rules
onegate import-keys omniroute.json --apply \
  --usage-db legacy/omniroute.db \
  --keys-out /root/new-keys.txt \
  --data-dir /var/lib/onegate                                            # keys + history
```

- Both are **dry-run by default** — run without `--apply` first and
  read the plan; the legacy install is only ever read.
- Idempotent: re-running maps existing rows instead of duplicating.
- Keys are re-minted (legacy hashes are unrecoverable by design);
  revoked stays revoked; usage rollups preserve aggregates exactly.
- Redirect your clients: same paths (`/v1/…`), swap the provider API
  key for a minted `ogk-…` key. Behavioral parity is corpus-proven
  (41 replayed cases, 0 blockers) — see
  [docs/compat/report-phase7.md](../compat/report-phase7.md).

## Scaling posture

One process, by design. All state is in the embedded SQLite; sessions
and per-IP rate budgets are in-memory (single-process). Run one
instance per data directory; horizontalizing would require moving
state out — not on the v1 roadmap.
