# v2 Competitive Analysis — OneGate vs OmniRoute vs Bifrost

Status: research baseline for the v2 plan ([roadmap-v2](../roadmap-v2.md)).
Owner: research-analyst. Compiled 2026-10-11 from the live upstream sources:

- **OmniRoute** — `github.com/diegosouzapw/OmniRoute` (74.9k stars, 10.8k forks,
  10,264 commits, branch `release/v3.8.52` — the exact version OneGate's phase-7
  parity audit targeted). Node.js 22/24 + Next.js 16 + TypeScript 6.0 monolith.
- **Bifrost** — `github.com/maximhq/bifrost` (Maxim AI). Go gateway + React 19 /
  Vite / TanStack Router dashboard, Tailwind + Radix (shadcn-style primitives),
  Redux Toolkit + RTK Query, WebSocket live updates.

OneGate v1.0.0 shipped **wire-protocol parity** with OmniRoute v3.8.52's proxy
surface — that was the phase-7 charter and it held (86/86 nodes, all gates PASS).
But parity of the *proxy wire contract* is not parity of the *product*. This
document is the honest gap inventory.

## 1. What OneGate v1.0.0 has

| Area | OneGate v1.0.0 |
|---|---|
| Core | Single static Go binary (no Node runtime), embedded SQLite (WAL), embedded dashboard |
| Proxy | OpenAI, Anthropic, Gemini wire protocols + OpenAI-compatible providers; buffered + SSE streaming; retry-before-first-byte fallback |
| Routing | Model registry with aliases, 4 policies (ordered, weighted, cost, latency), health tracking, 1s hot-reload |
| Access | Virtual keys (argon2id), scopes, RPM/TPM/spend/concurrency limits, per-key usage |
| Observability | Trace IDs, structured logs, Prometheus `/metrics`, usage rollups, live log feed, request log API |
| Dashboard | 6 pages (providers, routing, keys, usage, logs, settings) + login; 9 shadcn primitives; ~2.8k LOC |
| API | 24 management endpoints, OpenAPI-first with generated TS client |
| Ops | 5-platform release matrix, Docker, npx launcher, `onegate import` |

## 2. OmniRoute product surface (what v1.0.0 does not have)

### 2.1 Routing & resilience
- **Auto-combos**: `model: "auto"` builds a virtual combo from connected
  providers, scored live on **16 factors** (health, quota, cost, latency, task
  fit, quality, session availability…). Nine variants: `auto`, `auto/coding`,
  `auto/fast`, `auto/cheap`, `auto/subscription`, `auto/thrifty`,
  `auto/offline`, `auto/smart`, `auto/chaos` (parallel fan-out panel).
- **19 routing strategies** (OneGate: 4): priority, fill-first, weighted,
  round-robin, p2c (power-of-two-choices), least-used, random, strict-random,
  cost-optimized, headroom, reset-window, reset-aware, context-relay,
  context-optimized, cache-optimized, lkgp, auto (16-factor), fusion
  (panel + judge), pipeline (chained steps).
- **3 independent resilience layers** + adaptive admission (heavyweight
  requests queue instead of 503), atomic RPM rolling leases, Quota-Share
  (fair split of a shared account across pooled keys, work-conserving).

### 2.2 Token efficiency
- **12-engine compression pipeline** (transparent, no client changes):
  session-dedup, CCR, lite, RTK (tool-result filtering), responses tool output,
  headroom (GCF tabular), relevance, Caveman (prose), aggressive, LLMLingua-2
  (MobileBERT ONNX), Ultra, OmniGlyph. Claims 15–95% savings; presets
  (lite/standard/aggressive/ultra); per-request header control; response header
  echoes the applied plan.
- **Output styles** (terse, less-code, action-first, CJK-terse) and an
  **adaptive context-budget dial**.
- **Semantic response cache** + cache-affinity routing (pin prompt prefixes to
  the connection holding the cache) + cache-HIT savings header.

