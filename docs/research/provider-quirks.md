# Provider Protocol Quirks

Research notes from the Phase 2 protocol work (node p2.protocol-docs,
owner: research-analyst). Each entry carries a **claim**, a **citation**,
and a **confidence** level; adapter code references the quirk ids in
comments (enforced by `internal/protocol/conformance` TestQuirkDocAnchors,
which verifies the code↔doc anchoring in both directions).

Confidence key: **high** = verified against the live API shape during
adapter golden-fixture work; **medium** = documented behavior, fixture
synthesized; **low** = community-reported, not yet reproduced.

---

## OpenAI Chat Completions

### quirk:openai-empty-arguments — tool-call deltas open with `arguments:""`

- **Claim.** The first streaming `tool_calls` delta for a call carries
  `id`, `type`, `function.name`, and an explicit empty-string
  `function.arguments`; subsequent fragments append to `arguments`. An
  encoder that omits empty strings still parses, but deviates from the
  byte shape clients expect.
- **Where.** `internal/protocol/openai/types.go` (wireFnArg.Arguments has
  no omitempty), `stream.go` block_start encoding.
- **Citation.** OpenAI API reference, Chat Streaming →
  <https://platform.openai.com/docs/api-reference/chat/stream>
- **Confidence.** high

### quirk:openai-content-null — assistant tool-call messages carry `content: null`

- **Claim.** When an assistant message contains only tool calls, OpenAI
  emits `"content": null` (not `""`, not omitted). OneGate emits null for
  calls-only assistant messages and accepts string | null | omitted on
  decode.
- **Where.** `internal/protocol/openai/request.go` encodeMessage.
- **Citation.** OpenAI API reference, Chat Completions request/response →
  <https://platform.openai.com/docs/api-reference/chat>
- **Confidence.** high

### quirk:openai-stream-usage — stream usage needs `stream_options.include_usage`

- **Claim.** Streaming responses carry no usage unless the request sets
  `stream_options: {"include_usage": true}`; the final chunk then arrives
  with an empty `choices` array and a `usage` object. The adapter decodes
  that shape; the proxy (Phase 3) must set the flag on provider requests.
- **Where.** `internal/protocol/openai/stream.go` (include_usage branch).
- **Citation.** OpenAI API reference, Chat Streaming →
  <https://platform.openai.com/docs/api-reference/chat/stream>
- **Confidence.** high

### quirk:openai-azure-quota-types — Azure quota errors use type "tokens"/"requests"

- **Claim.** Azure OpenAI reports quota exhaustion with
  `error.type` ∈ {"tokens", "requests"} rather than
  "rate_limit_error"; the error classifier folds these (and any code
  containing `rate_limit`) into the canonical rate-limit error so the
  fallback engine can retry.
- **Where.** `internal/protocol/openai/errors.go`.
- **Citation.** Microsoft Learn, Azure OpenAI quota and limits →
  <https://learn.microsoft.com/azure/ai-services/openai/quotas-limits>
- **Confidence.** medium

## Anthropic Messages

### quirk:anthropic-max-tokens-required — `max_tokens` is mandatory

- **Claim.** Unlike OpenAI/Gemini, the Messages API rejects requests
  without `max_tokens`. The adapter defaults to 4096 when a canonical
  request carries none.
- **Where.** `internal/protocol/anthropic/request.go` (defaultMaxTokens).
- **Citation.** Anthropic Messages API reference →
  <https://docs.anthropic.com/en/api/messages>
- **Confidence.** high

### quirk:anthropic-tool-choice-none — "none" is expressed by omitting tools

- **Claim.** The Messages API tool_choice accepts auto / any / tool.
  Encoding a canonical "none" choice by sending `{"type":"none"}` risks
  rejection on older API revisions; OneGate expresses "none" by omitting
  the tools list entirely (decode still accepts a native `none`).
- **Where.** `internal/protocol/anthropic/request.go` EncodeRequest.
- **Citation.** Anthropic tool use guide →
  <https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/overview>
- **Confidence.** medium

### quirk:anthropic-cache-ttl-split — cache creation reports 5m/1h buckets

- **Claim.** Prompt-cache usage reports
  `cache_creation_input_tokens` (and newer payloads an
  `ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens` split). The
  canonical schema collapses the TTL split into a single
  `cache_write_tokens` counter (ADR 004 exclusion); the base counter
  round-trips.
