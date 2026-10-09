# Parity Replay Report

Generated 2026-10-09 22:51:05 by `scripts/parity/replay.py` (p7.replay-harness).

- Cases: **9/41 passing**
- Blocker diffs: **32**
- Cosmetic diffs: 0
- Elapsed: 5.1s

## Environment

```
provider prov-openai-a -> http://127.0.0.1:43087
provider prov-openai-b -> http://127.0.0.1:44329
provider prov-anthropic -> http://127.0.0.1:43087
provider prov-gemini -> http://127.0.0.1:43087
model gpt-4o (2 targets, ordered)
model openai-echo (1 targets, ordered)
model claude-sonnet (1 targets, ordered)
model gemini-flash (1 targets, ordered)
model fallback-500 (2 targets, ordered)
model fail-500 (2 targets, ordered)
model fail-401 (2 targets, ordered)
model fail-429 (2 targets, ordered)
model overloaded-model (1 targets, ordered)
model ctxlen-model (1 targets, ordered)
model slow-model (1 targets, ordered)
model midstream-model (1 targets, ordered)
model badjson-model (1 targets, ordered)
model tool-model (1 targets, ordered)
model think-model (1 targets, ordered)
model ping-model (1 targets, ordered)
model weighted-model (2 targets, weighted)
model stall-model (1 targets, ordered)
model circuit-model (1 targets, ordered)
key primary -> vkey_1791586260507_647967
key scoped-model -> vkey_1791586260508_156147
key scoped-provider -> vkey_1791586260508_519163
key revoked -> vkey_1791586260508_847056
key expired -> vkey_1791586260509_650900
key rpm2 -> vkey_1791586260509_972868
key tpm10 -> vkey_1791586260510_326426
key spend100 -> vkey_1791586260510_778193
key conc1 -> vkey_1791586260511_195185
key override-ordered -> vkey_1791586260511_566245
```

## Matrix

| Case | Rows | Severity | Result |
| --- | --- | --- | --- |
| CC-01 | A-1, B-4 | blocker | FAIL |
| CC-02 | A-2, B-3, B-9, D-1, D-9 | blocker | FAIL |
| CC-03 | D-2 | blocker | FAIL |
| CC-04 | C-18, D-10 | blocker | FAIL |
| CC-05 | G-1 | blocker | FAIL |
| CC-06 | A-4, D-4 | blocker | FAIL |
| CC-07 | D-5 | blocker | FAIL |
| CC-08 | D-6 | blocker | FAIL |
| CC-09 | G-2 | blocker | FAIL |
| CC-10 | A-6, D-7 | blocker | FAIL |
| CC-11 | G-3, G-4 | blocker | FAIL |
| CC-12 | A-10 | blocker | PASS |
| CC-13 | A-7 | blocker | FAIL |
| CC-14 | A-12 | blocker | PASS |
| CC-15 | B-7 | blocker | FAIL |
| CC-16 | C-1, C-24 | blocker | PASS |
| CC-17 | C-6, B-5 | blocker | FAIL |
| CC-18 | B-6, C-17 | blocker | FAIL |
| CC-19 | C-4 | blocker | PASS |
| CC-20 | C-5 | blocker | PASS |
| CC-21 | C-10, G-5 | blocker | FAIL |
| CC-22 | C-11 | blocker | FAIL |
| CC-23 | C-12 | blocker | FAIL |
| CC-24 | C-13 | blocker | PASS |
| CC-25 | C-14 | blocker | FAIL |
| CC-26 | C-15 | blocker | PASS |
| CC-27 | C-16 | blocker | FAIL |
| CC-28 | C-18 | blocker | FAIL |
| CC-29 | C-19 | blocker | FAIL |
| CC-30 | C-20 | blocker | FAIL |
| CC-31 | C-16 | blocker | FAIL |
| CC-32 | C-23 | blocker | FAIL |
| CC-33 | D-3, D-8 | blocker | FAIL |
| CC-34 | G-6 | blocker | FAIL |
| CC-35 | G-7 | blocker | FAIL |
| CC-36 | C-2 | blocker | PASS |
| CC-37 | C-3 | blocker | PASS |
| CC-38 | C-7 | blocker | FAIL |
| CC-39 | C-8 | blocker | FAIL |
| CC-40 | C-9 | blocker | FAIL |
| CC-41 | C-22 | blocker | FAIL |

