# OneGate Dashboard

Embedded management UI for the [OneGate](../README.md) LLM gateway.

Stack: React Router 7 (framework mode) · Tailwind CSS v4 · shadcn/ui
(radix-mira preset, RTL-ready) · Bun · Vite

## Development

```bash
bun install
bun run dev        # http://localhost:5173, proxies /api -> 127.0.0.1:7420
```

Start the Go gateway in another terminal (`make run` from the repo root), then
open the dashboard — `/api` requests are proxied to the gateway.

## Production

```bash
bun run build      # SSR + client build into build/
bun run start      # serve the SSR build (node)
```

The final release embeds this dashboard into the single Go binary
(go:embed, Phase 9 — `tasks/phase-9.release.graph.yaml`).

## Adding shadcn components

```bash
bunx --bun shadcn@latest add <component>
```