- **Where.** `internal/protocol/anthropic/response.go` DecodeUsage.
- **Citation.** Anthropic prompt caching →
  <https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching>
- **Confidence.** high

### quirk:anthropic-signature-delta — thinking signatures stream separately

- **Claim.** Extended-thinking blocks stream a `thinking_delta` followed
  by a separate `signature_delta` event; the signature must be replayed
  back to Anthropic with the thinking block in multi-turn history. The
  canonical event set carries both delta kinds.
- **Where.** `internal/protocol/anthropic/stream.go`, canonical
  `domain.StreamEvent.SignatureDelta`.
- **Citation.** Anthropic Messages streaming →
  <https://docs.anthropic.com/en/api/messages-streaming>
- **Confidence.** high

## Google Gemini

### quirk:gemini-no-call-ids — function calls carry names, not IDs

- **Claim.** `functionCall` parts have no stable call ID (newer API
  revisions add an optional `id`, which the adapter honors when present);
  `functionResponse` matches by name. OneGate synthesizes
  `call_{name}` IDs and resolves result names from the preceding
  assistant turn.
- **Where.** `internal/protocol/gemini/request.go` (callID, toolResultName),
  `response.go`.
- **Citation.** Gemini function calling →
  <https://ai.google.dev/gemini-api/docs/function-calling>
- **Confidence.** high

### quirk:gemini-parameters-field — two schema fields: `parameters` vs `parametersJsonSchema`

- **Claim.** `functionDeclarations` historically used `parameters` with a
  restricted schema dialect (uppercase enum values); newer revisions add
  `parametersJsonSchema` for full JSON Schema. The adapter decodes both
  and emits the widely-compatible `parameters` form.
- **Where.** `internal/protocol/gemini/types.go` (wireFunctionDecl).
- **Citation.** Gemini structured output / function declarations →
  <https://ai.google.dev/gemini-api/docs/structured-output>
- **Confidence.** medium

### quirk:gemini-overloaded-unavailable — overload surfaces as 503 UNAVAILABLE

- **Claim.** Capacity exhaustion ("The model is overloaded") arrives as
  HTTP 503 with `error.status` "UNAVAILABLE"; the adapter maps it to the
  canonical overloaded error so the fallback engine treats it as
  retryable.
- **Where.** `internal/protocol/gemini/errors.go`.
- **Citation.** Gemini API error troubleshooting →
  <https://ai.google.dev/gemini-api/docs/troubleshooting>
- **Confidence.** medium

### quirk:gemini-stop-after-function-call — STOP after a function call means tool use

- **Claim.** Gemini reports `finishReason: "STOP"` even when the turn ends
  with a function call (there is no dedicated tool-call reason); the
  adapter infers the canonical tool_calls finish whenever a function call
  was seen in the turn.
- **Where.** `internal/protocol/gemini/response.go` DecodeFinishReason,
  `stream.go` (sawFunctionCall).
- **Citation.** Gemini generateContent response →
  <https://ai.google.dev/api/generate-content>
- **Confidence.** high

## OpenAI-compatible provider flavors

### quirk:profile-openrouter-headers — OpenRouter wants attribution headers

- **Claim.** OpenRouter recommends (and for some rankings requires) the
  `HTTP-Referer` and `X-Title` headers for app attribution; they are a
  deployment-level ExtraHeaders decision, not protocol translation.
- **Where.** `internal/protocol/profile` (OpenRouter preset + test).
- **Citation.** OpenRouter API overview →
  <https://openrouter.ai/docs/api-reference/overview>
- **Confidence.** medium

### quirk:profile-groq-baseurl — Groq serves OpenAI under /openai/v1

- **Claim.** Groq's OpenAI-compatible surface lives at
  `https://api.groq.com/openai/v1` (path prefix, not host); the profile
  appends `/chat/completions` to the configured base.
- **Where.** `internal/protocol/profile` (groq preset).
- **Citation.** Groq OpenAI compatibility docs →
  <https://console.groq.com/docs/openai>
- **Confidence.** high

### quirk:profile-ollama-auth — Ollama needs no credentials

- **Claim.** Ollama's OpenAI-compatible endpoint
  (`http://host:11434/v1`) requires no Authorization header by default;
  the profile sends none (a configured key is still accepted if a
  deployment layers auth in front).
- **Where.** `internal/protocol/profile` (ollama preset).
- **Citation.** Ollama OpenAI compatibility →
  <https://github.com/ollama/ollama/blob/main/docs/openai.md>
- **Confidence.** high
