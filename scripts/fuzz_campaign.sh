#!/usr/bin/env bash
# p8.fuzzing: sustained local fuzz campaign — one long burst per target.
# This is the "fuzz clean for sustained runs" evidence mode; CI runs the
# short smoke (scripts/fuzz_smoke.sh) on every push.
#
# Usage: scripts/fuzz_campaign.sh [seconds-per-target]   (default 300)
#
# Any crasher found is written to testdata/fuzz/<Target>/ inside the
# package and fails the run; minimize with:
#   go test <pkg> -fuzz ^<Target>$ -fuzzminimizetime 60s
# then convert to a named regression test (see sse_test.go for the
# pattern) and fix the parser.
set -euo pipefail

DUR="${1:-300s}"

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

for pair in "${TARGETS[@]}"; do
  pkg="${pair%%:*}"
  tgt="${pair##*:}"
  echo "=== ${pkg} ${tgt} (${DUR}) ==="
  go test "${pkg}/" -fuzz "^${tgt}$" -fuzztime "${DUR}"
done
echo "campaign complete: all ${#TARGETS[@]} targets clean at ${DUR} each"
