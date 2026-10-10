# OneGate v2 Roadmap — Gateway Product Parity & Bifrost-class Dashboard

Appended after the v1.0.0 ship (2026-10-11). Six phases, numbered 10–15,
continuing the same graph-gated workflow: every phase ends with a `p{N}.gate`
node; nothing starts before its dependencies are `done`. Source of truth:
`tasks/phase-10..15.*.graph.yaml`. Research baseline:
[competitive analysis](research/v2-competitive-analysis.md).

**Why v2 exists.** v1 proved the core: one static Go binary with wire-parity
proxying, fallback, quotas, and a working dashboard. The gap audit against
the real OmniRoute product (372 providers, auto-combos, 12-engine compression,
MCP/A2A, 19 routing strategies) and the Bifrost dashboard (79 routes, 90
shadcn primitives, live WebSocket observability) shows v1 shipped a *proxy*
with an *admin panel*. v2 ships a *product* with a *control plane*.

**Non-negotiables carried from v1:**
1. Single static binary — every feature defends the no-runtime story.
2. Fast path is sacred — nothing unbounded on the proxy path.
3. Wire parity is contractual — v2 adds endpoints, never breaks v1 contracts.
4. Evidence or it didn't happen — gates require artifacts.

| Phase | Name | Focus | Est. window |
|-------|------|-------|-------------|
| 10 | Dashboard 2.0 | Bifrost-class UI: shell, 40+ shadcn primitives, live logs, charts, playground | Weeks 1–4 |
| 11 | Routing intelligence | auto-combo scoring, strategy suite, decision transparency | Weeks 4–7 |
| 12 | Provider ecosystem | catalog + metadata, embeddings/images/audio, OAuth providers | Weeks 7–10 |
| 13 | Token efficiency | semantic cache, compression pipeline, cache-affinity routing | Weeks 10–12 |
| 14 | Integrations & safety | MCP server, webhooks, guardrails | Weeks 12–15 |
| 15 | Ops & multi-tenancy | remote mode, CLI cockpit, audit log, alerting, teams/projects | Weeks 15–18 |

---

## Phase 10 — Dashboard 2.0 (the Bifrost bar)

The dashboard goes from 6 pages / 9 primitives to a control plane. Nothing
new on the backend except the APIs the UI needs (request detail, time-series,
playground proxy).

- **p10.design-system** — Expand the shadcn base: ~40 primitives (sidebar,
  command palette, data table, sheet, combobox, tree view, tabs, toast,
  tooltip, popover, date-picker, scroll-area, progress, form suite, charts).
  Dark/light themes stay; RTL support carried over.
- **p10.shell** — App shell: collapsible grouped sidebar, ⌘K command palette
  (navigate + mutate), breadcrumbs, global search, toasts on every mutation.
- **p10.overview** — New overview page: request volume, error rate, p50/p95
  latency, spend, per-provider throughput, model usage, time-range picker,
  live-updating charts fed by the rollup tables.
- **p10.providers** — Provider cards with live health status, latency sparklines,
  test-connection inline, catalog metadata (phase-12 seeds it), masked-key UX.
- **p10.routing** — Chain editor: tree view of model → targets, drag-to-reorder,
  per-target weight/position editing, strategy preview, health overlay.
- **p10.keys** — Key table with budget progress bars, usage drill-down,
  scope matrix, revoke/rotate flows, one-click copy-once reveal.
- **p10.logs** — Live tail (SSE → WebSocket if needed), faceted filters
  (model/provider/key/status/duration), request-detail drawer: timing
  waterfall (ingest → auth → route → upstream attempts → stream), full
  request/response bodies with redaction, trace-ID copy.
- **p10.playground** — Chat try-it: pick model, send requests through the real
  proxy path with a virtual key, inspect rendered responses + raw JSON,
  streaming preview. No dashboard-side LLM calls — it exercises the product.
- **p10.settings** — Settings redesign: config viewer/editor with validation,
  dangerous-zone (import/export), session management.

Gate: side-by-side screenshots vs Bifrost's public dashboard; Playwright
journeys for every new page; Lighthouse a11y ≥ 90 on core pages; embedded
bundle still < 2 MB gzipped total.

## Phase 11 — Routing intelligence

- **p11.scoring** — Auto-combo engine: `auto` model ID resolved at request
  time by live scoring across candidate targets (health, cost, latency,
  recent error rate, quota headroom, session affinity). Start with 6 factors;
  design for 16.
- **p11.strategies** — Strategy suite: round-robin, p2c, least-used,
  cost-optimized, headroom, reset-aware, cache-optimized (needs P13),
  fusion (panel + judge) — each a pure function on the target snapshot,
  table-driven tested.
- **p11.transparency** — `X-OneGate-Decision` response header (strategy,
  provider, model, attempt count, latency) + `/v1/routing/candidates`
  read-only endpoint exposing the live candidate pool; surfaced in the
  request-detail drawer.
- **p11.overrides** — Per-request overrides: model alias redirect, strategy
  pin, USD budget cap via headers; admin-configurable allow-list.
