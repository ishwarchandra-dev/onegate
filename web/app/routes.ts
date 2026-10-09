import { type RouteConfig, index, layout, route } from "@react-router/dev/routes"

export default [
  index("routes/login.tsx"),
  layout("routes/_dashboard.tsx", [
    route("providers", "routes/providers.tsx"),
    route("routing", "routes/routing.tsx"),
    route("keys", "routes/keys.tsx"),
    route("usage", "routes/usage.tsx"),
    route("logs", "routes/logs.tsx"),
    route("settings", "routes/settings.tsx"),
  ]),
] satisfies RouteConfig
