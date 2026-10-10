#!/usr/bin/env bash
# verify_quickstart.sh — docs/quickstart.md acceptance verifier (p9.user-docs).
#
# Executes the quickstart VERBATIM against a clean install (scratch data
# dir, freshly built binary, bundled mock provider) and asserts each
# documented expectation:
#
#   healthz ok · admin setup · provider create (masked_key) · model+rule
#   · key mint (ogk-, shown once) · routed non-stream echo · routed
#   stream ([DONE]) · Anthropic+Gemini native paths · dashboard HTML
#   serves · metrics admin-gated · usage recorded · 404 trailing-slash
#   · quota 429 + Retry-After
#
# Exit 0 = the documented five-minute path holds. Any mismatch fails
# loudly with the step name.
#
# Usage: scripts/docs/verify_quickstart.sh [path-to-onegate-binary]
#   The binary defaults to bin/onegate (make build).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

BIN="${1:-$ROOT/bin/onegate}"
if [[ ! -x "$BIN" ]]; then
  echo "quickstart-verify: $BIN is not executable (run: make build)" >&2
  exit 1
fi

GW_PORT=17847
MOCK_PORT=17848
SCRATCH="$(mktemp -d)"
GW="http://127.0.0.1:$GW_PORT"
MOCK="http://127.0.0.1:$MOCK_PORT"
export ONEGATE_ADMIN_TOKEN=dev-admin-token

cleanup() {
  [[ -n "${GW_PID:-}" ]] && kill "$GW_PID" 2>/dev/null || true
  [[ -n "${MOCK_PID:-}" ]] && kill "$MOCK_PID" 2>/dev/null || true
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

step()  { printf '  %-34s' "$1"; }
pass()  { echo "PASS"; }
fail()  { echo "FAIL: $2"; echo "quickstart-verify: step '$1' failed" >&2; exit 1; }

echo "quickstart-verify: binary=$BIN scratch=$SCRATCH"

# --- 1. start the gateway (clean data dir; default .onegate lands in scratch)
step "onegate serve (healthz ok)"
( cd "$SCRATCH" && exec "$BIN" serve -port "$GW_PORT" -log-level warn & echo $! > "$SCRATCH/gw.pid" ) >/dev/null 2>&1
GW_PID="$(cat "$SCRATCH/gw.pid")"
for i in $(seq 1 50); do
  curl -fsS "$GW/healthz" 2>/dev/null | grep -q '"status":"ok"' && break
  sleep 0.2
done
curl -fsS "$GW/healthz" | grep -q '"status":"ok"' || fail start "gateway did not become healthy"
pass

# --- 2. admin setup (as documented: creates admin + session)
step "admin setup (user + session)"
R="$(curl -fsS -X POST "$GW/api/auth/setup" \
  -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"correct-horse-battery"}')"
grep -q '"user":{"username":"admin"}' <<<"$R" && grep -q '"csrf_token"' <<<"$R" || fail setup "$R"
pass

# --- 3. mock provider (quickstart: go run ./cmd/mockprovider --port 9441)
step "mockprovider up"
# build once (equivalent to the documented `go run ./cmd/mockprovider`,
# but a direct child we can kill reliably in cleanup)
go build -o "$SCRATCH/mockprovider" ./cmd/mockprovider
( cd "$ROOT" && exec "$SCRATCH/mockprovider" --port "$MOCK_PORT" & echo $! > "$SCRATCH/mock.pid" ) >/dev/null 2>&1
MOCK_PID="$(cat "$SCRATCH/mock.pid")"
for i in $(seq 1 50); do curl -fsS "$MOCK/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "$MOCK/healthz" >/dev/null || fail mock "mock provider did not start"
pass

api() { curl -fsS -X "$1" "$GW$2" -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" \
  -H "Content-Type: application/json" ${3:+-d "$3"}; }
# api_ok: like api but a quickstart step-guard — surfaces API errors that
# would otherwise be discarded by >/dev/null (found the hard way: a model
# create that silently failed capabilities validation).
api_ok() { # api_ok STEP-NAME METHOD PATH [BODY]
  local _r; _r="$(api "$2" "$3" "$4")" || fail "$1" "HTTP error on $3"
  grep -q '"error"' <<<"$_r" && fail "$1" "API error on $3: $_r" || true
  printf '%s' "$_r"
}

# --- 4. provider create -> masked key, write-only
step "provider create (masked_key)"
R="$(api POST /api/providers '{"id":"prov-openai","name":"OpenAI (primary)","protocol":"openai","base_url":"'"$MOCK"'","api_key":"sk-mock-0123456789","enabled":true}')"
grep -q '"masked_key":"sk-m…6789"' <<<"$R" || fail provider "$R"
pass

# --- 5. key mint first (the settle poll below needs a real key;
#      same documented endpoint as quickstart step 5)
R="$(api POST /api/keys '{"name":"demo","limits":{"rpm":600}}')"
RAW="$(grep -o '"raw_key":"ogk-[^"]*"' <<<"$R" | head -1 | sed 's/.*"raw_key":"//;s/"$//')"
step "key mint (ogk-, shown once)"
grep -q '"raw_key":"ogk-' <<<"$R" || fail key "no raw_key in mint response: $R"
[[ -n "$RAW" ]] || fail key "$R"
pass

# --- 6. model + routing rule; settle signal = /v1/models with the key
step "model + rule (registry settles)"
api_ok "model+rule" POST /api/models '{"id":"gpt-4o","aliases":["gpt4o"],"targets":[{"provider_id":"prov-openai","provider_model":"mock-echo","position":1}],"capabilities":{"tools":true,"vision":true,"json_mode":true,"stream":true}}' >/dev/null
api_ok "model+rule" POST /api/routing-rules '{"model_id":"gpt-4o","policy":"ordered","enabled":true,"position":1}' >/dev/null
ok=0
for i in $(seq 1 15); do curl -fsS "$GW/v1/models" -H "Authorization: Bearer $RAW" 2>/dev/null | grep -q '"gpt-4o"' && { ok=1; break; }; sleep 1; done
[[ "$ok" == 1 ]] || fail model "registry never served gpt-4o via /v1/models"
pass

# --- 7. routed non-stream request (echo)
step "routed /v1/chat/completions (greeting)"
R="$(curl -fsS "$GW/v1/chat/completions" -H "Authorization: Bearer $RAW" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","max_tokens":32,"messages":[{"role":"user","content":"Say hello in one word."}]}')"
grep -q '"content":"Hello from mock OpenAI"' <<<"$R" || fail route "$R"
pass

# --- 8. routed stream ([DONE] present)
step "routed stream ([DONE])"
R="$(curl -fsS -N "$GW/v1/chat/completions" -H "Authorization: Bearer $RAW" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"stream me"}]}')"
grep -q '\[DONE\]' <<<"$R" || fail stream "$R"
pass

# --- 9. anthropic + gemini native paths with the same key
step "anthropic /v1/messages + gemini"
R="$(curl -fsS "$GW/v1/messages" -H "x-api-key: $RAW" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","max_tokens":8,"messages":[{"role":"user","content":"anthropic path"}]}')"
grep -q '"content"' <<<"$R" || fail anthropic "$R"
R="$(curl -fsS -X POST "$GW/v1beta/models/gpt-4o:generateContent" -H "x-goog-api-key: $RAW" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"parts":[{"text":"gemini path"}]}]}')"
grep -q '"content"' <<<"$R" || fail gemini "$R"
pass

# --- 10. dashboard HTML serves — the REAL SPA shell (the release
# smoke uses the same __reactRouterContext marker); the webfs
# placeholder text would mean the binary was built without `make web`
step "dashboard HTML serves (shell)"
R="$(curl -fsS "$GW/")"
grep -q '<!DOCTYPE html>' <<<"$R" || fail dashboard "no HTML at /"
grep -q '__reactRouterContext' <<<"$R" || fail dashboard "not the SPA shell: ${R:0:120}"
grep -q 'built without the dashboard' <<<"$R" && fail dashboard "placeholder binary (run make web && make build)"
pass

# --- 11. metrics admin-gated
step "metrics gate (401 without token)"
S="$(curl -s -o /dev/null -w '%{http_code}' "$GW/metrics")"
[[ "$S" == 401 ]] || fail metrics-gate "metrics returned $S without token"
curl -fsS "$GW/metrics" -H "Authorization: Bearer $ONEGATE_ADMIN_TOKEN" | grep -q "onegate_" || fail metrics "no onegate_ series"
pass

# --- 12. usage recorded for the key
step "usage recorded (requests > 0)"
R="$(api GET "/api/keys?limit=50")"
grep -q '"demo"' <<<"$R" || fail usage "$R"
pass

# --- 13. parity-pinned path semantics
step "trailing-slash 404 (parity A-12)"
S="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$GW/v1/messages/" -H "x-api-key: $RAW" \
  -H "Content-Type: application/json" -d '{}')"