## Failures

### CC-01 — OpenAI client, non-stream, canonical model echoes the requested model

**chat completion**

- `status: 503 != expected 200`
- `$.id: missing (expected "chatcmpl-mock")`
- `$.object: missing (expected "chat.completion")`
- `$.created: missing (expected 1700000000)`
- `$.model: missing (expected "gpt-4o")`
- `$.choices: missing (expected [{"index": 0, "message": {"role": "assistant", "content": "Hello from mock OpenAI"}, "finish_reason": "stop"}])`
- `$.usage: missing (expected {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15})`

### CC-02 — OpenAI client, stream: role chunk, content delta, finish+usage, [DONE]

**chat completion stream**

- `status: 503 != expected 200`
- `header Content-Type: 'application/json; charset=utf-8' != expected 'text/event-stream; charset=utf-8'`
- `sse: 0 frames != expected 5`

### CC-03 — OpenAI tool-call stream: index, id, empty-then-filled arguments, tool_calls finish

**tool stream**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 6`

### CC-04 — Fallback: first target 500, second target serves; usage records 2 attempts

**fallback success**

- `status: 503 != expected 200`
- `$.id: missing (expected "chatcmpl-mock")`
- `$.object: missing (expected "chat.completion")`
- `$.created: missing (expected 1700000000)`
- `$.model: missing (expected "fallback-500")`
- `$.choices: missing (expected [{"index": 0, "message": {"role": "assistant", "content": "Hello from mock OpenAI"}, "finish_reason": "stop"}])`
- `$.usage: missing (expected {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15})`

### CC-05 — Cross-protocol: OpenAI client, Anthropic provider, non-stream

**openai client to anthropic provider**

- `status: 503 != expected 200`
- `$.id: missing (expected "msg_mock")`
- `$.object: missing (expected "chat.completion")`
- `$.created: missing (expected "<any>")`
- `$.model: missing (expected "claude-sonnet")`
- `$.choices: missing (expected [{"index": 0, "message": {"role": "assistant", "content": "Hello from mock Anthropic"}, "finish_reason": "stop"}])`
- `$.usage: missing (expected {"prompt_tokens": 12, "completion_tokens": 6, "total_tokens": 18})`

### CC-06 — Anthropic client, stream: native event order with early usage

**anthropic stream**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 6`

### CC-07 — Anthropic stream: ping frames pass through between events

**ping stream**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 7`

### CC-08 — Anthropic stream: thinking_delta then signature_delta, then text block

**thinking stream**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 10`

### CC-09 — Cross-protocol: OpenAI client, Gemini provider, non-stream

**openai client to gemini provider**

- `status: 503 != expected 200`
- `$.id: missing (expected "<any>")`
- `$.object: missing (expected "chat.completion")`
- `$.created: missing (expected "<any>")`
- `$.model: missing (expected "gemini-flash")`
- `$.choices: missing (expected [{"index": 0, "message": {"role": "assistant", "content": "Hello from mock Gemini"}, "finish_reason": "stop"}])`
- `$.usage: missing (expected {"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12})`

### CC-10 — Gemini client, native stream: content chunk then finishReason+usageMetadata

