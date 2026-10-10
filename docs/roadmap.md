# OneGate Roadmap

Ten phases, graph-gated. Every phase ends with a `p{N}.gate` node; the next
phase's nodes depend on it. The live source of truth is `tasks/*.graph.yaml`
— this page is the human summary.

> **v2 appended (2026-10-11):** phases 10–15 now extend this roadmap —
> Dashboard 2.0 (Bifrost-class), routing intelligence, provider ecosystem,
> token efficiency, integrations & safety, ops & multi-tenancy. See
> [`roadmap-v2.md`](roadmap-v2.md) and the gap audit in
> [`research/v2-competitive-analysis.md`](research/v2-competitive-analysis.md).
> Board: `python3 scripts/graph_status.py` (v1: 86/86 done; v2: 41 nodes pending).

| Phase | Name | Focus | Rough window |
|-------|------|-------|--------------|
| 0 | Scaffold | Repo, CI, agents, task graphs, docs | Week 1 |
| 1 | Foundation | Config, domain types, SQLite, logging, secrets | Weeks 2–4 |
| 2 | Protocols | Canonical schema; OpenAI / Anthropic / Gemini adapters | Weeks 5–8 |
| 3 | Proxy core | Reverse proxy, streaming pipeline, fallback engine | Weeks 9–12 |
| 4 | Routing | Model registry, virtual keys, quotas, health, hot reload | Weeks 13–16 |
| 5 | Observability | Traces, usage pipeline, metrics, rollups, live logs | Weeks 17–19 |
| 6 | Dashboard | Management API + full UI, auth, E2E suite | Weeks 19–22 |
| 7 | Compatibility | OmniRoute v3.8.52 parity audit, replay harness, import | Weeks 22–24 |
| 8 | Hardening | Load, chaos, crash recovery, security review, fuzzing | Weeks 24–25 |
| 9 | Release | go:embed, build matrix, Docker, npx launcher, v1.0.0 | Week 25+ |

## Phase narratives

**Phase 0 — Scaffold.** The workspace, the workflow, and the evidence
machinery. Ships: Go module + entrypoint skeleton, dashboard scaffold via
shadcn, CI (Go + web + graph checks), 24 agent charters, 10 phase graphs,
core docs. Done when first push is green and `graph_status.py --check` passes.

**Phase 1 — Foundation.** Everything long-lived gets born here: canonical
domain types, config loading with hot reload, the SQLite schema and repository
layer, structured logging with redaction, and encryption of provider keys at
rest. No proxy traffic yet — this is the load-bearing wall everything else
stands on.

**Phase 2 — Protocols.** One canonical internal schema, three real adapters
(OpenAI, Anthropic, Gemini) plus the OpenAI-compatible profile for everyone
else. Golden-fixture tested; streaming event orders verified event-by-event.
Zero imports from proxy/routing — enforced by review.

**Phase 3 — Proxy core.** The request path goes live: middleware, endpoints,
pooled upstream clients, the streaming pipeline with fast flush, and the
fallback engine with strict retry-before-first-byte semantics. Ends with
end-to-end tests against mock providers and a TTFT budget.

**Phase 4 — Routing & access.** Model registry and fallback policies as pure
functions, target health with circuit behavior, virtual keys (argon2id),
per-key quotas and spend caps, provider-429 handling, and hot reload of the
registry.

**Phase 5 — Observability.** Trace IDs across every hop, a usage pipeline
that never blocks the proxy, Prometheus metrics, rollup tables for analytics,
and the live-log feed. Closes with a pprof audit proving observability is
free.

**Phase 6 — Dashboard.** OpenAPI-first management API, generated TS client,
dashboard auth, and the core views: providers, routing, keys, usage, logs.
Playwright E2E suite against a seeded gateway.

**Phase 7 — Compatibility.** The parity proof: full checklist from the
OmniRoute v3.8.52 source, captured traffic corpus, replay diff harness, fix
cycles, and `onegate import` (config, keys, rules, history).

**Phase 8 — Hardening.** Soak and burst load tests, the chaos catalog,
crash-recovery matrix, security review, fuzzing of parsers. Optimizations are
profile-driven only.

**Phase 9 — Release.** Embedding decision (SSR vs SPA), go:embed pipeline,
cross-compile matrix, Docker images, the `npx onegate` launcher, complete user
docs, and the v1.0.0 ship decision.

## Principles

1. **Graph is truth.** README claims come second to node status.
2. **Evidence or it didn't happen.** `done` requires artifacts and results linked.
3. **Parity is contractual.** Existing OmniRoute users must not notice the rewrite.
4. **Single binary is the product.** Every dependency decision defends it.
5. **Fast path is sacred.** Nothing unbounded, nothing blocking, on the proxy path.
