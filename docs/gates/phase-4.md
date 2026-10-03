# Phase 4 Gate Report — Routing Engine, Virtual Keys & Quotas

- Graph: `tasks/phase-4.routing.graph.yaml`
- Verdict: **PASSED** (all 10 nodes done, all gate criteria verified)
- Date: 2026-10-03

## Gate criteria

| Criterion | Verification | Result |
|---|---|---|
| Routing decision is a pure function, fully table-tested | `internal/routing/registry.go` implements `DecideTargets(req RouteRequest, snap *Snapshot, health HealthView) ([]Target, error)` as a pure function with zero mutable state or hidden locks. Verified by 16 table test cases in `internal/routing/registry_test.go` covering aliases, missing targets, key scopes, capabilities (tools, vision, json, stream), policy overrides, and target health filtering. | PASS |
| Virtual keys enforce auth, model mapping, and quotas under -race | `internal/auth/vkeys.go` provides constant-time verification (`subtle.ConstantTimeCompare`), raw keys returned once at creation and never stored in SQLite (only HMAC-SHA256 hash). `internal/ratelimit/quota.go` enforces concurrency limits, sliding token buckets for RPM/TPM, and lifetime micro-USD spend caps. Verified by `internal/auth/vkeys_test.go`, `internal/ratelimit/quota_test.go`, and `internal/routing/integration_test.go` under `-race`. | PASS |
| Provider 429/retry-after integrates with fallback decisions | `internal/ratelimit/backoff.go` parses `Retry-After` (seconds and HTTP-Dates), `retry-after-ms`, `x-ratelimit-reset-*`, and body text hints with full-jitter exponential backoff. Integrated into fallback routing in `internal/routing/integration_test.go` (`TestIntegration_RateLimitedProvider_429Fallback`), which proves provider 429s trigger retryable fallback to secondary targets before first byte. | PASS |
| Hot-reload of registry & rules without restart | `internal/routing/watcher.go` monitors storage changes via SHA-256 hash diffing and atomically swaps the registry snapshot (`atomic.Pointer[Snapshot]`). Verified in `internal/routing/watcher_test.go` and `internal/routing/integration_test.go` (`TestIntegration_HotReload_LiveUpdate`) that in-flight requests finish on their original snapshot while new requests immediately observe updated routes without server restarts. | PASS |

## Architecture & Layering Verification

The architecture layering contract (`AGENTS.md`) is strictly enforced:
- **Zero Storage Dependencies in Production Routing**: `internal/routing` depends only on stdlib and `internal/domain`. Persistence is inverted via `routing.StorageSource`, implemented in `internal/storage/routing_source.go`.
- **Enforced via Layering Test**: `internal/routing/layering_test.go` parses all AST imports in `internal/routing/*.go` to ensure zero forbidden internal imports (storage, proxy, server, config, auth, api, or third-party packages).
- **Exact Integer Currency**: All pricing tables and spend counters (`internal/ratelimit/pricing.go`) use integer micro-USD (1e-6 USD); floating-point types are strictly forbidden.

## Node acceptance evidence

| Node | Acceptance | Evidence |
|---|---|---|
| `p4.model-registry` | Decision function pure: `(req, registry, health) → targets`; table tests; capability flags filter targets | `internal/routing/registry.go`, `internal/routing/registry_test.go`, `internal/storage/routing_source.go`. |
| `p4.fallback-policies` | Fallback policies (ordered, weighted, cost-preferred, latency-preferred); OmniRoute v3.8.52 parity tests; deterministic weighted shuffle under seeding | `internal/routing/policy.go`, `internal/routing/policy_test.go`. |
| `p4.health-tracking` | Consecutive-failure thresholds open circuit; half-open probes recover; state changes observable via event channel | `internal/routing/health.go`, `internal/routing/health_test.go`. |
| `p4.virtual-keys` | Constant-time verify; raw key returned exactly once; scopes (allowed models, allowed providers, per-key routing overrides) | `internal/auth/vkeys.go`, `internal/auth/vkeys_test.go`, `internal/storage/repos.go`. |
| `p4.quotas` | Per-key quotas: concurrency limits, RPM/TPM sliding windows, spend caps, canonical 429 with retry-after header | `internal/ratelimit/quota.go`, `internal/ratelimit/quota_test.go`. |
| `p4.price-tables` | Exact micro-USD price tables for OpenAI, Anthropic, Gemini, and Llama; cost calculation and prompt token estimation | `internal/ratelimit/pricing.go`, `internal/ratelimit/pricing_test.go`. |
| `p4.provider-429` | Honors retry-after headers/dates/ms; capped+jittered exponential backoff; respects total context deadline | `internal/ratelimit/backoff.go`, `internal/ratelimit/backoff_test.go`. |
| `p4.hot-reload` | Storage change watch pipeline; atomic pointer swap; in-flight requests finish on old config; new requests see new config immediately | `internal/routing/watcher.go`, `internal/routing/watcher_test.go`. |
| `p4.integration` | Full matrix integration suite with mock providers: healthy/degraded/rate-limited provider × key scopes × quotas under `-race` | `internal/routing/integration_test.go`, `internal/routing/layering_test.go`. |
| `p4.gate` | All gate criteria verified | This gate report and clean test suite. |

## Verification commands

```bash
export PATH="$HOME/.local/go/bin:$PATH"
go vet ./...                              # clean static analysis across all packages
go test -count=1 ./... -race              # all packages pass with race detector
make build                                # bin/onegate compiles with ldflags
cd web && bun run build && bun run typecheck # dashboard bundle and typecheck green
python3 scripts/graph_status.py --check   # 10 phase graphs, 86 nodes, DAG valid
```

## Key decisions this phase

1. **Dependency Inversion for Routing Persistence**: `internal/routing` defines the `StorageSource` interface (`ListModels()`, `ListProviders()`, `ListRules()`). `internal/storage` implements this interface, ensuring the routing engine has zero coupling to SQLite, database connections, or storage repositories.
2. **Pure Decision Function**: Target resolution is completely isolated from system state: `DecideTargets(req, snapshot, health)` is deterministic and table-tested. In-flight requests hold an immutable `*Snapshot`, eliminating all lock contention on the hot path.
3. **Integer Micro-USD Accounting**: Float math introduces non-deterministic rounding errors in billing and rate limits. All costs are tracked in integer micro-USD (1 USD = 1,000,000 micros).
4. **Circuit Breaker State Machine**: Consecutive failures trip targets to `StateOpen`, causing `DecideTargets` to immediately prune degraded providers from candidate chains before making network calls. Cooldown timers automatically transition to `StateHalfOpen` for probe traffic recovery.
5. **Atomic Hot Reload**: `Registry.snap` is stored in an `atomic.Pointer[Snapshot]`. Storage change polls detect mutations via content hashing and atomically swap the pointer. Existing goroutines complete on their snapshot safely.