### 2.3 Provider ecosystem
- **372 registered providers**, 1,443 model IDs, machine-readable catalog with
  pricing/free-tier metadata; 154 free-tier-marked providers; 491-row free
  budget catalog; live quota telemetry dashboard (`/dashboard/free-tiers`).
- Media: vision+audio+video modality bridge, `/v1/ocr`, `/v1/audio/translations`,
  image/video/audio generation (Grok Imagine, ComfyUI, Firefly, ElevenLabs…).
- OAuth flows for subscription-based providers (Claude, ChatGPT, Gemini
  coding plans); stealth layer (JA3/JA4 TLS fingerprint impersonation) for
  browser-based providers.

### 2.4 Protocols & integrations
- **MCP server** (stdio + HTTP `/api/mcp/stream` + SSE) exposing **110 tools**,
  33 scopes, full audit trail; **A2A** agent-to-agent (`/.well-known/agent.json`,
  JSON-RPC 2.0 + SSE, 6 skills; inbound delegation to agent fleets).
- **Webhooks** (request/quota events → Slack/Discord/Telegram/any URL).
- **Remote mode**: `connect <host>` with scoped tokens (read/write/admin),
  contexts, remote CLI driving a VPS instance.
- **CLI cockpit**: 80+ commands; `run` launches 7 coding CLIs (Claude Code,
  Codex, Aider, Goose, OpenCode, Qwen, Gemini) with per-process credential
  injection; `configure` writes 10 tools' configs; `chat` TUI client; `setup`
  wizard; `doctor` diagnostics.
- Tokenized URL aliases for clients that cannot send headers
  (`/vscode/<key>/chat/completions`, Ollama-compatible `/api/chat`).

### 2.5 Security, memory, platform
- Guardrails: prompt-injection guard on every route (red-team suite),
  opt-in credential masking (redacts leaked API keys in both directions),
  OIDC login gate for the dashboard.
- Memory: opt-in, SQLite FTS5 + int8-quantized vector embeddings, typed decay,
  per-request opt-out.
- Platform: Electron desktop, macOS menu-bar tray (Tauri), Android/Termux,
  PWA, VS Code Copilot Chat extension (OmniCopilot), 67 languages, headless
  mode (`serve --headless`), LTS release channels, plugin framework +
  marketplace, skills frameworks, Telegram bot bridge, BigQuery log export,
  gamification/leaderboards, LMArena-ELO provider rankings.

### 2.6 Where OneGate deliberately diverges (and wins)
- Single static binary, ~18MB, no Node/V8 heap tuning (OmniRoute's own docs
  recommend 8–12 GiB heaps for coding-agent workloads; OneGate is a Go binary
  with bounded memory).
- Sub-millisecond proxy overhead (OmniRoute is a Next.js monolith that also
  renders its dashboard in-process).
- Strict wire-contract parity suite (41 corpus cases) and a formal
  task-graph/evidence workflow.

## 3. Bifrost dashboard inventory (the UI/UX bar for v2)

**79 routes** under `workspace/`, organized as:

| Group | Routes |
|---|---|
| Overview | `dashboard` (charts: cost, latency, throughput, provider latency/throughput/tokens, model usage, log volume, cache token meters, overhead), `observability`, `pprof` |
| Logs | `logs` (live WebSocket tail, advanced filtering, request/response inspection), `audit-logs`, `mcp-logs` |
| Providers & models | `providers`, `providers/routing-rules`, `providers/model-limits`, `model-catalog`, `model-limits`, `custom-pricing`, `custom-pricing/overrides` |
| Routing | `routing-rules` (+ tree editor), `circuit-breaker`, `adaptive-routing` (+ settings), `complexity-router` |
| Access & governance | `virtual-keys`, `governance/virtual-keys`, `governance/users`, `governance/teams`, `governance/projects`, `governance/customers`, `governance/business-units`, `governance/rbac`, `governance/access-profiles` |
| Safety | `guardrails` (configuration + providers), `alerting/rules`, `alerting/channels`, `alerting/history` |
| Integrations | `mcp-registry` (+ library, OAuth callbacks), `mcp-sessions` (+ auth flows), `mcp-settings`, `webhooks` (+ deliveries), `virtual-mcps`, `oauth-grants`, `scim`, `prompt-repo`, `skills-repo`, `plugins`, `agent/handover` |
| Config | 14 sub-pages: api-keys, branding, caching, client-settings, compatibility, feature-flags, license, logging, mcp-gateway, observability, performance-tuning, pricing-config, proxy, security |
| Ops | `cluster`, `edge-control` (config/devices/inventory), `docs` (built-in documentation hub) |

