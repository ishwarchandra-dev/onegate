# Troubleshooting

Symptom-first. Each entry states the cause and the fix; if your case
is not here, check the [CLI reference](../cli.md) and
[operations](operations.md) first, then the logs (they carry
`trace_id` on every line — quote it when asking for help).

## Startup

**`bind: address already in use` (exit 1)** — the port is taken.
`-port` another port, or stop the other listener. `ONEGATE_PORT`
overrides config; `onegate config` shows the resolved value.

**`dashboard not embedded: serving placeholder` (warning)** — the
binary was built without the dashboard (`make build` without
`make web`). The API and proxy still work. Rebuild with
`make web && make build`; release binaries and Docker images always
embed it.

**`read config file …: no such file` (exit 1)** — `-config`/`$ONEGATE_CONFIG`
points at a missing file. An explicit path must exist; discovery
(`./onegate.json`) silently falls back to defaults when no file is
anywhere.

**Exit code 2 on a command** — usage error: unknown command, bad
flag, or wrong positional count. `onegate help`, or `<command> -h`.

## Data directory

**`lost master key` / provider keys unreadable** — `master.key` is
missing or replaced. The db is intact but provider credentials are
unreadable. Fix: re-enter each provider's `api_key` (dashboard or
API `PUT`). Do not delete `onegate.db` — everything else (keys,
rules, usage) is unaffected.

**Disk full** — the gateway degrades explicitly: usage writes fail
loudly in logs, proxying continues until SQLite cannot commit.
Behavior under disk-full is chaos-tested (explicit errors, no
corruption); free space and it recovers without restart.

## Requests

**401 `invalid_api_key`** — the key is malformed or unknown; both
produce the identical response (no existence oracle). Check the key
is an `ogk-…` from this data dir; keys from another data dir are
unknown here.

**403 `key_revoked` / `key_expired`** — revoked keys propagate
instantly (change-hook invalidation). Expired keys at the boundary.

**404 on a model a key should see** — the key's `allowed_models`
whitelist excludes it (indistinguishable from a nonexistent model by
design), or the model really is absent. Check the key's scopes and
the model list (`GET /v1/models` — registry-backed, so also the
signal that routing changes have settled).

**429 with `Retry-After`** — the key's own quota (RPM/TPM/concurrency/
spend). The header's seconds are honest — retry after them. Spend
caps are lifetime: a capped key needs its cap raised or a new key.

**502/503 from the gateway** — upstream trouble. Circuits open after
repeated target failures; 503 `no-healthy-targets` means every target
in the chain is open (10 s cooldown, then one probe). Alert on both
502 and 503 (see [operations](operations.md#observability)); with
closed circuits it is configuration, with open ones it is upstream.

**Streaming cut off** — mid-stream upstream failures become in-stream
error events (no `[DONE]` after an error — by design). Retries happen
only before the first byte; after that the chain is frozen. Client
disconnects cancel upstream and record `cancelled` usage.

**Trailing-slash variants 404** — `/v1/messages/` (with slash) is not
`/v1/messages`; Go 1.22 mux semantics, pinned by parity. Call the
paths as documented in [routing](routing.md#client-endpoints).

## Import (from OmniRoute)

**Dry-run shows the plan, nothing written** — that is the default;
re-run with `--apply`.

**`import: parse …` errors** — the file must be the legacy
`omniroute.json` (the config, not the db). For usage history use
`onegate import-keys --usage-db legacy/omniroute.db`.

**Raw keys shown once** — that is the design (gateway stores only
peppered hashes). Lost them? Mint + revoke. `--keys-out` writes them
to a 0600 file instead of stdout.

## Security

- Provider keys: AES-GCM at rest under `master.key`; logs redact
  `api_key`/`token`/`secret` patterns; errors never echo credentials.
- The gateway dials only registered providers through the SSRF-guarded
  client. Loopback/RFC-1918 upstreams are **allowed by design**
  (self-hosted models behind the gateway); on a hostile multi-tenant
  host, restrict with the extra-CIDR knob (`NewSSRFGuard` in
  `internal/proxy/client/ssrf.go`).
- Body cap: 32 MiB (413 beyond). Read-header timeout guards slowloris.
- If you suspect a leaked virtual key: revoke it — propagation to the
  data plane is immediate.

## Getting further

- `onegate config` — what serve is actually running with.
- `GET /healthz` — schema version + status; Settings screen — uptime,
  config, schema version.
- Trace IDs: every log line and every error envelope carries the
  request's `trace_id`; the Logs view groups a request's full
  attempt chain.
