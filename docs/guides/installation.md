# Installation

Three supported ways to run OneGate. All produce the same single
binary; pick what fits your environment. Every release carries
`SHA256SUMS` and a `provenance.json` (version, commit, toolchain) —
verify before you trust.

## npm launcher (`npx onegate`)

The `onegate` npm package is a zero-dependency launcher: it downloads
the official binary for your platform from the GitHub Release,
verifies it against the release `SHA256SUMS`, caches it under
`~/.onegate/launcher/v<version>/<os>_<arch>/`, and runs it. Later runs
start from the cache with no network.

```bash
npx onegate --version          # first run: downloads + verifies (~10 MB)
npx onegate serve              # run the gateway
npm install -g onegate         # or install it permanently
```

Supported platforms: linux/darwin/windows × amd64/arm64. Requires
Node.js ≥ 18 and `tar` on `PATH` (default everywhere; Windows 10
1803+ ships `tar.exe`).

Environment knobs (`ONEGATE_BINARY`, `ONEGATE_VERSION`,
`ONEGATE_DOWNLOAD_ROOT`, `ONEGATE_LAUNCHER_CACHE`), exit codes, and
the security model are documented in the
[launcher README](../../launcher/README.md) and the
[CLI reference](../cli.md#exit-codes).

## Binary from a GitHub Release

```bash
version=1.0.0                                   # pick a release tag
os_arch=linux_amd64                             # or darwin_arm64, windows_amd64 …
curl -fLO "https://github.com/ishwarchandra-dev/onegate/releases/download/v${version}/onegate_v${version}_${os_arch}.tar.gz"
curl -fLO "https://github.com/ishwarchandra-dev/onegate/releases/download/v${version}/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS   # must say OK
tar -xzf "onegate_v${version}_${os_arch}.tar.gz"   # contains one file: onegate_v...
install -m 755 "onegate_v${version}_${os_arch}" /usr/local/bin/onegate
onegate --version
```

Windows archives are `.zip`; `tar -xf` extracts them (bsdtar) or use
any unzip tool. `windows/arm64` builds are statically validated
(ELF/Mach-O/PE checks in the release pipeline); all other targets are
runtime-smoked on native runners.

## Docker

```bash
docker run -d --name onegate \
  -p 7420:7420 \
  -v onegate-data:/data \
  ghcr.io/ishwarchandra-dev/onegate:latest
```

The image is distroless (no shell, no package manager), runs as
non-root (uid 65532), and expects the data dir at `/data`. Pin by
version in production: `ghcr.io/ishwarchandra-dev/onegate:1.0.0`.

To configure inside a container: pass flags after the image name
(`-port`, `-log-level`, …), or mount a config file and set
`ONEGATE_CONFIG=/data/onegate.json`, or set `ONEGATE_*` env vars with
`-e`. See the [CLI reference](../cli.md) for the full precedence chain.

## Building from source

Requires Go 1.25+ (toolchain pinned in `go.mod`) and Bun 1.1+ for the
dashboard:

```bash
git clone https://github.com/ishwarchandra-dev/onegate
cd onegate
make web        # build the dashboard (embedded into the binary)
make build      # -> bin/onegate
./bin/onegate --version
```

`make build` without `make web` embeds the committed placeholder and
the gateway warns about it at startup (`make web && make build` is the
single-binary pipeline). Release-grade cross-compilation:
`scripts/release/build.sh`.

## What lands where

| Path | Contents |
|---|---|
| `~/.onegate/` (or `-data-dir`) | `onegate.db` (SQLite, WAL), `master.key` (AES-GCM master secret) |
| `~/.onegate/launcher/` | launcher cache (npm install only) |
| `./onegate.json` (or `-config`) | optional config file; see [operations](operations.md#configuration) |

Back up the data directory and you back up everything (providers,
keys, models, rules, usage history) — see
[operations](operations.md#backups).
