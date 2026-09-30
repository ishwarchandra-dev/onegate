# Phase 2 Gate Report — Protocol Adapters & Canonical Types

- Graph: `tasks/phase-2.protocols.graph.yaml`
- Verdict: **PASSED** (all 8 nodes done, all gate criteria verified)
- Date: 2026-09-30

## Gate criteria

| Criterion | Verification | Result |
|---|---|---|
| OpenAI, Anthropic, Gemini adapters round-trip golden fixtures | `internal/protocol/{openai,anthropic,gemini}` golden tests: decode→encode byte-stable under JSON normalization for requests, responses, and SSE streams | PASS |
| Streaming translation per protocol verified event-by-event | `TestStreamEventByEvent` in each adapter pins exact canonical event sequences (OpenAI tool-call deltas + `[DONE]`, Anthropic named-event order + thinking signatures, Gemini alt=sse chunks); cross-protocol assembled-level conformance in `TestStreamConformance` | PASS |
| Protocol packages have zero imports from proxy/routing/storage | `internal/protocol/protocol_test.go` TestLayering parses every import in the tree and enforces the allowlist (stdlib, internal/domain, siblings); the tree has no proxy/routing/storage imports | PASS |

## Node acceptance evidence

| Node | Acceptance | Evidence |
|---|---|---|
| p2.canonical-schema | Tool calls, multimodal blocks, logit biases expressible; field-mapping tables documented | `internal/domain/protocol_test.go` TestRequestSupersetExpressibility; `docs/protocol-mappings.md` (per-protocol tables + lossy summary) |
| p2.openai-adapter | Golden round-trips byte-stable; error envelope exact | `openai` TestRequestRoundTrip/TestResponseRoundTrip/TestStreamRoundTrip/TestErrors (incl. tool-call deltas, usage, `[DONE]`) |
| p2.anthropic-adapter | message_start/content_block_delta/message_delta order preserved; error types mapped | `anthropic` TestStreamEventByEvent, TestErrors (overloaded_error etc.) |
| p2.gemini-adapter | Safety blocks → canonical finish reasons; UsageMetadata incl. thoughts tokens | `gemini` TestResponseDecodedShape (blocked → safety), usage translation tests |
| p2.openai-compat | Profile matrix tests against mock servers | `profile` TestProfileMatrix (openai/openrouter/groq/mistral/vllm/ollama), TestAuthTemplating, TestURLJoining |
| p2.cross-protocol-tests | Same semantic input → semantically equal canonical forms; lossy mappings enumerated | `conformance` TestProtocolRoundTripConformance, TestCrossProtocolPairs (9 pairs), TestLossyTableDocumented; `conformance/lossy.go` diff table |
| p2.protocol-docs | Every quirk referenced from adapter code comments | `docs/research/provider-quirks.md` (claim + citation + confidence); TestQuirkDocAnchors enforces code↔doc sync both ways |
| p2.gate | All gate criteria verified; layering audit passes | This report + TestLayering |

## Verification commands

```
go vet ./...                          # clean
go test ./... -race -count=1          # all packages pass
python3 scripts/graph_status.py --check   # 10 graphs, 86 nodes, DAG valid
```

## Key decisions this phase

- **ADR 004** — canonical schema at the edge; strict-canonical with
  documented drops (no opaque passthrough).
- Canonical event taxonomy modeled on Anthropic SSE (most expressive),
  extended with `ThinkingDelta` / `SignatureDelta` so reasoning streams
  round-trip across all three protocols.
- `ToolResult.Name` added to the canonical schema: Gemini functionResponse
  requires the function name; OpenAI/Anthropic decoders resolve ID→name
  from the preceding assistant turn.
- Gemini's path-carried model and endpoint-carried stream flag are
  documented losses (`conformance/lossy.go`); the proxy supplies them from
  routing context in Phase 3.

## Known gaps deferred by design

- Logprob response payloads dropped (ADR 004 exclusions; revisit in
  Phase 7 parity audit `p7.parity-checklist`).
- Provider real-world tolerance beyond golden fixtures (rate-limit
  headers, Azure style) is decode-best-effort; the Phase 7 compatibility
  audit will replay OmniRoute v3.8.52 fixtures.