**gemini stream**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 2`

### CC-11 — Cross-protocol: Anthropic client to OpenAI provider; Gemini client to OpenAI provider

**anthropic client to openai provider**

- `status: 503 != expected 200`
- `$.id: missing (expected "chatcmpl-mock")`
- `$.type: "error" != expected "message"`
- `$.role: missing (expected "assistant")`
- `$.model: missing (expected "gpt-4o")`
- `$.content: missing (expected [{"type": "text", "text": "Hello from mock OpenAI"}])`
- `$.stop_reason: missing (expected "end_turn")`
- `$.stop_sequence: missing (expected null)`
- `$.usage: missing (expected {"input_tokens": 10, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 5})`

**gemini client to openai provider (stream)**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 2`

### CC-13 — GET /v1/models lists canonical models (scope-filtered per key)

**models listing**

- `status: 404 != expected 200`
- `body is not JSON: Extra data: line 1 column 5 (char 4): '404 page not found\n'`

**scoped key sees only allowed models**

- `status: 404 != expected 200`
- `body is not JSON: Extra data: line 1 column 5 (char 4): '404 page not found\n'`

### CC-15 — No CORS headers on proxy endpoints even with an Origin header

**preflight-ish GET with Origin**

- `status: 404 != expected 200`

### CC-17 — RPM quota: third request in the minute rejects with 429 + Retry-After

**first request passes**

- `status: 503 != expected 200`

**second request passes**

- `status: 503 != expected 200`

**third request rejects**

- `status: 503 != expected 429`
- `header Retry-After: missing`
- `$.error.message: "no available upstream provider targets for model" != expected "rate limit of 2 requests per minute exceeded"`
- `$.error.type: "overloaded_error" != expected "rate_limit_error"`
- `$.error.code: missing (expected "rpm_limit_exceeded")`

### CC-18 — Upstream 429 exhausted: 429 in client envelope, provider Retry-After preserved

**terminal 429**

- `status: 503 != expected 429`
- `header Retry-After: missing`
- `$.error.message: "no available upstream provider targets for model" != expected "mock openai error 429"`
- `$.error.type: "overloaded_error" != expected "rate_limit_error"`
- `$.error.code: missing (expected "mock_error")`

### CC-21 — Unknown model: 404 not_found_error; aliases resolve to the same model

**unknown model**

- `status: 503 != expected 404`
- `$.error.message: "no available upstream provider targets for model" != expected "model not found: no-such-model"`
- `$.error.type: "overloaded_error" != expected "not_found_error"`

**alias resolves**

- `status: 503 != expected 200`
- `$.id: missing (expected "chatcmpl-mock")`
- `$.model: missing (expected "echo-alias")`
- `$.choices: missing (expected [{"index": 0, "message": {"role": "assistant", "content": "Hello from mock OpenAI"}, "finish_reason": "stop"}])`

### CC-22 — Model denied by key scope: 403 permission_error

**scoped model denied**

- `status: 503 != expected 403`
- `$.error.message: "no available upstream provider targets for model" != expected "model not allowed by virtual key scope"`
- `$.error.type: "overloaded_error" != expected "permission_error"`

### CC-23 — All provider targets denied by key scope: 403 permission_error

**scoped provider denied**

- `status: 503 != expected 403`
- `$.error.message: "no available upstream provider targets for model" != expected "all provider targets denied by virtual key scope"`
- `$.error.type: "overloaded_error" != expected "permission_error"`

### CC-25 — Anthropic missing max_tokens: 400 with the adapter message

**no max_tokens**

- `status: 503 != expected 400`
- `$.error.type: "overloaded_error" != expected "invalid_request_error"`

### CC-27 — Upstream 401 exhausted: 502 api_error / upstream_authentication

**terminal upstream auth failure**

- `status: 503 != expected 502`
- `$.error.message: "no available upstream provider targets for model" != expected "mock openai error 401"`
- `$.error.type: "overloaded_error" != expected "api_error"`
- `$.error.code: missing (expected "upstream_authentication")`

### CC-28 — Upstream 5xx exhausted: 502 api_error

**terminal 5xx**

