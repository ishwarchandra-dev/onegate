#!/usr/bin/env bash
# p8.fuzzing: short fuzz bursts across every fuzz target — the CI smoke
# mode. Sustained campaigns run locally with scripts/fuzz_campaign.sh.
#
# Usage: scripts/fuzz_smoke.sh [seconds-per-target]   (default 10)
set -euo pipefail

DUR="${1:-10s}"

# package:target pairs — keep in sync with the registry in
# docs/reports/fuzzing.md.
TARGETS=(
  "internal/protocol/sse:FuzzSplitFrames"
  "internal/protocol/openai:FuzzDecodeRequest"
  "internal/protocol/openai:FuzzDecodeResponse"
  "internal/protocol/openai:FuzzDecodeError"
  "internal/protocol/openai:FuzzStreamDecoder"
  "internal/protocol/anthropic:FuzzDecodeRequest"
  "internal/protocol/anthropic:FuzzDecodeResponse"
  "internal/protocol/anthropic:FuzzDecodeError"
  "internal/protocol/anthropic:FuzzStreamDecoder"
  "internal/protocol/gemini:FuzzDecodeRequest"
  "internal/protocol/gemini:FuzzDecodeResponse"
  "internal/protocol/gemini:FuzzDecodeError"
  "internal/protocol/gemini:FuzzStreamDecoder"
  "internal/config:FuzzConfigFile"
  "internal/importer:FuzzLegacyParseAndPlan"
)

fail=0
for pair in "${TARGETS[@]}"; do
  pkg="${pair%%:*}"
  tgt="${pair##*:}"
  echo "=== ${pkg} ${tgt} (${DUR}) ==="
  if ! go test "${pkg}/" -fuzz "^${tgt}$" -fuzztime "${DUR}" 1>/dev/null; then
    echo "FAIL: ${pkg} ${tgt}" >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "fuzz smoke: FAILURES detected" >&2
  exit 1
fi
echo "fuzz smoke: all ${#TARGETS[@]} targets clean"
