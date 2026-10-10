# Virtual keys

A virtual key (`ogk-…`) is the only credential your clients ever hold.
It authenticates a request, scopes what the key can see, meters it
(RPM / TPM / concurrency / spend), and attributes every request and
micro-cent to a name you chose. Provider keys never leave the gateway.

## Minting

```bash
curl -s -X POST http://127.0.0.1:7420/api/keys \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
        "name": "prod-app",
        "scopes": {
          "allowed_models": ["gpt-4o", "claude-sonnet"],
          "policy_override": "cost"
        },
        "limits": {
          "rpm": 600,
          "tpm": 2000000,
          "concurrency": 8,
          "max_spend_usd_micros": 50000000
        },
        "expires_at_ms": 1767225600000
      }'
```

The raw key appears exactly once, in `raw_key`:

```json
{"key":{"id":"vkey_…","name":"prod-app","prefix":"ogk-9d2f…","scopes":{…},
        "limits":{…},"status":"active",…},
 "raw_key":"ogk-9d2f6f1c44b8a01e…"}
```

Store it now. The gateway stores only a peppered hash — by design it
cannot show the key again; a lost key is rotated (mint + revoke), not
recovered.

## Limits

| Limit | Unit | Enforced |
|---|---|---|
| `rpm` | requests / minute | 429 + `Retry-After` before any upstream work |
| `tpm` | tokens / minute (prompt+completion) | debited from actual usage; second over-quota request 429s |
| `concurrency` | parallel in-flight | excess gets 429 |
| `max_spend_usd_micros` | integer micro-USD lifetime cap | requests that would exceed it are refused |

Money is integer micro-USD everywhere (no floats in pricing paths).
Prices come from the built-in table; per-target cost shaping is the
`cost_multiplier` on model targets ([routing](routing.md)).

## Scopes

- `allowed_models` — whitelist of canonical IDs/aliases. Unlisted keys
  see all models; listed keys get 404 model-not-found for anything
  else (indistinguishable from a nonexistent model — the error never
  confirms existence).
- `allowed_providers` — restricts which upstreams may serve the key.
- `policy_override` / `model_overrides` — reshape routing per key
  ([routing](routing.md#per-key-reshaping)).

## Lifecycle

```bash
# list (never contains raw keys)
curl -s http://127.0.0.1:7420/api/keys -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN"

# revoke (clients start getting 403 key-revoked immediately)
curl -s -X POST http://127.0.0.1:7420/api/keys/key-…/revoke \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN"

# per-key usage
curl -s "http://127.0.0.1:7420/api/keys/key-…/usage?from_ms=…&to_ms=…" \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN"
```

- Revocation is instant (verified-key cache invalidates on the change
  hook, not on TTL).
- Expiry (`expires_at_ms`) turns the key into 403 expired at the
  boundary.
- Deleting a key removes attribution history with it; prefer revoke.

## Auth errors clients see

| Status | Meaning |
|---|---|
| 401 `invalid_api_key` | malformed or unknown key (identical response — no oracle) |
| 403 `key_revoked` / `key_expired` | what it says |
| 429 + `Retry-After` | quota (above) — retry after the header's seconds |

Errors render in the client's own protocol envelope (OpenAI/Anthropic/
Gemini shape) with the same codes OmniRoute used.

## Migrating keys from OmniRoute

`onegate import-keys` re-mints keys (legacy scrypt hashes are
unrecoverable by design), preserves revoked status, and imports usage
history with aggregate-preserving rollups. Raw keys are printed once
(to `--keys-out`, mode 0600) — distribute them to clients afterwards.
See `onegate import-keys -h` and [operations](operations.md#upgrades).
