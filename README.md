# OneGate

**A single-binary LLM gateway. The OmniRoute v3.8.52 experience, rebuilt in Go.**

OneGate proxies OpenAI / Anthropic / Gemini (and OpenAI-compatible providers)
behind one endpoint with virtual keys, model routing, fallback chains, quotas,
usage analytics, and an embedded management dashboard — no Node runtime, no
docker-compose, one static binary.

```
npx onegate          # or: ./onegate serve (CLI reference: docs/cli.md)
```

| | |
|---|---|
| **Gateway** | Go 1.24, stdlib-first, single static binary |
| **Dashboard** | React Router 7 · Tailwind v4 · shadcn/ui (radix-mira, RTL-ready), embedded via `go:embed` |
| **Storage** | Embedded SQLite (WAL) — zero external services |
| **Parity** | Behavioral drop-in for OmniRoute v3.8.52 (see `docs/compat/`) |

## Status

**v1.0.0 shipped** — 10 phases, 86/86 nodes done (see the gate reports in
[`docs/gates/`](docs/gates/)). Live board:

```bash
make graph          # or: python3 scripts/graph_status.py
```

**v2 in planning** — phases 10–15 appended: Dashboard 2.0 (Bifrost-class),
routing intelligence, provider ecosystem, token efficiency, integrations &
safety, ops & multi-tenancy. Plan: [`docs/roadmap-v2.md`](docs/roadmap-v2.md) ·
gap audit: [`docs/research/v2-competitive-analysis.md`](docs/research/v2-competitive-analysis.md).

Roadmap: [`docs/roadmap.md`](docs/roadmap.md) · v1 phases 0–9 done; v2 phases 10–15 pending.

## Quick start (development)

Prereqs: Go 1.24+, Bun 1.1+.

```bash
# gateway
make run            # builds + runs on 127.0.0.1:7420, /healthz for liveness

# dashboard (separate terminal)
make web-install    # first time: bun install in web/
make web-dev        # vite dev server on :5173, proxies /api -> :7420
```

Full production build:

```bash
make web            # dashboard -> web/build
make build          # static binary -> bin/onegate (dashboard embedded in Phase 9)
```

## Repository layout

```
cmd/onegate/          entrypoint (flags, server bootstrap, graceful shutdown)
internal/
  config/             runtime configuration
  domain/             canonical types (Phase 1+)
  protocol/           provider wire-protocol adapters (Phase 2+)
  proxy/              reverse proxy core (Phase 3+)
  routing/            model registry, fallback, health (Phase 4+)
  storage/            SQLite schema, migrations, repos (Phase 1+)
  observability/      logs, metrics, traces (Phase 5+)
  api/                management API for the dashboard (Phase 6+)
  auth/               virtual keys, secrets, sessions (Phase 1+/4+)
  version/            build metadata
web/                  dashboard (React Router 7 + shadcn/ui, bun)
tasks/                phase task graphs — the source of truth for progress
docs/                 roadmap, architecture, graph protocol, research, compat
.claude/agents/       24 agent definitions driving the build workflow
scripts/              graph tooling, load tests, parity + chaos harnesses
```

## Documentation

| Doc | What |
|---|---|
| [Quickstart](docs/quickstart.md) | Routed LLM traffic in five minutes, verified against a clean install (CI re-verifies it every push) |
| [CLI reference](docs/cli.md) | Every command, flag, env var, exit code |
| Guides: [installation](docs/guides/installation.md) · [providers](docs/guides/providers.md) · [routing](docs/guides/routing.md) · [keys](docs/guides/keys.md) · [dashboard](docs/guides/dashboard.md) | Operating OneGate |
| [operations](docs/guides/operations.md) · [troubleshooting](docs/guides/troubleshooting.md) | Running in anger; symptom-first fixes |
| [Upgrading](docs/guides/upgrading.md) · [CHANGELOG](CHANGELOG.md) | Migration paths; what changed per release |
| [Architecture](docs/architecture.md) · [ADR decisions](docs/adr/) · [task graph protocol](docs/graph-engineering.md) | How it is built |

## How this repo is built: graph engineering

Work is organized as **task graphs** (`tasks/phase-N.*.graph.yaml`) — DAGs of
nodes with owners, deliverables, and acceptance criteria. A node moves
`pending → in-progress → review → done` only with evidence. Phases close via
gate nodes; nothing starts before its dependencies are `done`.

- Protocol: [`docs/graph-engineering.md`](docs/graph-engineering.md)
- Agents: [`docs/graph-engineering.md#agents`](docs/graph-engineering.md#agents) (24 charters in `.claude/agents/`)
- Validate: `python3 scripts/graph_status.py --check`

## Contributing

1. `python3 scripts/graph_status.py` — pick an unblocked node.
2. Implement against its acceptance criteria (the owning agent's charter applies).
3. `go vet ./... && go test ./... -race` (Go) or `bun run build && bun run typecheck` (web).
4. Update the node status with evidence; code-reviewer sign-off, then `done`.

## License

[MIT](LICENSE)