- `status: 503 != expected 502`
- `$.error.message: "no available upstream provider targets for model" != expected "mock openai error 500"`
- `$.error.type: "overloaded_error" != expected "api_error"`
- `$.error.code: missing (expected "mock_error")`

### CC-29 — Anthropic 529 overload: 503 overloaded_error to the client

**overloaded**

- `$.error.message: "no available upstream provider targets for model" != expected "Overloaded"`

### CC-30 — Context length exceeded: 400 with provider code passthrough

**ctxlen**

- `status: 503 != expected 400`
- `$.error.message: "no available upstream provider targets for model" != expected "This model's maximum context length is 4096 tokens. However, your messages resulted in 5000 tokens. Please reduce the l`
- `$.error.type: "overloaded_error" != expected "invalid_request_error"`
- `$.error.code: missing (expected "context_length_exceeded")`
- `$.error.param: missing (expected "messages")`

### CC-31 — Upstream garbage response: 502 api_error / upstream_invalid_response

**bad json upstream**

- `status: 503 != expected 502`
- `$.error.message: "no available upstream provider targets for model" != expected "upstream returned an invalid response"`
- `$.error.type: "overloaded_error" != expected "api_error"`
- `$.error.code: missing (expected "upstream_invalid_response")`

### CC-32 — Circuit opens after 3 consecutive target failures; 4th request gets 503 no-healthy-targets

**failure 1 (502 terminal, circuit closed)**

- `status: 503 != expected 502`

**failure 2**

- `status: 503 != expected 502`

**failure 3 (circuit trips after this attempt)**

- `status: 503 != expected 502`

**4th request: no healthy targets**

- `$.error.message: "no available upstream provider targets for model" != expected "no healthy targets available"`

### CC-33 — Mid-stream provider abort after first byte: in-stream error event, no [DONE]

**midstream abort**

- `status: 503 != expected 200`
- `sse: 0 frames != expected 2`

### CC-34 — Per-key policy override: weighted model served by ordered override (deterministic target A)

**override ordered on weighted model**

- `status: 503 != expected 200`

### CC-35 — Provider model remap: canonical claude-sonnet served as claude-3-5-sonnet-latest

**remapped model**

- `status: 503 != expected 200`

### CC-38 — TPM quota: actual token usage debits the bucket; second request rejects 429

**first request passes (15 actual tokens against tpm=10)**

- `status: 503 != expected 200`

**second request rejects**

- `status: 503 != expected 429`
- `header Retry-After: missing`
- `$.error.message: "no available upstream provider targets for model" != expected "token limit of 10 tokens per minute exceeded"`
- `$.error.type: "overloaded_error" != expected "rate_limit_error"`
- `$.error.code: missing (expected "tpm_limit_exceeded")`

### CC-39 — Spend cap: gpt-4o request costs 75 micros; second request exceeds the 100-micro cap

**first request passes (spend 75 of 100)**

- `status: 503 != expected 200`

**second request rejects**

- `status: 503 != expected 429`
- `header Retry-After: missing`
- `$.error.message: "no available upstream provider targets for model" != expected "lifetime spend cap of $0.00 reached (current spend: $0.00)"`
- `$.error.type: "overloaded_error" != expected "rate_limit_error"`
- `$.error.code: missing (expected "spend_limit_exceeded")`

### CC-40 — Concurrency cap: two parallel requests to a slow model; exactly one 429

**request A**

- `status: 503 != expected 200`

**request B**

- `status: 503 != expected 429`
- `$.error.message: "no available upstream provider targets for model" != expected "concurrency limit of 1 in-flight requests reached"`
- `$.error.type: "overloaded_error" != expected "rate_limit_error"`
- `$.error.code: missing (expected "concurrency_limit_exceeded")`

### CC-41 — Client disconnect during stalled upstream: usage recorded as cancelled, upstream cancelled

**post-check**

- `post.status: "error" != expected "cancelled"`