- **p11.admission** — Adaptive admission: heavyweight requests queue with
  bounded backpressure instead of immediate 429/503; rolling RPM leases per
  key (atomic, no lock contention on the fast path).

Gate: chaos test proving auto-combo converges after provider failure;
decision header present on 100% of proxied responses; strategy parity tests.

## Phase 12 — Provider ecosystem

- **p12.catalog** — Built-in provider catalog (seeded, versioned JSON): the
  30–50 providers that matter (OpenAI, Anthropic, Gemini, DeepSeek, Groq,
  Mistral, Qwen, xAI, Together, Fireworks, Groq, Cerebras, SiliconFlow,
  Z.AI, OpenRouter, Ollama, LM Studio…): wire profile, default base URL,
  model list with pricing/capability metadata. Dashboard consumes it;
  custom providers stay first-class.
- **p12.embeddings** — `/v1/embeddings` (OpenAI shape) across providers;
  usage accounting per key.
- **p12.media** — `/v1/images/generations`, `/v1/audio/transcriptions` +
  `/v1/audio/translations`, `/v1/audio/speech`; per-modality capability
  flags on models.
- **p12.oauth** — OAuth 2.0 (PKCE) provider flows for subscription-backed
  providers (Claude/ChatGPT/Gemini coding plans); encrypted token storage
  and refresh; dashboard account-linking UX.
- **p12.batch** — OpenAI-compatible Batch API (`/v1/batches`, `/v1/files`):
  background execution with bounded worker pool, per-key quota accounting.

Gate: catalog-driven provider add in ≤ 3 clicks; embeddings + one media
endpoint verified against two real providers; batch round-trip test.

## Phase 13 — Token efficiency

- **p13.cache** — Semantic response cache: exact-match first (v1 fast),
  optional embedding-similarity tier (pgvector-free — SQLite + in-process
  vectors), TTL + per-key opt-out, cache-hit savings header, hit-rate chart
  on the dashboard.
- **p13.compression** — Pluggable compression pipeline (Go, no ONNX in v2):
  lite (whitespace/preserve-guarded), prose (Caveman-style rule compression),
  tool-output (RTK-style filtering for shell/build/test output), aggressive
  (summarization via routed LLM, opt-in). Preservation engine guarantees
  code blocks/URLs/JSON pass through byte-perfect. Per-model presets,
  per-request header override, response header echoes the applied plan.
- **p13.affinity** — Cache-affinity routing: pin repeat prompt prefixes to
  the target holding the cached prefix; integrates with p11 strategies.

Gate: compression fidelity suite on a pinned corpus (zero code-block
mutation, measured savings reported); cache hit-rate ≥ 30% on replayed
workload benchmark; latency overhead budget enforced.

## Phase 14 — Integrations & safety

- **p14.mcp** — MCP server (stdio + HTTP streamable) exposing gateway
  management as tools: providers, models, keys, usage queries, routing
  health (~25 tools v2, not 110). Scoped auth; audit trail per tool call.
- **p14.webhooks** — Signed webhook events (request.completed, quota.exceeded,
  key.revoked, provider.unhealthy) with retry/backoff, delivery log + replay
  UI.
- **p14.guardrails** — Opt-in guardrails: prompt-injection heuristic scanner
  (configurable per model/key), credential-masking (redact API-key-shaped
  strings in both directions), with bypass scopes for admin keys.

Gate: MCP tool call drives a real provider change end-to-end; webhook
delivery + replay verified; guardrail red-team corpus passes.

## Phase 15 — Ops & multi-tenancy

- **p15.remote** — Remote mode: scoped API tokens (read/write/admin) with
  expiry + revocation, minted from the dashboard; CLI carries a `--host`
  flag targeting any instance.
- **p15.cli** — CLI cockpit: `onegate models|providers|keys|usage|logs`
  (JSON + table output), `onegate test <model>` smoke command,
  `onegate configure <tool>` writing Claude Code/Codex/Cursor/OpenCode
  configs pointing at the gateway.
- **p15.audit** — Audit log: every mutation (who/what/when/before→after),
  immutable append, dashboard page with filters.
- **p15.alerting** — Alert rules (error rate, latency, spend, quota)
  evaluated on rollups; channels: webhook, email(SMTP opt-in); alert history
  page.
- **p15.tenancy** — RBAC-lite: teams + projects; keys and usage roll up to
  projects; dashboard governance views (users, teams, projects). Full RBAC/
  SSO stays out of scope for v2 (single-binary charter; no identity provider
  dependency).

Gate: end-to-end multi-tenant demo (2 teams, 3 keys, quota per project,
alert fires on burst); audit log captures every mutation in the demo;
v2.0.0 release checklist green (matrix, Docker, npx smoke, docs).

---

## Explicit non-goals for v2

Browser-stealth providers (TLS impersonation), Electron/Termux/PWA/VS Code
distribution, gamification, Telegram bridge, LMArena rankings, 67-language
i18n, plugin marketplace. Rationale in the competitive analysis. These may
inform v3; none block gateway product quality.
