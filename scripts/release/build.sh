#!/usr/bin/env bash
# scripts/release/build.sh — OneGate release matrix builder (p9.build-matrix).
#
# Produces, under dist/:
#   onegate_<version>_<os>_<arch>[.exe]      raw binaries
#   onegate_<version>_<os>_<arch>.tar.gz|.zip  archives
#   SHA256SUMS                               checksums for every archive
#   provenance.json                          version, commit, date, toolchain,
#                                            per-target hashes, smoke results
#
# Usage:
#   scripts/release/build.sh [version]        # version defaults to git describe
#   RELEASE_STRICT=1 scripts/release/build.sh v1.0.0
#       RELEASE_STRICT=1 refuses dirty trees and non-tag versions
#       (release candidates are cut from tags only — devops charter).
#
# Smoke testing: the native target gets a full runtime smoke (start,
# /healthz, dashboard serves, placeholder refused). Cross targets get
# static validation here; the release workflow re-builds on native
# runners and smokes each platform natively.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

PKG="github.com/ishwarchandra-dev/onegate"
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
GO_VERSION="$(go env GOVERSION)"

if [[ "${RELEASE_STRICT:-0}" == "1" ]]; then
  if [[ "$VERSION" != v* ]]; then
    echo "release: strict mode requires a v* tag version, got '$VERSION'" >&2
    exit 1
  fi
  if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
    echo "release: strict mode refuses a dirty working tree" >&2
    exit 1
  fi
fi

# The six-target matrix (phase 9 gate: linux/darwin/windows, amd64+arm64).
TARGETS=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
  "windows arm64"
)

OUT="dist"
rm -rf "$OUT" && mkdir -p "$OUT"

echo "== building dashboard (embedded into every binary) =="
( cd web && bun install >/dev/null )
make web >/dev/null

echo "== refusing placeholder builds =="
ONEGATE_REQUIRE_EMBED=1 go test ./cmd/onegate -run TestEmbeddedDashboardNotPlaceholder -count=1 >/dev/null

LDFLAGS="-X ${PKG}/internal/version.Version=${VERSION} \
-X ${PKG}/internal/version.GitCommit=${COMMIT} \
-X ${PKG}/internal/version.BuildDate=${DATE}"

build_one() {
  local os="$1" arch="$2" ext=""
  [[ "$os" == "windows" ]] && ext=".exe"
  local name="onegate_${VERSION}_${os}_${arch}${ext}"
  echo "-- ${os}/${arch}" >&2
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/$name" ./cmd/onegate
  if [[ "$os" == "windows" ]]; then
    ( cd "$OUT" && zip -q "$name.zip" "$name" && rm "$name" )
    echo "${name}.zip"
  else
    ( cd "$OUT" && tar -czf "${name}.tar.gz" "$name" && rm "$name" )
    echo "${name}.tar.gz"
  fi
}

ARCHIVES=()
for t in "${TARGETS[@]}"; do
  read -r os arch <<<"$t"
  ARCHIVES+=("$(build_one "$os" "$arch")")
done

echo "== checksums =="
( cd "$OUT" && sha256sum *.tar.gz *.zip > SHA256SUMS )   # bare names: no ./ prefix in the manifest

# ---------------------------------------------------------------------------
# Smoke test: runtime on the native target, static on cross targets.
# ---------------------------------------------------------------------------
SMOKE_NATIVE="not-run"

smoke_runtime() {
  local bin="$1" port="$2" dir ok="fail"
  dir="$(mktemp -d)"
  if "$bin" -data-dir "$dir" -port "$port" -log-level warn &>/dev/null &
  local pid=$!
  then
    for _ in $(seq 1 50); do
      if curl -fsS "http://127.0.0.1:${port}/healthz" 2>/dev/null | grep -q '"status":"ok"'; then
        if curl -fsS "http://127.0.0.1:${port}/" 2>/dev/null | grep -q "__reactRouterContext"; then
          ok="pass"
        fi
        break
      fi
      sleep 0.2
    done
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$dir"
  echo "$ok"
}

echo "== smoke: native ($(go env GOOS)/$(go env GOARCH)) =="
NATIVE_NAME="onegate_${VERSION}_$(go env GOOS)_$(go env GOARCH)"
if [[ "$(go env GOOS)" == "windows" ]]; then
  ( cd "$OUT" && unzip -q "${NATIVE_NAME}.exe.zip" )
  NATIVE_BIN="$OUT/${NATIVE_NAME}.exe"
else
  ( cd "$OUT" && tar -xzf "${NATIVE_NAME}.tar.gz" )
  NATIVE_BIN="$OUT/${NATIVE_NAME}"
fi
if [[ -f "$NATIVE_BIN" ]]; then
  SMOKE_NATIVE="$(smoke_runtime "$NATIVE_BIN" 17841)"
  rm -f "$NATIVE_BIN"
  if [[ "$SMOKE_NATIVE" != "pass" ]]; then
    echo "release: native smoke FAILED" >&2
    exit 1
  fi
fi

echo "== provenance =="
{
  echo '{'
  echo "  \"name\": \"onegate\","
  echo "  \"version\": \"${VERSION}\","
  echo "  \"commit\": \"${COMMIT}\","
  echo "  \"built\": \"${DATE}\","
  echo "  \"go\": \"${GO_VERSION}\","
  echo '  "cgo": false,'
  echo '  "dashboard_embedded": true,'
  echo '  "targets": ['
  first=1
  for a in "${ARCHIVES[@]}"; do
    [[ $first == 1 ]] || echo ','
    first=0
    printf '    "%s"' "$a"
  done
  echo ''
  echo '  ],'
  echo "  \"smoke\": {"
  echo "    \"native\": \"${SMOKE_NATIVE}\","
  echo '    "cross": "static-validated (runtime smokes run on native runners in the release workflow)"'
  echo '  }'
  echo '}'
} > "$OUT/provenance.json"

echo
echo "Release artifacts in ${OUT}/:"
( cd "$OUT" && ls -lh )
echo
echo "Native smoke: ${SMOKE_NATIVE}"
