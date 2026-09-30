# Protocol Field Mappings

Canonical schema: `internal/domain/protocol.go` (ADR 004). This document
is the normative per-protocol mapping table for the three adapters. The
conformance suite (`internal/protocol/conformance`) enforces the
round-trippable rows; the "Lossy" column is mirrored in
`internal/protocol/conformance/lossy.go` as machine-readable data.

Notation: `canonical.field ⇄ native.field` — bidirectional, fidelity
preserving. `canonical.field → native.field` — one-way fold. `✗` — no
counterpart (dropped or synthesized).

## 1. OpenAI Chat Completions (`internal/protocol/openai`)

### 1.1 Request

| Canonical | OpenAI | Notes / Lossy |
|---|---|---|
| `model` | `model` | |
| `messages[].role=system` | `messages[0].role=system` | Anthropic-bound system text is folded back into a leading system message |
| `messages[].role` | `messages[].role` | `tool` role synthesized from canonical tool_result blocks |
| `messages[].content` (text blocks) | `messages[].content` (string) | single-text-block messages use the compact string form |
| `messages[].content` (image blocks) | `content[]{type:"image_url"}` | `base64` → `data:` URL; `detail` carried |
| `messages[].name` | `messages[].name` | |
| `tool_call` block | `assistant.tool_calls[]{id,type:"function",function{name,arguments}}` | arguments string preserved verbatim |
| `tool_result` block | `role:"tool"` message + `tool_call_id` | `is_error` folds into content text (✗ no flag) |
| `thinking` block | ✗ | dropped for OpenAI providers |
| `tools[]` | `tools[]{type:"function",function{name,description,parameters}}` | |
| `tools[].input_schema` | `function.parameters` | raw JSON preserved |
| `tool_choice.mode=auto/none/required` | `tool_choice:"auto"/"none"/"required"` | |
| `tool_choice.mode=named` | `tool_choice:{type:"function",function:{name}}` | |
| `stream` | `stream` | |
| `sampling.max_tokens` | `max_tokens` | |
| `sampling.temperature` | `temperature` | nil → omitted (provider default) |
| `sampling.top_p` | `top_p` | |
| `sampling.top_k` | ✗ | dropped (no OpenAI equivalent) |
| `sampling.stop_sequences` | `stop` | |
| `sampling.seed` | `seed` | |
| `sampling.logit_bias` | `logit_bias` | |
| `sampling.logprobs` / `top_logprobs` | `logprobs` / `top_logprobs` | request flags carried; response payloads dropped (ADR 004) |
| `sampling.response_format` | `response_format` | `json_schema` → `{type:"json_schema",json_schema:{...}}` |
| `user` | `user` | |
| ✗ | `n`, `frequency_penalty`, `presence_penalty`, `store`, `metadata` | not expressible canonically (ADR 004 exclusions) |

### 1.2 Response

| Canonical | OpenAI | Notes / Lossy |
|---|---|---|
| `id` | `id` | |
| `model` | `model` | canonical ID substituted at encode time |
| `role` | `choices[0].message.role` | |
| `content` text blocks | `choices[0].message.content` | multi-block → joined string (Lossy: block boundaries) |
| `content` tool_call blocks | `choices[0].message.tool_calls[]` | |
| `content` thinking blocks | `choices[0].message.reasoning_content` (DeepSeek-style) | best-effort; standard OpenAI has no field |
| `finish_reason=stop` | `finish_reason:"stop"` | |
| `finish_reason=length` | `finish_reason:"length"` | |
| `finish_reason=tool_calls` | `finish_reason:"tool_calls"` | |
| `finish_reason=content_filter` | `finish_reason:"content_filter"` | |
| `finish_reason=safety/recitation` | → `"content_filter"` | native kept in `provider.native_finish_reason` |
| `finish_reason=refusal` | → `"stop"` | refusal text stays a text block; native kept in `provider.native_finish_reason` |
| `message.refusal` (wire→canonical) | text block + `finish_reason=refusal` | refusal string appended as a text block; stop folds to refusal |
| `usage.input_tokens` | `usage.prompt_tokens` | |
| `usage.output_tokens` | `usage.completion_tokens` | |
| `usage.total_tokens` | `usage.total_tokens` | derived when 0 |
| `usage.reasoning_tokens` | `usage.completion_tokens_details.reasoning_tokens` | |
| `usage.cache_read_tokens` | `usage.prompt_tokens_details.cached_tokens` | |
| `usage.cache_write_tokens` | ✗ | |
| `created_ms` | `created` (s → ms) | |

