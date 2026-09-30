# ADR 002: Configuration format is JSON

- Status: Accepted (Phase 1, node p1.config-load)
- Date: 2026-09-30

## Context

OneGate needs a config file for host/port, data directory, log level, and
hot-reload settings. Candidates: JSON, YAML, TOML. OmniRoute v3.8.52 used
`omniroute.json`.

## Decision

Native config is **JSON** (`onegate.json`), loaded with `encoding/json`.

## Consequences

- **Zero new dependencies** — the stdlib parser is battle-tested and our
  "stdlib-first" rule stays intact (YAML/TOML would each add a dependency).
- **Parity by construction**: `onegate import` (Phase 7) copies OmniRoute
  config rather than converting it.
- `json.SyntaxError` carries byte offsets, which we translate to
  line:column for precise error messages.
- Partial files override only the fields they mention (pointer-field
  overlay), so a minimal config stays minimal.
- Unknown fields are ignored for forward compatibility (newer files on
  older binaries keep working).
- Human-friendliness of YAML is conceded; mitigations: strict schema,
  field-path validation errors, and a generated reference (docs).

## Alternatives considered

- **YAML**: friendlier to hand-edit, but new dependency + famously quirky
  parsing (Norway problem, implicit typing). Not worth it for ~6 fields.
- **TOML**: good fit, still a dependency and no parity story.
- **Env-only**: too opaque for a gateway that owns a data directory.
