# OneGate CLI reference

Every flag, every command, every exit code — the complete contract of
the `onegate` binary. This page is kept honest by
`scripts/check_cli_docs.py` (CI): a flag that is not documented here
fails the build, and so does a documented flag that no longer exists.

## Command tree

```
onegate [flags]                 same as `onegate serve` (back-compat)
onegate serve   [flags]         run the gateway
onegate import  <file> [flags]  migrate a legacy OmniRoute install (dry-run default)
onegate import-keys <file> [flags]
                                migrate legacy virtual keys + usage history
onegate config  [flags]         print the fully resolved runtime config as JSON
onegate -version                print version and exit
onegate help | -h               help (top level); `<command> -h` per command
```

A first argument that starts with `-` is a flag for the implicit serve
path. Any other unknown first argument is a usage error (exit 2) with a
pointer to `onegate help`.

## Exit codes

The binary:

| Code | Meaning | Examples |
|---|---|---|
| 0 | success (including `-h`/`--help` and `-version`) | help printed to stdout |
| 1 | runtime error | storage open failure, invalid resolved config, import apply failure, port already in use at listen time |
| 2 | usage error | unknown command, undefined flag, wrong number of positional arguments |

Usage errors print `onegate: <reason>` to stderr (exit 2); help goes to
stdout (exit 0).

The `npx onegate` launcher ([`launcher/`](../launcher/)) wraps the
binary and forwards the child's exit code verbatim (signals become
`128+N`). Its own failures use distinct codes so scripts can tell a
gateway problem from a delivery problem:

| Code | Meaning (launcher) |
|---|---|
| 1 | usage/platform error (unsupported platform, bad env, spawn failure) |
| 2 | network error (unreachable download root, HTTP ≠ 200, stall) |
| 3 | verification failure — corrupt or tampered download, refused |
| 4 | extraction/install failure |

## `onegate serve`

Runs the gateway: embedded dashboard, management API and the OpenAI /
Anthropic / Gemini proxy endpoints on one port (default `127.0.0.1:7420`,
health at `GET /healthz`).

| Flag | Type | Default | Also settable via | Notes |
|---|---|---|---|---|
| `-host` | string | `127.0.0.1` | env `ONEGATE_HOST`, config `host` | bind address |
| `-port` | int | `7420` | env `ONEGATE_PORT`, config `port` | 1–65535 |
| `-data-dir` | string | `.onegate` | env `ONEGATE_DATA_DIR`, config `data_dir` | holds `onegate.db` + `master.key`; created if missing |
| `-log-level` | string | `info` | env `ONEGATE_LOG_LEVEL`, config `log_level` | `debug` \| `info` \| `warn` \| `error` |
| `-config` | string | (discovery) | env `ONEGATE_CONFIG` | explicit config file; see below |
| `-version` | bool | — | — | print version and exit |

Precedence, highest wins: **flags > environment > config file >
defaults** (ADR 002). Config discovery when `-config` is not given:
`$ONEGATE_CONFIG`, then `./onegate.json` (cwd). The file may be partial
— any subset of the keys below; unspecified fields keep their defaults.

Config-file keys and defaults (all optional in `onegate.json`):

```json
{
  "host": "127.0.0.1",
  "port": 7420,
  "data_dir": ".onegate",
  "log_level": "info",
  "http": {
    "read_header_timeout_ms": 10000,
    "read_timeout_ms": 0,
    "write_timeout_ms": 0,
    "idle_timeout_ms": 120000
  },
  "reload": { "enabled": true, "poll_ms": 2000 }
}
```

HTTP timeout semantics (ADR 005): read/write default OFF so streaming
responses are never cut mid-flight; `read_header` is the slowloris
guard and must stay > 0. Hot reload (`reload.enabled`) re-reads the
config file on change and on `SIGHUP`, keeping the last-good config on
a parse failure.

Examples:

```bash
onegate serve -port 8000 -log-level debug
onegate -data-dir /var/lib/onegate            # subcommand is optional
ONEGATE_ADMIN_TOKEN=secret onegate serve      # gate /metrics + admin API
```

