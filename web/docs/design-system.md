# OneGate Dashboard Design System

Single source of truth for the dashboard's visual language. Tokens live in
`web/app/app.css` (`:root` light, `.dark` dark) — change them there, never
in components. This document defines how to use them.

Owned by the `design-engineer` charter (`p6.design-system`).

## Tokens

### Surfaces & text

| Token | Role |
| --- | --- |
| `--background` / `--foreground` | Page base |
| `--card` / `--card-foreground` | Cards, tables, panels |
| `--popover` / `--popover-foreground` | Menus, popovers, tooltips |
| `--muted` / `--muted-foreground` | Secondary surfaces / secondary text |
| `--primary` / `--primary-foreground` | Brand green — primary actions, links |
| `--destructive` | Errors, revoke, circuit **open** |
| `--success` | Healthy, circuit **closed** |
| `--warning` | Degraded, circuit **half_open**, stream gaps |
| `--skeleton` | Loading placeholder blocks (`.og-skeleton`) |

Status colors are used either as solid text/icon color on the page
background, or as `bg-{status}/10` tint with the solid color as text —
both pairings are verified AA (below).

### Categorical chart palette

8 hues chosen for pairwise distinguishability; `chart-1` is the brand green
so single-series charts feel on-brand. Series beyond 8 cycle back to
`chart-1`.

| Token | Hue | Typical use |
| --- | --- | --- |
| `--chart-1` | green | primary series / totals |
| `--chart-2` | blue | secondary series |
| `--chart-3` | violet | third series |
| `--chart-4` | amber | attention series (fallbacks) |
| `--chart-5` | cyan | fifth series |
| `--chart-6` | rose | error share / failure series |
| `--chart-7` | indigo | seventh series |
| `--chart-8` | lime | eighth series |

Chart chrome: `--chart-grid` (gridlines), `--chart-axis` (axis text = same
AA class as body text), `--chart-tooltip` / `--chart-tooltip-foreground`
(tooltip is inverted: dark on light mode, light on dark mode),
`--chart-crosshair`.

### Chart language

- **Axis labels**: `text-muted-foreground` at 11px, `tabular-nums` for all
  numeric axes (`.tnum`).
- **Legend**: row of color dots (8px) + label, above the plot, start-aligned.
  Never rely on color alone — pair with the series name.
- **Time axis**: Unix-ms input, rendered in the viewer's locale; hour steps
  for <=48h windows, day steps above.
- **Empty buckets**: absent rows from `/api/usage/range` are zero-filled by
  the client so charts never show gaps that look like errors.
- **Data-table fallback**: every chart offers a keyboard-accessible data
  table (visually collapsed, expandable) — the accessibility fallback the
  charter requires.

### Density contract

| Context | Density |
| --- | --- |
| Tables (providers, keys, requests) | compact — 28px rows, `text-xs` |
| Forms (add/edit dialogs) | comfortable — 8px field gaps, 20px section gaps |
| Analytics (usage view) | airy — 24px card padding, generous plot margins |

## Loading & empty states — designed, not improvised

Loading skeletons (`web/app/components/skeletons.tsx`) mirror the final
layout so nothing jumps:

| Component | Use |
| --- | --- |
| `TableSkeleton` | list views (providers, keys, routing rules, requests) |
| `StatCardSkeleton` | KPI tiles |
| `ChartSkeleton` | analytics plots (axis rails + bars) |
| `FormSkeleton` | add/edit dialogs |
| `LogStreamSkeleton` | live logs view |
| `PageSkeleton` | route-level data loading |

Empty & error states (`web/app/components/states.tsx`):

| Component | Use |
| --- | --- |
| `EmptyState` | no data yet — icon, title, description, optional CTA |
| `ErrorState` | API failure — always offers Retry |
| `StreamGapIndicator` | SSE backpressure drops — visible gap, never a freeze |

Skeleton animation is an opacity pulse (`.og-skeleton` in `app.css`):
direction-neutral by construction, and disabled under
`prefers-reduced-motion`.

## Dark / light parity

- Both blocks in `app.css` define **exactly the same token set** —
  enforced by `scripts/check_contrast.py` (parity check).
- Components never read mode; they read tokens. A view that looks right in
  one mode but not the other is a token bug, not a component fix.
- The chart tooltip inverts (`--chart-tooltip` is dark-on-light-mode,
  light-on-dark) to keep AA in both modes.

## Accessibility (WCAG AA evidence)

`scripts/check_contrast.py` parses `app.css`, converts oklch to sRGB, and
verifies every committed pairing. Current result: **56/56 pass** — summary:

| Pairing class | Requirement | Result |
| --- | --- | --- |
| Body/secondary text on background & card | >= 4.5:1 | pass (both modes) |
| Status text (success/warning/destructive) on background | >= 4.5:1 | pass (both modes) |
| Filled status surfaces (fg on success/warning/primary) | >= 4.5:1 | pass (both modes) |
| Chart series on card (WCAG 1.4.11 non-text) | >= 3:1 | pass (both modes, all 8 hues) |
| Focus ring on background | >= 3:1 | pass (both modes) |

Additional rules:

- Interactive elements always show a visible focus ring
  (`focus-visible:ring-*`); never remove outlines without replacement.
- Icons are decorative (`aria-hidden`) with text labels alongside.
- Directional icons are marked `data-icon="directional"` and mirrored with
  `rtl:rotate-180`.

## RTL rules (RTL-safe patterns)

The dashboard must read correctly in both directions (phase-6 gate:
"Both LTR and RTL layouts verified"). `components.json` sets `rtl: true`.

1. **Logical properties only.** Never `pl/pr/ml/mr/left/right` — use
   `ps/pe/ms/me`, `text-start/text-end`, `border-s/border-e`,
   `rounded-s/rounded-e`. Tailwind v4 maps these to CSS logical
   properties, which mirror automatically under `dir="rtl"`.
2. **No directional gradients or transforms** for decorative motion. The
   skeleton pulse is opacity-based for exactly this reason.
3. **Directional icons** (arrows, chevrons): add
   `rtl:rotate-180` (or `rtl:-scale-x-100`) so "next" points the way the
   text reads.
4. **Charts stay LTR.** Time axes are always start=oldest → end=newest
   reading direction; this is a data convention, not a text convention —
   do NOT mirror plot areas.
5. **Numbers never mirror.** Wrap numerics in `.tnum` (tabular-nums);
   `onegate` IDs, costs, and latencies render identically in both modes.
6. **Testing**: wrap any view in `<div dir="rtl">` during development; the
   E2E suite (p6.e2e-dashboard) runs an RTL pass on the primary views.

## Iconography

Lucide (`lucide-react`) — already a dependency. Never introduce a second
icon set. Sizes: 3.5–4px inside buttons (matches `button.tsx` slots),
5px inside state icons, 5–6px in navigation.

## Fonts

`Inter Variable` (`@fontsource-variable/inter`) for everything; headings
reuse the same family (`--font-heading: var(--font-sans)`). Numeric data
uses `tabular-nums` via the `.tnum` utility. No additional fonts without a
design review.