### 1.3 Streaming

| Canonical | OpenAI chunk | Notes |
|---|---|---|
| `message_start` | first chunk (role delta) | synthesized from `choices[0].delta.role` |
| `block_start` (text) | synthesized | OpenAI has no block framing; index 0 |
| `block_delta` (text) | `choices[0].delta.content` | |
| `block_start` (tool_call) | `choices[0].delta.tool_calls[i]` with `id`+`function.name` | index from `tool_calls[i].index` |
| `block_delta` (args) | `choices[0].delta.tool_calls[i].function.arguments` | |
| `block_stop` | synthesized | |
| `message_delta` | `choices[0].finish_reason` + final chunk `usage` (with `stream_options.include_usage`) | |
| `message_stop` | `data: [DONE]` | |
| `ping` | keep-alive comment lines | |
| `error` | non-200 SSE or mid-stream error JSON | |

### 1.4 Errors

| Canonical | OpenAI | 
|---|---|
| `status` | HTTP status |
| `type=invalid_request_error` | `error.type:"invalid_request_error"` |
| `type=rate_limit_error` | `error.type:"rate_limit_error"` (429) or `type:"requests"/"tokens"` (Azure quota) |
| `type=authentication_error` | `error.code:"invalid_api_key"` (401) |
| `type=not_found_error` | `error.code:"model_not_found"` (404) |
| `code` | `error.code` |
| `param` | `error.param` |
| `message` | `error.message` |

## 2. Anthropic Messages (`internal/protocol/anthropic`)

### 2.1 Request

| Canonical | Anthropic | Notes / Lossy |
|---|---|---|
| `model` | `model` | |
| `messages[].role=system` | top-level `system` (string) | Lossy: multiple system messages concatenated with `\n\n`; interleaving order lost |
| `messages[].role=user/assistant` | `messages[]` | |
| `messages[].content` | `content[]` or string | |
| text block | `{type:"text",text}` | |
| image block | `{type:"image",source:{type:"base64",media_type,data}}` | `url` → ✗ (Anthropic needs base64; URL images fetched? No — dropped with mapping note) |
| tool_call block | `{type:"tool_use",id,name,input}` | arguments string → parsed object (Lossy: key order) |
| tool_result block | `{type:"tool_result",tool_use_id,content,is_error}` | |
| thinking block | `{type:"thinking",thinking,signature}` | |
| `tools[]` | `tools[]{name,description,input_schema}` | |
| `tool_choice.mode=auto/required(→any)/named(→tool)` | `tool_choice:{type:"auto"/"any"/"tool"}` | `none` → tools + tool_choice omitted entirely [quirk:anthropic-tool-choice-none]; decode accepts a native `{type:"none"}` |
| `tool_choice.mode=named` | `tool_choice:{type:"tool",name}` | |
| `stream` | `stream` | |
| `sampling.max_tokens` | `max_tokens` | required natively; adapter defaults when 0 |
| `sampling.temperature/top_p/stop_sequences` | same names | |
| `sampling.top_k` | `top_k` | |
| `sampling.seed/logit_bias/logprobs/response_format` | ✗ | dropped (documented) |
| `user` | `metadata.user_id` | |

### 2.2 Response