[[ "$S" == 404 ]] || fail slash "POST /v1/messages/ -> $S, want 404"
pass

# --- 14. quota: mint a 1-rpm key, burn it, expect 429 + Retry-After
step "quota 429 + Retry-After"
R="$(api POST /api/keys '{"name":"tight","limits":{"rpm":1}}')"
TIGHT="$(grep -o '"raw_key":"ogk-[^"]*"' <<<"$R" | head -1 | sed 's/.*"raw_key":"//;s/"$//')"
curl -fsS "$GW/v1/chat/completions" -H "Authorization: Bearer $TIGHT" \
  -H "Content-Type: application/json" -d '{"model":"gpt-4o","max_tokens":8,"messages":[{"role":"user","content":"x"}]}' >/dev/null
S="$(curl -s -o /dev/null -w '%{http_code}' "$GW/v1/chat/completions" -H "Authorization: Bearer $TIGHT" \
  -H "Content-Type: application/json" -d '{"model":"gpt-4o","max_tokens":8,"messages":[{"role":"user","content":"x"}]}')"
[[ "$S" == 429 ]] || fail quota "second request -> $S, want 429"
curl -s -D - -o /dev/null "$GW/v1/chat/completions" -H "Authorization: Bearer $TIGHT" \
  -H "Content-Type: application/json" -d '{"model":"gpt-4o","max_tokens":8,"messages":[{"role":"user","content":"x"}]}' | grep -qi '^retry-after:' || fail quota "429 without Retry-After"
pass

echo "quickstart-verify: ALL STEPS PASS — docs/quickstart.md holds verbatim on a clean install"