**90 shadcn-style UI primitives** vs OneGate's 9 — including the ones that
make a dashboard feel professional: `sidebar`, `command` (⌘K palette),
`data table`, `sheet` (side drawer), `combobox`, `treeView`, `resizable`,
`tabs`, `toast`, `tooltip`, `popover`, `hoverCard`, `calendar`/`datePickerWithRange`,
`scrollArea`, `progress`, `chart` suite, `form` + `formField`, `codeEditor`,
`markdown`, `tagInput`, `multiSelect`, `slider`, `timePicker`, `tristateCheckbox`.

UX patterns worth adopting: collapsible icon sidebar with groups; ⌘K command
palette for navigation + actions; slide-over sheets for create/edit flows
instead of modal-only; rich data tables (column sort, faceted filters, row
actions); request-detail drawers with timing waterfalls; dark/light themes;
live-updating charts with time-range pickers; toast feedback on every mutation.

## 4. Gap matrix (OneGate v1.0.0 → v2 targets)

| # | Capability | OmniRoute | Bifrost | OneGate v1 | v2 phase |
|---|---|---|---|---|---|
| G1 | Dashboard component depth | partial | 90 primitives | 9 primitives | P10 |
| G2 | Dashboard routes | ~20 | 79 | 6 | P10 |
| G3 | Live log tail UX (WS + filters + drawer) | ✓ | ✓ | basic SSE list | P10 |
| G4 | Charts suite (cost/latency/throughput/cache) | ✓ | ✓ | basic usage | P10 |
| G5 | Routing strategies | 19 | ~8 | 4 | P11 |
| G6 | Auto-combo live scoring | 16-factor | adaptive-routing | ✗ | P11 |
| G7 | Routing transparency (decision header) | ✓ | ✓ | ✗ | P11 |
| G8 | Provider catalog + metadata | 372 + free tiers | 15+ | manual entry | P12 |
| G9 | Embeddings/images/audio/OCR endpoints | ✓ | ✓ | ✗ | P12 |
| G10 | Semantic response cache | ✓ | plugin | ✗ | P13 |
| G11 | Compression pipeline | 12 engines | ✗ | ✗ | P13 |
| G12 | MCP server (management tools) | 110 tools | ✓ | ✗ | P14 |
| G13 | Webhooks | ✓ | ✓ + deliveries UI | ✗ | P14 |
| G14 | Guardrails (injection/masking) | ✓ | ✓ | ✗ | P14 |
| G15 | Remote mode / scoped tokens | ✓ | ✓ | admin token only | P15 |
| G16 | CLI cockpit (models/keys/run/configure) | 80+ cmds | CLI | serve/import | P15 |
| G17 | Audit log + alerting | ✓ | ✓ | ✗ | P15 |
| G18 | Teams/projects/RBAC | ✗ | ✓ | single admin | P15 (RBAC-lite) |
| G19 | Playground (chat try-it) | TUI chat | ✗ | ✗ | P10 |

Scope note: OmniRoute's browser-stealth providers, Electron/Termux/PWA/VS Code
distribution, gamification, Telegram bridge, and 67-language i18n are **not**
v2 targets — they conflict with OneGate's single-binary charter or are
peripheral to gateway quality. The v2 plan takes the capabilities that define
gateway *product quality*: routing intelligence, token efficiency, protocol
coverage, integrations, and a Bifrost-class dashboard.
