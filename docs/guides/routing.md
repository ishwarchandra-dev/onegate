# Routing

Routing decides, for a request naming a canonical model, which provider
target serves it — and what happens when that target fails. The policy
engine is pure decision code over an atomically-swapped registry
snapshot; changes made in the dashboard or API go live within ~1 s
(storage watcher), never requiring a restart.

## The three objects

1. **Providers** — upstreams ([providers](providers.md)).
2. **Models** — a canonical ID (`gpt-4o`), optional aliases, and one or
   more **targets**: `{provider_id, provider_model, position, weight,
   cost_multiplier}`. `provider_model` is the name the upstream knows.
3. **Routing rules** — per-model policy selector: `ordered`, `weighted`,
   `cost`, or `latency` (+ enable/disable and ordering among rules).

## A failover chain in one command

```bash
curl -s -X POST http://127.0.0.1:7420/api/models \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
        "id": "claude-sonnet",
        "aliases": ["claude"],
        "targets": [
          {"provider_id": "prov-anthropic", "provider_model": "claude-3-5-sonnet-latest", "position": 1},
          {"provider_id": "prov-openai",    "provider_model": "gpt-4o",                  "position": 2},
          {"provider_id": "prov-compat",    "provider_model": "deepseek-chat",           "position": 3}
        ]
      }'
curl -s -X POST http://127.0.0.1:7420/api/routing-rules \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"model_id": "claude-sonnet", "policy": "ordered", "enabled": true, "position": 1}'
```

Behavior you get for free:

- `ordered` walks the chain top-down. A failing target (5xx, 429,
  connect error, invalid response) fails over to the next — **only
  before the first byte** reaches the client; once streaming has begun
  the chain is frozen and errors render as client-native SSE error
  frames.
- Per-key overrides can reshape this per consumer (below).

## The four policies

| Policy | Picks | Knob per target |
|---|---|---|
| `ordered` | chain order, failover down the list | `position` (lower first) |
| `weighted` | random share across targets | `weight` (default 1) |
| `cost` | cheapest first (nominal price × `cost_multiplier`/100) | `cost_multiplier` |
| `latency` | fastest-responding first (rolling EWMA) | — |

Resolution order (most specific wins): routing-rule override → key's
`model_overrides` → key's `policy_override` → key scopes' implicit
policy → the model's rule.

## Circuit breakers

Every failed attempt trips a failure on that (provider, model) pair.
After repeated failures the circuit opens: requests skip the target
for a cooldown (10 s) instead of waiting for it, then a single request
probes (half-open) and reopens the circuit on success. Dashboard
status pages show live circuit state per target; the API exposes it
under `/api/routing/health`.

## Per-key reshaping

```json
{
  "name": "team-a",
  "scopes": {
    "allowed_models": ["gpt-4o", "claude-sonnet"],
    "policy_override": "cost",
    "model_overrides": {"gpt-4o": "latency"}
  }
}
```

Keys without `allowed_models` see every model; keys with a list are
blind to the rest (404 model-not-found, indistinguishable from a
nonexistent model). Full lifecycle and quotas: [keys](keys.md).

## Client endpoints

| Client protocol | Path | Credential header |
|---|---|---|
| OpenAI | `POST /v1/chat/completions`, `GET /v1/models` | `Authorization: Bearer ogk-…` |
| Anthropic | `POST /v1/messages` | `x-api-key: ogk-…` |
| Gemini | `POST /v1beta/models/{model}:generateContent` and `:streamGenerateContent` | `x-goog-api-key: ogk-…` (or `?key=`) |

Any SDK works unchanged: set `baseURL` to the gateway and use the
virtual key as the API key. Cross-protocol translation (OpenAI SDK →
Anthropic model, etc.) is automatic; the model you name is the
canonical ID or an alias.

## Streaming semantics

- SSE frames are translated per-protocol and flushed per frame
  (TTFT is a tracked metric).
- `[DONE]` is appended for OpenAI clients — never after an error.
- Client disconnect cancels the upstream request; usage is recorded as
  `cancelled`.
- Mid-stream upstream failures become in-stream error events; the
  chain does not retry (first-byte rule above).

## Hot reload

Providers, models, rules, and key changes propagate to the data plane
through the registry watcher (~1 s) and a credential cache TTL (15 s).
The parity corpus (41 cases) pins all of this; it is re-run in CI.