| Canonical | Anthropic | Notes / Lossy |
|---|---|---|
| `id` | `id` | |
| `model` | `model` | |
| `role` | `role` ("assistant") | |
| `content` blocks | `content[]` (text / tool_use / thinking) | |
| `finish_reason=stop` | `stop_reason:"end_turn"` | |
| `finish_reason=length` | `stop_reason:"max_tokens"` | |
| `finish_reason=tool_calls` | `stop_reason:"tool_use"` | |
| `finish_reason=refusal` | `stop_reason:"refusal"` | |
| `finish_reason=safety` | `stop_reason` absent + `stop_sequence`? | refusal → safety when no stop_reason |
| `usage.input_tokens` | `usage.input_tokens` | |
| `usage.output_tokens` | `usage.output_tokens` | |
| `usage.cache_read_tokens` | `usage.cache_read_input_tokens` | |
| `usage.cache_write_tokens` | `usage.cache_creation_input_tokens` | Lossy: 5m/1h split collapsed (ADR 004) |
| `usage.total_tokens` | derived | |
| `created_ms` | ✗ (no created field) | gateway stamp |

### 2.3 Streaming (native event order preserved)

| Canonical | Anthropic SSE | Notes |
|---|---|---|
| `message_start` | `event: message_start` | carries `message` skeleton + usage |
| `block_start` | `event: content_block_start` | `content_block` skeleton |
| `block_delta` text | `event: content_block_delta` `delta:{type:"text_delta",text}` | |
| `block_delta` thinking | `delta:{type:"thinking_delta",thinking}` | |
| `block_delta` args | `delta:{type:"input_json_delta",partial_json}` | |
| `block_stop` | `event: content_block_stop` | |
| `message_delta` | `event: message_delta` | `delta.stop_reason` + `usage` |
| `message_stop` | `event: message_stop` | |
| `ping` | `event: ping` | |

### 2.4 Errors

| Canonical | Anthropic |
|---|---|
| `status` | HTTP status |
| `type` | `error.type` (taxonomies match by design: invalid_request_error, authentication_error, permission_error, not_found_error, rate_limit_error, overloaded_error, api_error) |
| `message` | `error.message` |

## 3. Google Gemini generateContent (`internal/protocol/gemini`)

### 3.1 Request

| Canonical | Gemini | Notes / Lossy |
|---|---|---|
| `model` | path segment `models/{model}:generateContent` | |
| `messages[].role=system` | `systemInstruction.parts[]` | folded |
| `messages[].role=user` | `contents[]{role:"user",parts[]}` | |
| `messages[].role=assistant` | `contents[]{role:"model",parts[]}` | |
| text block | `parts[]{text}` | |
| image block | `parts[]{inlineData:{mimeType,data}}` | URL → ✗ |
| tool_call block | `parts[]{functionCall:{name,args}}` | ✗ no call ID — adapter synthesizes `call_{name}_{seq}` deterministically |
| tool_result block | `parts[]{functionResponse:{name,response}}` | matched by name to last call (Lossy: parallel same-name calls) |
| thinking block | ✗ / `parts[]{thought:true}` (v1.5+ summaries) | best-effort passthrough |
| `tools[]` | `tools[]{functionDeclarations[]}` | JSON Schema → cleaned (✗ some keywords unsupported; see quirks doc) |
| `tool_choice.mode=auto/none` | `toolConfig.functionCallingConfig.mode:"AUTO"/"NONE"` | |
| `tool_choice.mode=required` | `mode:"ANY"` | |
| `tool_choice.mode=named` | `mode:"ANY"` + `allowedFunctionNames:[name]` | |
| `sampling.max_tokens` | `generationConfig.maxOutputTokens` | |
| `sampling.temperature/top_p` | `generationConfig.temperature/topP` | |
| `sampling.top_k` | `generationConfig.topK` | |
| `sampling.stop_sequences` | `generationConfig.stopSequences` | |
| `sampling.seed` | `generationConfig.seed` | |
| `sampling.response_format=json_object` | `generationConfig.responseMimeType:"application/json"` | |
| `sampling.logit_bias/logprobs` | ✗ | dropped |
| `user` | ✗ | |

### 3.2 Response

