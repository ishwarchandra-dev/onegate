# Dashboard tour

The dashboard is embedded in the binary (ADR 006): one port serves it,
the API it talks to, and the proxy. This is a tour of every screen and
what it is for; the E2E suite covers each route, so what is described
here is what is shipped.

Open `http://127.0.0.1:7420/` (or whatever `-host`/`-port` you run).

## First run: setup wizard

With no admin account, the login screen becomes a setup wizard:
create the admin username + password, then log in. Sessions are
in-memory and expire after 24 h — a restart logs the dashboard out
(re-login; nothing is lost). An existing account turns the wizard off;
`POST /api/auth/setup` then reports `created:false`.

Login is also possible with the admin token
(`ONEGATE_ADMIN_TOKEN`) — useful for scripted setups.

## Layout

A sidebar with six sections; on narrow screens it collapses into a
menu. Right-to-left languages are supported (logical CSS properties
throughout) and every view has loading skeletons, empty states with
call-to-action, and error states. Charts follow the design system's
8-hue categorical palette with strict WCAG AA contrast in both light
and dark mode.

## Providers

- List with masked keys (`sk-***`), enable/disable switches (optimistic
  UI, rolled back on failure), and Test — a live probe that reports
  per-protocol errors (e.g. `401 invalid x-api-key`).
- Add/edit dialogs write through the API; the key field is write-only.

## Routing

- Model cards per canonical model: aliases, targets with chain order,
  and live **health badges** (circuit state per target).
- The chain editor validates both client-side and server-side (the
  server re-validates; a client bypass gets the same rejection).
- Routing rules: policy picker (`ordered`/`weighted`/`cost`/`latency`),
  enable/disable, ordering.

## Keys

- Mint with scopes/limits; the raw `ogk-…` is shown **once** in a
  dialog with copy-to-clipboard and a warning that it will never be
  shown again (the table shows masked values only).
- Revoke / delete per key; usage per key over time.

## Usage

- Summary cards (requests, tokens, cost, latency) over a selectable
  time range, zero-filled buckets, series toggles, and CSV export.
- Charts are rollup-backed (hourly aggregates) — fast over long ranges;
  the raw request list with per-request details (model served,
  provider, TTFT, status) is beside them.

## Logs

- A live SSE tail (filters by level/trace), a **pause** that keeps the
  connection but stops rendering, and a dropped-events indicator when
  your browser tab throttles.
- Trace drawer: every request ID links to its full trace (all attempts
  with timings, per-target errors).

## Settings

Read-only view of the running configuration (bind address, data dir,
log level, HTTP timeouts, reload settings, schema version, uptime) —
the source of truth for "what is this thing actually running with".
Change configuration via flags/env/config-file and (with
`reload.enabled`) hot-reload it; see [operations](operations.md).

## Tips

- The dashboard never talks to providers — only to `/api/*` on the
  same origin. The Test button probes go through the gateway's
  SSRF-guarded client.
- Everything on screen is also an API call — the generated TS client
  mirrors `docs/api/openapi.yaml` 1:1, so anything you can click you
  can also script.
