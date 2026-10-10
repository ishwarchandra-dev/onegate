# OneGate Threat Model — v2 (Phase 8 security review)

Status: **current** · Owner: security-engineer · Node: p8.security-review
Supersedes: implicit v1 (per-phase security decisions recorded in gates
0–7 and ADRs 002–005; this document consolidates them).

Scope: the `onegate` gateway binary (data plane + management plane), its
SQLite store, the embedded dashboard, and the CLI import surface.

## 1. System under review

```
LLM clients ──vkey──▶ ┌──────────────────────────────────────┐
                      │  ingest (/v1/... gemini/...)          │
                      │  routing + fallback + quotas          │──vkey scopes──▶
                      │  protocol adapters (openai/anthropic/ │
                      │  gemini/openai-compat profiles)       │
                      └───────────┬──────────────┬────────────┘
                                  │ AES-GCM      │ SSRF-guarded
                                  ▼              ▼ dialer
                        SQLite (WAL)      upstream providers
                              ▲
Admin/browser ──session───────┘
  /api/* (admin token | session cookie + CSRF)
  /metrics (admin-gated)  /api/logs/live (SSE, auth)
```

Assets: provider API keys (encrypted at rest), virtual keys (argon2id
hashes only), admin account (PBKDF2-HMAC-SHA256, 600k iter), usage
history, session cookies.

## 2. Trust boundaries

| # | Boundary | Traffic | Enforcement |
|---|----------|---------|-------------|
| B1 | Client → ingest endpoints | untrusted virtual-key holders | key verify (constant-time), scopes, quotas, body caps |
| B2 | Browser → management API | dashboard user (or admin token) | session cookie HttpOnly/SameSite=Lax (+Secure on TLS), CSRF double-submit on session mutations, per-class rate limits, 1 MiB body cap, per-IP limits on public routes |
| B3 | Gateway → upstream providers | provider base URLs (admin-configured) | scheme allowlist, literal-IP screening, dial-time SSRF guard (metadata/link-local ranges), `Proxy: nil` |
| B4 | Process → disk | SQLite + master.key | master secret 0600 file, AES-GCM for provider keys, WAL for crash safety |
| B5 | Legacy config → importer | `omniroute.json` (untrusted file) | plan-time provider validation (S-3, this phase); legacy DB read-only |
| B6 | Host → metrics/SSE | operators | `/metrics` admin-gated; `/api/logs/live` requires session/token; ring buffer is in-process only |

## 3. Attacker model & mitigations

| ID | Threat | Vector | Mitigation (verified this review) | Residual |
|----|--------|--------|-----------------------------------|----------|
| T1 | Provider key theft | SQLite file exfiltration | keys sealed AES-GCM with master.key (never in DB plaintext); masked on every read path | disk compromise also yields master.key → physical/host access assumed protected by operator |
| T2 | Virtual key forgery | guessing/leaked hashes | argon2id hashes, constant-time verify, raw key never stored; show-once at mint; `KeyHash` stripped from Get/List | rainbow attack on stolen DB requires argon2id cost — by design |
| T3 | Session hijack | cookie theft / XSS | HttpOnly + SameSite=Lax (+Secure on TLS); no DOM key access; dashboard renders no raw HTML; fresh session id per login | SameSite=Lax chosen over Strict for top-level nav parity; mutations still CSRF-guarded |
| T4 | CSRF on management mutations | cross-site form/fetch | double-submit token on **every** session-authenticated mutation (wrap enforces; token-auth requests exempt — no ambient credential) | none open |
| T5 | SSRF to cloud metadata | malicious/typo'd base URL | build-time scheme+IP check **and** dial-time `Control` hook (DNS-rebinding safe); redirects re-dial through the same guarded dialer; `Proxy:nil` blocks env-proxy pivot | loopback/RFC1918 deliberately allowed (local Ollama/vLLM are first-class); metadata ranges blocked |
| T6 | Admin brute force | repeated login attempts | per-IP rate limit on public routes (login/setup), PBKDF2 600k + timing equalizer (BurnPasswordWork), constant-time token compare | distributed attack → add fail2ban/egress firewall at operator discretion (documented, not built) |
| T7 | Key material in logs | debug logging of auth headers | redaction at handler layer (ReplaceAttr: `key`, `token`, `secret`, `authorization`, `api_key`...); no URL/query-string logging anywhere (Gemini `?key=` cannot land in logs) | none found |
| T8 | DoS via oversized bodies | ingest + management | ingest: protocol-level caps + upstream timeouts; management: 1 MiB LimitReader; rate limits per class; explicit 503 posture under overload (p8.load-burst) | volumetric network floods are an operator/infra concern |
| T9 | Dependency CVEs | supply chain | govulncheck gates CI (this phase); toolchain pinned `go1.26.9`; x/sys bumped past GO-2026-5024 | monitor new CVEs — CI job is the tripwire |
| T10 | Crash-state confusion | kill -9 | WAL + transactional migrations; crash matrix proven convergent (p8.crash-recovery) | none open |
| T11 | Token in URL / Referer leak | query-param credentials | admin/session tokens only in headers/cookies; the one query credential (Gemini `?key=`) is an inbound convention, never echoed into logs, redirects, or upstream calls | none |
| T12 | Timing side channels | auth comparisons | `subtle.ConstantTimeCompare` on admin token, CSRF token, session lookup keyed by 256-bit random id; password equalizer burns leftover PBKDF2 work | session-map lookup by id is O(1) hash — acceptable |