## `onegate config`

Prints exactly what `serve` would boot with, as indented JSON, and
exits. Same flags as serve (minus `-version`), same precedence. Useful
for debugging a precedence chain or previewing a deployment:

```bash
onegate config
ONEGATE_PORT=9000 onegate config              # preview an env override
onegate config -config prod.json | jq .http
```

The output contains no secrets: provider keys live encrypted in
SQLite, the master key is a file, the admin token is an env var —
none of them are part of the resolved configuration.

## `onegate import`

Migrates a legacy OmniRoute v3.8.52 `omniroute.json` into the OneGate
data dir. **Dry-run by default** — the plan is printed, nothing is
written; re-run with `--apply`.

| Flag | Type | Default | Notes |
|---|---|---|---|
| `-apply` | bool | `false` | write the plan (otherwise dry-run) |
| `-data-dir` | string | `.onegate` | import destination |

Exactly one positional argument: the legacy config file. Idempotent —
re-running an apply maps already-imported rows instead of duplicating.
The legacy install is only ever read.

```bash
onegate import omniroute.json                        # plan only
onegate import omniroute.json --apply --data-dir /var/lib/onegate
```

## `onegate import-keys`

Migrates legacy virtual keys and usage history (p7.data-import).
Legacy key hashes are unrecoverable by design (scrypt), so keys are
**re-minted**: new `ogk-…` raw keys are shown exactly once, under
`--apply`, written to `--keys-out` (mode 0600) or stdout. Revoked
stays revoked; usage history is imported with aggregate-preserving
rollups.

| Flag | Type | Default | Notes |
|---|---|---|---|
| `-apply` | bool | `false` | write the import (otherwise dry-run) |
| `-data-dir` | string | `.onegate` | import destination |
| `-usage-db` | string | — | legacy `omniroute.db` to read usage history from (read-only) |
| `-keys-out` | string | — | file for minted raw keys (mode 0600); stdout if unset |

```bash
onegate import-keys omniroute.json --apply \
  --usage-db legacy/omniroute.db --keys-out /root/new-keys.txt
```

## Environment variables

| Variable | Used by | Meaning |
|---|---|---|
| `ONEGATE_CONFIG` | serve, config | config file path (before cwd fallback) |
| `ONEGATE_HOST` / `ONEGATE_PORT` | serve, config | bind address / port |
| `ONEGATE_DATA_DIR` | serve, config | data directory |
| `ONEGATE_LOG_LEVEL` | serve, config | log level |
| `ONEGATE_HTTP_READ_HEADER_TIMEOUT_MS` | serve | slowloris guard (ms) |
| `ONEGATE_HTTP_READ_TIMEOUT_MS` | serve | whole-request read bound; 0 = off |
| `ONEGATE_HTTP_WRITE_TIMEOUT_MS` | serve | response write bound; 0 = off (keep off for streaming) |
| `ONEGATE_HTTP_IDLE_TIMEOUT_MS` | serve | keep-alive idle bound |
| `ONEGATE_ADMIN_TOKEN` | serve | gates `/metrics` and the admin API (also the dashboard's admin-token login fallback) |
| `ONEGATE_BINARY` | launcher | run this binary directly; no download, no cache |
| `ONEGATE_VERSION` | launcher | fetch a different release |
| `ONEGATE_DOWNLOAD_ROOT` | launcher | mirror/artifact server (GitHub Releases by default) |
| `ONEGATE_LAUNCHER_CACHE` | launcher | cache dir (default `~/.onegate/launcher`) |
| `ONEGATE_REQUIRE_EMBED` | CI/release | refuse placeholder-dashboard builds (`=1`) |

## Version

`onegate -version` prints one line:

```
v1.0.0 (commit 1a2b3c4, built 2026-10-10T00:00:00Z)
```

Version, commit and build date are injected at link time
(`make build`, `scripts/release/build.sh`); development builds report
`0.0.0-dev`.
