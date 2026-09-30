# ADR 003: SQLite driver is modernc.org/sqlite (pure Go)

- Status: Accepted (Phase 1, node p1.storage-schema)
- Date: 2026-09-30

## Context

OneGate embeds SQLite as its only datastore. Two viable Go drivers:

- `mattn/go-sqlite3` — cgo bindings to upstream SQLite. Fastest, but cgo.
- `modernc.org/sqlite` — SQLite translated to Go (no cgo).

The product is a **single static binary cross-compiled for
linux/darwin/windows × amd64/arm64** and launched via `npx onegate`
(Phase 9).

## Decision

Use **modernc.org/sqlite v1.34.x** (pinned for Go 1.24 compatibility).

## Consequences

- `CGO_ENABLED=0` builds everywhere: cross-compilation is a plain `GOOS=/
  GOARCH=` invocation — no C toolchains in CI, no glibc/musl matrix, no
  static-linking surprises. This is the single biggest operational win for
  the single-binary story.
- Performance: pure-Go SQLite is slower than native for heavy analytic
  queries. Our load profile is small OLTP writes (one usage row per
  request, batched) and indexed reads — the delta is irrelevant here, and
  Phase 8 load tests will hold the line with evidence.
- Binaries get larger (~10 MB); acceptable against the embedding plan.
- Known upstream quirks (e.g. time handling) are contained behind
  `internal/storage` — the driver never leaks past that package.

## Alternatives considered

- **mattn/go-sqlite3**: fastest, but cgo forfeits trivial cross-compilation
  and complicates the npx distribution path for Windows users.
- **An embedded KV store (bbolt, etc.)**: loses SQL, migrations, and the
  analytics query surface we need for the dashboard.
- **External Postgres**: violates the zero-external-services constraint.
