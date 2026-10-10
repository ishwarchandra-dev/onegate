# Changelog

All notable changes to OneGate are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is
[Semantic Versioning](https://semver.org/). Entries are assembled from
the task graph's done-nodes and their evidence (see `tasks/`) — never
from memory. Upgrade instructions ship with every entry.

## [1.0.0] — 2026-10-10

First general-availability release: the complete OmniRoute v3.8.52
experience as a single static Go binary with an embedded dashboard.
86/86 task-graph nodes done across ten phases; every phase closed via
a gate report (`docs/gates/`).

### Added — Gateway core

- Single-binary LLM gateway: OpenAI, Anthropic and Gemini behind one
  endpoint; any client SDK works unchanged against its native paths
  (`/v1/chat/completions`, `/v1/messages`, `/v1beta/models/*`) with
  cross-protocol translation through a canonical schema (ADR 004;
  golden-fixture and cross-adapter conformance suites; documented lossy
  table). OpenAI-compatible vendor profile (`openai-compat`) with
  base-URL path anchoring. (p2.*, p7.parity-fixes)
- Reverse proxy core: SSRF-guarded pooled upstream client; SSE pipeline
  with per-frame flush and TTFT tracking; retry/fallback engine with
  strict retry-before-first-byte semantics (mid-stream failures render
  client-native error frames, never `[DONE]`); client disconnect
  cancels upstream; 32 MiB body cap. (p3.*)
- Routing: hot-reloaded registry (~1 s, no restarts) with
  ordered/weighted/cost/latency policies, per-key overrides, and
  circuit breakers with half-open recovery. (p4.*)
- Virtual keys: `ogk-` minting with show-once raw output; scopes
  (allowed models/providers, policy overrides); RPM/TPM/concurrency/
  spend caps with honest `Retry-After`; instant revocation/expiry via
  change-hook cache invalidation. Integer micro-USD pricing everywhere.
  (p4.virtual-keys, p4.quotas, p8.perf-fixes)
- Observability: request trace IDs across every hop; redacted
  structured logs; bounded non-blocking usage pipeline
  (drop-and-count on overflow, never blocks the proxy); Prometheus
  `/metrics` (admin-gated); hourly usage rollups; live-log SSE feed.
  (p5.*)
- Management API + embedded dashboard (React Router 7 · Tailwind v4 ·
  shadcn/ui): OpenAPI-first contract, 35 operations, generated TS
  client and Go route table (CI-enforced freshness); session auth with
  CSRF + first-run setup wizard; providers (masked keys, live probes),
  routing (chain editor, circuit health), keys (show-once), usage
  (rollup charts, CSV), live logs; RTL-complete; 10/10 Playwright
  journeys green. (p6.*)
- Embedded dashboard via `go:embed` (ADR 006): one port serves
  UI + API + proxy; build-time gzip; immutable asset caching; release
  builds refuse a missing dashboard (`ONEGATE_REQUIRE_EMBED`). (p9.embed-*)

### Added — Compatibility & migration (from OmniRoute v3.8.52)

- Behavioral parity: 41-case replay corpus, all green; per-area
  sign-offs with a cosmetic-divergence register (A-11 envelope shapes);
  parity is re-run in CI on every push. (p7.*)
- `onegate import` (dry-run default) and `onegate import-keys`
  (re-minted keys shown once, revoked stays revoked, aggregate-
  preserving usage history import). Idempotent re-runs. (p7.config-import,
  p7.data-import)

### Added — Hardening (evidence in `docs/reports/`)

- Load: 60 s @ 60 RPS mixed stream/non-stream — p99 2.3 ms, zero
  errors, flat resource curves; burst/churn/overload degrade explicitly
  (never silent queuing). (p8.load-*)
- Chaos: provider outage, mid-stream truncation, stall+disconnect, fd
  exhaustion — 4/4 match expected behavior. (p8.chaos-catalog)
- Crash recovery: kill -9 matrix converges; 6646/6646 confirmed
  requests durable at ~1100 req/s. (p8.crash-recovery)
- Fuzzing: 15 native fuzz targets with committed corpora; SSE framing
  spec fix (lone-CR) and usage clamp found and fixed here. (p8.fuzzing)
- Performance: profile-driven cycle — verified-key cache with
  change-hook invalidation, touch coalescing, sync.Map quota keys;
  hot path −44 % to −56 % ns/op (3-run medians). (p8.perf-fixes)

### Added — Distribution & tooling

- Release matrix: linux/darwin/windows × amd64/arm64, CGO-free
  (pure-Go SQLite, ADR 003), `SHA256SUMS` + `provenance.json`, native
  runtime smokes per platform. (p9.build-matrix)
- Docker: distroless non-root image, `/data` volume, multi-arch GHCR
  publishing with provenance attestations. (p9.docker)
- `npx onegate` launcher: zero-dependency npm package; downloads the
  release binary, SHA256-verifies it against `SHA256SUMS` (a corrupt or
  tampered download refuses to run, exit 3), caches per version;
  `ONEGATE_BINARY` override for system/offline installs. (p9.npx-launcher)
- CLI surface: `serve` / `import` / `import-keys` / `config` / `help`,
  exit codes 0/1/2, full reference in `docs/cli.md` kept fresh by a
  mechanical docs-sync check in CI. (p9.cli-polish)
- Docs: five-minute quickstart re-verified verbatim against a clean
  install on every CI push, plus the guide set (installation, providers,
  routing, keys, dashboard, operations, troubleshooting). (p9.user-docs)

### Security

- Provider keys encrypted at rest (AES-GCM under `master.key`);
  virtual keys stored as peppered HMAC-SHA256 hashes (unrecoverable by
  design); admin passwords PBKDF2-HMAC-SHA256 (600k iterations).
- SSRF-guarded upstream dialing; request-ID validation; body caps;
  slowloris read-header guard; cookie `HttpOnly`/`SameSite`/`Secure`;
  CSRF double-submit on session-authenticated mutations.
- Threat model v2 (`docs/security/threat-model.md`): 12 threats, 6
  trust boundaries; all findings fixed (toolchain pinned to go1.26.9
  via `go.mod`; govulncheck clean in CI). (p8.security-review)

### Deprecated

- Nothing. This is the first release.

### Fixed

- Nothing prior to GA. In-release corrections worth noting: parity
  CC-14 regression (unmatched non-GET answered 405 after the dashboard
  catch-all landed) caught by the parity replay and fixed before
  tagging; see the git history for the full record.

[1.0.0]: https://github.com/ishwarchandra-dev/onegate/releases/tag/v1.0.0