| Canonical | Gemini | Notes / Lossy |
|---|---|---|
| `id` | `responseId` | |
| `model` | `modelVersion` | |
| `content` text blocks | `candidates[0].content.parts[].text` | |
| `content` tool_call blocks | `candidates[0].content.parts[].functionCall` | |
| `content` thinking blocks | `parts[]{thought:true}` | |
| `finish_reason=stop` | `candidates[0].finishReason:"STOP"` | |
| `finish_reason=length` | `"MAX_TOKENS"` | |
| `finish_reason=safety` | `"SAFETY"`,`"BLOCKLIST"`,`"PROHIBITED_CONTENT"`,`"SPII"`,`"IMAGE_SAFETY"` | |
| `finish_reason=recitation` | `"RECITATION"` | |
| `finish_reason=tool_calls` | `"STOP"` when functionCall present | inferred |
| `finish_reason=content_filter` | `"OTHER"` + safety block present | |
| `finish_reason=other` | `"OTHER"`,`"MALFORMED_FUNCTION_CALL"`,… | native kept in `provider.native_finish_reason` |
| blocked response | `promptFeedback.blockReason` | → canonical finish safety + error envelope |
| `usage.input_tokens` | `usageMetadata.promptTokenCount` | |
| `usage.output_tokens` | `usageMetadata.candidatesTokenCount` | |
| `usage.total_tokens` | `usageMetadata.totalTokenCount` | |
| `usage.reasoning_tokens` | `usageMetadata.thoughtsTokenCount` | |
| `usage.cache_read_tokens` | `usageMetadata.cachedContentTokenCount` | |
| `created_ms` | ✗ | gateway stamp |

### 3.3 Streaming (`?alt=sse`)

| Canonical | Gemini SSE | Notes |
|---|---|---|
| `message_start` | first data chunk | synthesized (`responseId`, `modelVersion`) |
| `block_start` text | synthesized at first text part | |
| `block_delta` text | `candidates[0].content.parts[].text` | one event per part |
| `block_start/delta` tool | part `functionCall` (complete in one chunk) | emitted as block_start + single args delta + block_stop |
| `block_stop` | synthesized | |
| `message_delta` | chunk with `finishReason` or `usageMetadata` | |
| `message_stop` | stream close | synthesized |
| `error` | non-200 SSE / inline error JSON | |

### 3.4 Errors

| Canonical | Gemini |
|---|---|
| `status` | HTTP status (mapped: 429→429, 503 overloaded→overloaded_error) |
| `type` | derived from `error.status` (INVALID_ARGUMENT→invalid_request_error, UNAUTHENTICATED→authentication_error, PERMISSION_DENIED→permission_error, NOT_FOUND→not_found_error, RESOURCE_EXHAUSTED→rate_limit_error, UNAVAILABLE+"overloaded"→overloaded_error, else api_error) |
| `code` | `error.status` (gRPC code string) |
| `message` | `error.message` |

## 4. Lossy mapping summary (diff table)

The machine-readable source of truth is
`internal/protocol/conformance/lossy.go`; this table mirrors it (the
conformance suite verifies the two stay in sync). "Dropped by" lists the
target protocols that cannot carry the canonical field.

| Canonical field | Dropped by | Notes |
|---|---|---|
| `model` | gemini | carried in the URL path, not the body |
| `stream` | gemini | streaming is the :streamGenerateContent endpoint |
| `sampling.top_k` | openai | no top_k on the OpenAI wire |
| `sampling.seed` | anthropic | no seed parameter |
| `sampling.logit_bias` | anthropic, gemini | no logit biases |
| `sampling.logprobs/top_logprobs` | anthropic, gemini | no logprob request flags |
| `sampling.response_format` | anthropic | no structured-output mode |
| `response_format.json_schema envelope` | gemini | only the inner schema survives (name/strict dropped) |
| `messages[].thinking` | openai | assistant thinking blocks dropped from history |
| `messages[].role=system ordering` | anthropic | folds into one top-level system param |
| `tool_call.id / tool_result.call_id` | gemini | names only; IDs synthesized as `call_{name}` |
| `image.detail` | anthropic, gemini | OpenAI-only fidelity hint |
| `user` | gemini | no end-user identifier |
| `usage.reasoning_tokens` | anthropic | thinking tokens counted inside output tokens |
| `usage.cache_write_tokens` | gemini | no cache-write counter in usageMetadata |
| `response.created_ms` | anthropic, gemini | no created timestamp on the wire |
