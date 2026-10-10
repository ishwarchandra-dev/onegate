# Providers

A provider is one upstream: a protocol, a base URL, and a credential.
OneGate translates between the client's protocol and the provider's,
so an OpenAI SDK can hit an Anthropic model and vice versa — the
translation table is
[docs/protocol-mappings.md](../protocol-mappings.md); the lossy edges
are documented there and enforced by tests.

## Adding a provider

```bash
curl -s -X POST http://127.0.0.1:7420/api/providers \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
        "id": "prov-anthropic",
        "name": "Anthropic (primary)",
        "protocol": "anthropic",
        "base_url": "https://api.anthropic.com",
        "api_key": "sk-ant-…",
        "enabled": true
      }'
```

| Field | Meaning |
|---|---|
| `id` | Stable identifier used by models/targets (`prov_…` prefixed if you omit it) |
| `protocol` | `openai` \| `anthropic` \| `gemini` \| `openai-compat` |
| `base_url` | Upstream root. For `openai-compat` it may include a path prefix (e.g. `https://api.deepseek.com/v1`); the standard endpoint paths are anchored under it |
| `api_key` | Write-only. Encrypted at rest (AES-GCM under `master.key`); every read returns `masked_key` |
| `enabled` | Disabled providers are skipped by routing entirely |

## The four protocols in practice

- **openai** — `https://api.openai.com`. Sends `Authorization: Bearer`.
- **anthropic** — `https://api.anthropic.com`. Sends `x-api-key` + `anthropic-version`.
- **gemini** — `https://generativelanguage.googleapis.com`. Sends `x-goog-api-key`.
- **openai-compat** — anything speaking the OpenAI wire format:
  DeepSeek, Groq, Together, Mistral, vLLM, LM Studio, Ollama's compat
  endpoint, another OneGate. Same paths and auth as `openai`, plus the
  base-URL path-prefix anchoring above.

Same-protocol pass-through is byte-stable (golden-fixture tested);
cross-protocol goes through the canonical schema and is conformance-
tested across every adapter pair.

## Testing a connection

The dashboard's Providers view has a Test button; via API it's a
`GET` probe that respects the provider's protocol and reports
per-protocol errors:

```bash
curl -s -X POST http://127.0.0.1:7420/api/providers/prov-anthropic/test \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN"
# {"ok":true,...}   or {"ok":false,"error":{"detail":"401 invalid x-api-key"}}
```

## Rotating keys

Update the provider with a new `api_key` — the credential cache on the
data plane invalidates within its TTL (15 s), so rotation propagates
without a restart:

```bash
curl -s -X PUT http://127.0.0.1:7420/api/providers/prov-anthropic \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"Anthropic (primary)","protocol":"anthropic",
       "base_url":"https://api.anthropic.com","api_key":"sk-ant-NEW","enabled":true}'
```

## Security notes

- Provider keys are sealed with AES-GCM under `master.key`; losing
  that file loses the keys (re-enter them — see
  [troubleshooting](troubleshooting.md)).
- The gateway dials only what you register as a provider, through an
  SSRF-guarded client. Internal/RFC-1918 upstreams are allowed by
  design (self-hosted models); for hostile multi-tenant hosts, tighten
  with the extra-CIDR knob described in
  [troubleshooting](troubleshooting.md#security).
- Provider errors never leak the credential: logs are redacted
  (`api_key`, `token`, `secret` patterns) and error envelopes carry
  only status/code/detail.

## Quirks

Provider-specific behavior (upstream 529s, Retry-After formats, ping
frames, context-length code shapes) is catalogued with citations in
[docs/research/provider-quirks.md](../research/provider-quirks.md) and
pinned by the parity corpus — you should not need it day-to-day, but
it explains every odd upstream response you will ever see.
