import type { Config } from "@react-router/dev/config"

export default {
  // SPA mode (p9.embed-decision, ADR 006): the dashboard is fully
  // client-side (no loaders/actions; all data via /api fetches) and must
  // embed into the single Go binary — no Node.js server at runtime.
  // react-router build then emits a static build/ (index.html + assets)
  // that go:embed serves with a catch-all fallback.
  ssr: false,
} satisfies Config
