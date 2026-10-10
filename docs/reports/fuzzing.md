# Fuzzing Campaign — p8.fuzzing

Status: **complete** · Owner: qa-engineer · Node: p8.fuzzing
Toolchain: Go native fuzzing (`go test -fuzz`), go1.26.9.

## Scope

Every untrusted-input parser in the gateway, per the node's acceptance
surface: protocol parsers (openai / anthropic / gemini: request,
response, error envelope, stream event machines), SSE framing, config
file parsing, and legacy import parsing (which also drives plan
building + report rendering — a mini integration fuzz per input).

## Target registry (15 targets)

| Package | Target | Surface | Invariants asserted |
|---|---|---|---|
| internal/protocol/sse | FuzzSplitFrames | SSE body → frames | determinism; no CR survives normalization (spec: CR/LF/CRLF all terminate lines) |
| internal/protocol/openai | FuzzDecodeRequest | wire request → canonical | decode-accepts ⇒ encode-succeeds (round trip) |
| internal/protocol/openai | FuzzDecodeResponse | wire response → canonical | round trip; usage never negative |
| internal/protocol/openai | FuzzDecodeError | wire error → GatewayError | envelope always valid JSON; status stays in HTTP error family for real upstream codes |
| internal/protocol/openai | FuzzStreamDecoder | chunk state machine | no panic whole-body and line-by-line; terminal states survive reuse |
| internal/protocol/anthropic | FuzzDecodeRequest | " | round trip |
| internal/protocol/anthropic | FuzzDecodeResponse | " | round trip; usage never negative |
| internal/protocol/anthropic | FuzzDecodeError | " | envelope JSON + status family |
| internal/protocol/anthropic | FuzzStreamDecoder | " | no panic; double-decode of terminal events |
| internal/protocol/gemini | FuzzDecodeRequest | " | round trip |
| internal/protocol/gemini | FuzzDecodeResponse | " | round trip; usage never negative |
| internal/protocol/gemini | FuzzDecodeError | " | envelope JSON + status family |
| internal/protocol/gemini | FuzzStreamDecoder | " | no panic; Finish() safe after any sequence |
| internal/config | FuzzConfigFile | file JSON → strict decode → merge → Validate | Validate-passing configs honor their own contract (port range, log level, slowloris guard) |
| internal/importer | FuzzLegacyParseAndPlan | omniroute.json → Parse → BuildPlan → Report | no panic for any plan; only validated providers planned as writes; skipped ones always fail validation |

## Campaign evidence (local, this node)

| Burst | Result |
|---|---|
| sse.FuzzSplitFrames 45s | **2,577,239 execs**, clean after F-1 fix |
| openai × 4 (20–45s each) | clean |
| anthropic × 4 (25s each) | clean |
| gemini × 4 (25s each) | clean |
| config.FuzzConfigFile 60s | clean |
| importer.FuzzLegacyParseAndPlan 60s | clean (thousands of full plan builds) |
| **post-fix parity replay** | **41/41 corpus cases pass** — proves the F-2 clamp is parity-neutral |

CI enforcement: `fuzz` job runs `scripts/fuzz_smoke.sh 10s` (15 × 10s
bursts ≈ 3 min) on every push/PR. Seed corpora are committed under each
package's `testdata/fuzz/<Target>/`; they run as plain unit tests in
the ordinary `go test` job, so any committed crasher (fixture) fails CI
until fixed. Sustained local campaigns: `scripts/fuzz_campaign.sh [s]`
(default 300s/target).

## Findings & fixes (crashes → fixtures + fixes)

### F-1 (fixed): SSE parser mis-handled lone-CR line endings

- **Found by**: FuzzSplitFrames, minimized input `data:\r` (committed
  as `internal/protocol/sse/testdata/fuzz/FuzzSplitFrames/1e08851f22a725b1`).
- **Bug**: only `\r\n` was normalized; a body using bare-CR line
  endings (legal per the WHATWG SSE spec — CR, LF, CRLF all terminate
  lines) leaked `\r` into frame data and merged what should be separate
  frames.
- **Fix**: `sse.SplitFrames` now normalizes lone `\r` to `\n` after
  CRLF folding. Regression tests: `sse_test.go`
  (TestSplitFramesLoneCRTerminators, TestSplitFramesMixedTerminators,
  TestSplitFramesBareCRIgnoredAsLineFeed — the last is the exact
  minimized crasher).

### F-2 (fixed): negative token counts accepted from upstream usage

- **Found by**: FuzzDecodeResponse seed — `{"usage":{"prompt_tokens":-5}}`
  produced a canonical usage with negative totals after derivation.
- **Impact**: a hostile or buggy openai-compat upstream could deflate
  spend accounting and corrupt usage rollups (quota paths were already
  guarded by `> 0` checks; storage/rollups/cost analytics were not).
- **Fix**: `domain.TokenUsage.Sanitize()` clamps every token counter at
  zero; wired into all three adapters' `DecodeUsage` (openai,
  anthropic, gemini). Parity replay re-run: 41/41 — no legitimate
  payload in the corpus carries negative counts, so the clamp is
  behavior-neutral for real traffic.

### Observations (no action)

- `sse.Format` does not split multi-line frame data into multiple
  `data:` lines; re-parsing its output loses the join. It has **no
  callers** outside tests (the stream writer assembles frames
  directly), so this stays a documented quirk rather than a fix.
- Out-of-family statuses (0/999) into `DecodeError` pass through; the
  fuzz targets only assert the contract for statuses `net/http` can
  actually produce.

— qa-engineer, p8.fuzzing