## 4. Findings register (this review)

| ID | Severity | Finding | Disposition |
|----|----------|---------|-------------|
| S-1 | **High** | Go toolchain 1.24.5: 37 stdlib vulns reachable (crypto/x509 quadratic parse, database/sql Rows.Scan, ...) — govulncheck symbol-level | **Fixed**: toolchain pinned `go1.26.9` in go.mod (`toolchain` directive; CI reads go-version-file); full suite re-run green |
| S-2 | Medium | GO-2026-5024 golang.org/x/sys/windows integer overflow (module-level, uncalled, windows-only) | **Fixed**: x/sys v0.22.0 → v0.44.0; govulncheck now reports 0 vulns (symbol + module level) |
| S-3 | Medium | Importer (`onegate import`) wrote provider rows without base-URL/protocol validation — a malicious `omniroute.json` could persist a `file://` or metadata-IP provider (data-plane SSRF guard would refuse it at request time, but the row would land and confuse operators) | **Fixed**: plan-time validation mirroring the API boundary (`validateProvider`, reusing `client.BlockedIPReason` — one policy source); invalid providers become `skip` actions with reasons in the dry-run report; Apply never writes them; 3 regression tests |
| S-4 | Low | govulncheck was not part of CI | **Fixed**: new `vuln` CI job runs govulncheck on every push/PR |
| S-5 | Info | In-memory session store: sessions lost on restart (users re-login); acceptable for single-binary v1, matches ADR posture | Tracked (documented limitation; candidate p9 hardening if multi-instance ever lands) |
| S-6 | Info | Per-IP login rate limiting is per-process (in-memory) — no shared budget across replicas | Same as S-5; single-binary scope makes this moot for v1 |
| S-7 | Info | Loopback/RFC1918 upstreams allowed by design (local providers first-class) — operators running on hostile multi-tenant hosts should set stricter guards (`NewSSRFGuard` supports extra CIDRs) | Documented in ssrf.go + here; no change |

**All high findings fixed (S-1). govulncheck clean: verified locally, enforced in CI (S-4).**

## 5. Audit trail (what was re-verified, where)

- Constant-time comparisons: `internal/api/auth.go` (admin token),
  `internal/api/api.go` (CSRF), `internal/observability/metrics.go`
  (Bearer/X-Admin-Key), `internal/auth/password.go` (PBKDF2 digest).
- Session cookie flags + fresh ids: `internal/api/sessions.go`;
  expiry sweep verified in p6.auth-sessions tests.
- Body caps: `internal/api/api.go` `maxBodyBytes = 1 MiB` (LimitReader,
  400 on overflow); ingest size caps live in the stream pipeline caps
  from p3.
- SSRF: `internal/proxy/client/ssrf.go` (scheme allowlist, literal-IP
  screen, dial-time `Control`, metadata CIDR list incl. IPv6
  `fd00:ec2::254`); `client.New` makes the guard mandatory (nil →
  default); transport `Proxy: nil`; redirects re-enter the guarded
  dialer (no redirect-based bypass).
- Log redaction: `internal/observability/logging.go` (key-suffix
  matching, `***REDACTED***`); verified no `r.URL`/query logging on any
  request path (T7/T11 grep evidence).
- Key hygiene: `internal/auth/vkeys.go` strips `KeyHash` on Get/List;
  mint is show-once (p4/p6 evidence).
- Probe path: management `test-connection` rides the same SSRF-guarded
  transport as the data plane (`internal/api/providers.go`).
- Importer: `internal/importer/plan.go` `validateProvider` + skip
  actions (S-3 fix, tests in `plan_security_test.go`).

## 6. Sign-off

- govulncheck: **0 findings** (go1.26.9, after S-1/S-2), CI-enforced.
- Threat model updated with B5 (importer boundary) and T9–T12 rows.
- No high findings open; S-5/S-6/S-7 tracked as documented limitations.

— security-engineer, p8.security-review
