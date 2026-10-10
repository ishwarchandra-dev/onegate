# onegate (npm launcher)

`npx onegate` runs the OneGate LLM gateway — a single static binary that
proxies OpenAI / Anthropic / Gemini behind one endpoint, with virtual
keys, routing, fallback, quotas, usage analytics, and an embedded
dashboard.

This npm package is a **launcher**. It contains no binary and has zero
runtime dependencies. On first run it downloads the official gateway
binary for your platform from the OneGate GitHub Release, verifies it
against the release's `SHA256SUMS`, installs it into a local cache, and
executes it. Every later run starts straight from the cache with no
network access.

```
npx onegate                    # start the gateway (dashboard on :7420)
npx onegate --version           # print the gateway version
npx onegate -- --help           # pass flags through to the binary
```

## How it works

1. **Platform resolution** — `process.platform`/`process.arch` map onto
   the release matrix: linux/darwin/windows × amd64/arm64 (6 targets).
   Anything else fails with exit code 1 and the supported list.
2. **Version pinning** — the package version equals the gateway version
   it fetches (this package at 1.0.0 fetches release `v1.0.0`), so
   `npx onegate@1.0.0` and a locally cached `v1.0.0` always agree.
3. **Download** — from
   `https://github.com/ishwarchandra-dev/onegate/releases/download/v<version>/`,
   following redirects (https-only for the default root), with a 30 s
   stall watchdog.
4. **Verify (the gate)** — the archive's SHA-256 must match the entry in
   the release's `SHA256SUMS` (GNU `sha256sum` format). A missing entry,
   a malformed manifest, or a single flipped byte all mean the same
   thing: **the download is corrupt and refuses to run** (exit 3). The
   launcher never executes anything it could not verify.
5. **Extract** — the system `tar` (GNU tar on Linux; bsdtar on macOS and
   on Windows 10 1803+, where it also reads the `.zip` archives) pulls
   exactly one named member out of the archive.
6. **Place** — the binary lands in
   `~/.onegate/launcher/v<version>/<os>_<arch>/onegate` via an atomic
   rename; concurrent runs race safely (EEXIST → first installer wins).
   Failures delete their staging directory; no partial state survives.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `ONEGATE_BINARY` | — | Run this binary directly; no download, no cache. For system installs, offline machines, and distro packagers. |
| `ONEGATE_VERSION` | package version | Fetch a different release (e.g. `1.0.1`). The cache is version-scoped, so switching is safe. |
| `ONEGATE_DOWNLOAD_ROOT` | GitHub Releases URL | Mirror or artifact server. Layout must mirror the release: `<root>/v<version>/<archive>` + `SHA256SUMS`. `http://` roots are allowed only when explicitly configured this way. |
| `ONEGATE_LAUNCHER_CACHE` | `~/.onegate/launcher` | Where verified binaries live. |

## Exit codes

The child process's exit code is always forwarded verbatim (signals
become `128+N`). The launcher's own failures:

| Code | Meaning |
|---|---|
| 1 | unsupported platform, bad env, or the child could not be spawned |
| 2 | network failure (unreachable root, HTTP ≠ 200, stall) |
| 3 | verification failure — corrupt or tampered download, refused |
| 4 | extraction or installation failure |

## Requirements

- Node.js ≥ 18 (any newer version is fine; the launcher only uses
  Node's standard library).
- `tar` on `PATH` — present by default on every supported platform
  (Windows 10 1803+ ships `tar.exe`).

## Uninstall / offline

The launcher is stateless: `npm uninstall onegate` (or stop using `npx`)
plus `rm -rf ~/.onegate/launcher` removes every trace. For air-gapped
machines, install a release archive manually and set `ONEGATE_BINARY`,
or run the binary directly — the launcher is a convenience, never a
dependency.

## Development

```
cd launcher
npm test        # node:test suite: platform mapping, sums parsing,
                # install/e2e against a local fixture release server
```

Tests run against a fixture release directory (real archives, real
SHA256SUMS) served over a loopback HTTP server — no network access
needed. The release workflow additionally smoke-tests `npx onegate`
against the real published GitHub Release on every hosted platform.

License: MIT (same as the gateway). Source:
<https://github.com/ishwarchandra-dev/onegate> (`launcher/`).
