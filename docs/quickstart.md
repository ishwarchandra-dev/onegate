# Quickstart — routed LLM traffic in five minutes

This path was verified against a clean install (fresh data directory,
freshly built binary, no provider account). It walks you from nothing
to a routed, quota-limited LLM request through OneGate — offline, using
the bundled mock provider — and then swaps in a real provider.

Total time for a fresh user: **under five minutes**. Every command below
is copy-pasteable; expected output follows each block.

## 0. Install (pick one)

**npm launcher** (downloads the official binary, SHA256-verified):

```bash
npx onegate --version
# v1.0.0 (commit 1a2b3c4, built 2026-10-10T00:00:00Z)
```

**Binary from a release** — grab an archive from
[GitHub Releases](https://github.com/ishwarchandra-dev/onegate/releases),
check it against `SHA256SUMS`, extract it, put it on your `PATH`.
Details: [installation](guides/installation.md).

**Docker** — `docker run -p 7420:7420 ghcr.io/ishwarchandra-dev/onegate`.
Details: [installation](guides/installation.md).

## 1. Start the gateway

```bash
onegate serve
```

Expected log line (then it idles):

```
onegate listening addr=127.0.0.1:7420 version=v1.0.0 ... schema_version=5
```

One port now serves everything: the dashboard at `http://127.0.0.1:7420/`,
the management API at `/api/*`, and the proxy at `/v1/*` (OpenAI),
`/v1/messages` (Anthropic), `/v1beta/models/*:generateContent` (Gemini).

Quick check:

```bash
curl -s http://127.0.0.1:7420/healthz
# {"schema_version":5,"status":"ok","version":"v1.0.0"}
```

## 2. Create an admin account

The dashboard and API need an admin. Set a token for scripting and
create the account (first run only — the same flow is offered as a
setup wizard on the dashboard's login screen):

```bash
export ONEGATE_ADMIN_TOKEN=dev-admin-token
curl -s -X POST http://127.0.0.1:7420/api/auth/setup \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "correct-horse-battery"}'
# {"user":{"username":"admin"},"csrf_token":"…","expires_at_ms":…}
#  ^ creates the admin AND logs you in (session cookie); with the admin
#    token set, subsequent curl calls below authenticate via Bearer.
#  Already created? 403 {"code":"setup_already_done"} — use login instead.
```

## 3. Add a provider

Try it without a provider account. Two ways:

- **You have the repository** (developers): run the bundled mock
  provider — it echoes, speaks all three wire protocols, and can also
  simulate failures:

  ```bash
  # terminal 2 — a local upstream answering on all 3 protocols
  go run ./cmd/mockprovider --port 9441
  ```

- **Binary/npm/Docker install**: point the provider at any
  OpenAI-compatible endpoint you can already reach (an internal
  gateway, LM Studio at `http://127.0.0.1:1234`, …) and skip to the
  `curl` below with that `base_url`.

Register it:

```bash
curl -s -X POST http://127.0.0.1:7420/api/providers \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
        "id": "prov-openai",
        "name": "OpenAI (primary)",
        "protocol": "openai",
        "base_url": "http://127.0.0.1:9441",
        "api_key": "sk-mock-0123456789",
        "enabled": true
      }'
# {"id":"prov-openai",...,"masked_key":"sk-m…6789"}   api_key is write-only:
# reads return masked_key (first 4 + … + last 4; ≤8 chars collapses to
# stars) — the real key is encrypted at rest
```

With a real account instead: `protocol: "openai"`,
`base_url: "https://api.openai.com"`, `api_key: "sk-…"`. Other protocols
and OpenAI-compatible vendors: [providers](guides/providers.md).

## 4. Define a model and its chain

```bash
curl -s -X POST http://127.0.0.1:7420/api/models \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
        "id": "gpt-4o",
        "aliases": ["gpt4o"],
        "targets": [
          {"provider_id": "prov-openai", "provider_model": "mock-echo", "position": 1}
        ],
        "capabilities": {"tools": true, "vision": true, "json_mode": true, "stream": true}
      }'
# {"id":"gpt-4o",...}
#  ^ capabilities gate requests: a request asking for features the model
#    does not declare (streaming, tools, vision, json_mode) gets 400
#    "no targets satisfy required capabilities". Defaults are all false
#    when omitted — set what the upstream really supports.

curl -s -X POST http://127.0.0.1:7420/api/routing-rules \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"model_id": "gpt-4o", "policy": "ordered", "enabled": true, "position": 1}'
# {"id":"rule-...",...}
```

The chain is live within ~1s (registry hot-reload; no restart). Chaining
two providers with automatic failover, weighted/cost/latency policies:
[routing](guides/routing.md).

## 5. Mint a virtual key

```bash
curl -s -X POST http://127.0.0.1:7420/api/keys \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "demo", "limits": {"rpm": 60, "tpm": 100000, "max_spend_usd_micros": 1000000}}'
```

The response contains the raw key **once**, in `raw_key` — store it
now, it is never shown again (later reads return only the `prefix`):

```json
{"key":{"id":"vkey_...","name":"demo","prefix":"ogk-9d2f…","scopes":{},
        "limits":{"rpm":600},"status":"active",...},
 "raw_key":"ogk-9d2f6f1c44b8a01e..."}
```

```bash
export OGK=ogk-9d2f6f1c44b8a01e...   # the raw_key value from YOUR response
```

## 6. Send a routed request

OpenAI-style (works with any OpenAI SDK — point `baseURL` at the
gateway and use the virtual key as the API key):

```bash
curl -s http://127.0.0.1:7420/v1/chat/completions \
  -H "Authorization: Bearer $OGK" -H "Content-Type: application/json" \
  -d '{
        "model": "gpt-4o",
        "max_tokens": 32,
        "messages": [{"role": "user", "content": "Say hello in one word."}]
      }'
```

Expected (the mock answers each protocol with a fixed greeting; set an
`X-Mock-Echo: …` header on the provider to change the text — see
`internal/mockprovider`):

```json
{"id":"chatcmpl-mock","object":"chat.completion","model":"gpt-4o",
 "choices":[{"index":0,"message":{"role":"assistant","content":"Hello from mock OpenAI"},"finish_reason":"stop"}],
 "usage":{...}}
```

Streaming — same endpoint, `"stream": true`. Anthropic and Gemini
clients work unchanged against their native paths with the same `ogk-`
key ([routing](guides/routing.md#client-endpoints) for the exact paths).

## 7. Watch it work

- Dashboard: open `http://127.0.0.1:7420/` — log in with the admin
  account; usage charts, live logs, and the request list populate as
  traffic flows ([dashboard tour](guides/dashboard.md)).
- Metrics: `curl -s http://127.0.0.1:7420/metrics` (admin-gated).

## Where to go next

| Want to… | Read |
|---|---|
| Connect real providers (OpenAI, Anthropic, Gemini, compat vendors) | [providers](guides/providers.md) |
| Failover chains, weighted/cost/latency routing, circuit breakers | [routing](guides/routing.md) |
| Scopes, quotas, spend caps, key lifecycle | [virtual keys](guides/keys.md) |
| Explore the dashboard | [dashboard tour](guides/dashboard.md) |
| Configure, operate, monitor, back up | [operations](guides/operations.md) |
| Something went wrong | [troubleshooting](guides/troubleshooting.md) |
| Every flag and exit code | [CLI reference](cli.md) |
| Migrate from OmniRoute | `onegate import --help` + [operations](guides/operations.md#upgrades) |
