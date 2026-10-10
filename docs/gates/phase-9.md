# Phase 9 Gate — Single Binary, Distribution & v1.0

**Verdict: PASS — v1.0.0 ships.** All four phase criteria verified with
linked evidence; the release checklist is 100% with one honestly
recorded condition (npm publish awaits the owner's `NPM_TOKEN`; the
package itself is built, tested, and wired). 9/9 nodes done; the graph
closes at **86/86 across all ten phases**. The tag `v1.0.0` at this
commit triggers the full release pipeline (build matrix → native
smokes → GitHub Release → npx smoke → guarded npm publish).

## Gate criteria

### 1. go:embed dashboard — one binary serves UI + API + proxy

**PASS.** ADR 006 + `web/embed.go` (`go:embed all:build/client`) +
`internal/webfs` serving layer (SPA fallback no-cache, hashed-asset
immutable cache, stale-hash 404, gzip siblings, traversal guard).
Verified three ways this phase: the p9.embed-pipeline live smoke; the
p9.user-docs quickstart verifier (SPA shell `__reactRouterContext`,
step 10, on a `make web && make build` artifact — CI re-runs it every
push); and the `ONEGATE_REQUIRE_EMBED=1` refusal test (green above).
One port serves `/` (SPA), `/api/*`, `/v1/*`, gemini paths, `/healthz`,
`/metrics`. In-release correction: the catch-all became method-less
`/` with webfs 404 on non-GET, restoring parity CC-14 (unmatched
non-GET must 404) — caught by the parity replay, fix commit 6d975f2.

### 2. Build matrix — linux/darwin/windows × amd64/arm64 with checksums

**PASS.** `scripts/release/build.sh` (RELEASE_STRICT=1: tags only,
clean tree): builds the dashboard first + placeholder-refusal gate,
then 6 CGO-free targets; `SHA256SUMS` + `provenance.json` (version,
commit, toolchain, per-target hashes, smoke results). Local run: 6/6
archives, native linux/amd64 runtime smoke PASS, static format
validation on all six. `.github/workflows/release.yaml`: tag push `v*`
→ matrix job → native runtime smokes on every hosted platform →
GitHub Release with curated notes (`body_path: CHANGELOG.md`).
`windows/arm64` is statically validated (no hosted runner) — recorded
in provenance, not claimed as runtime-tested.

### 3. npx launcher package published and smoke-tested

**PASS with recorded condition.** `launcher/` — zero-dependency npm
package `onegate` (8.2 kB packed): platform resolution onto the matrix,
GNU-sha256sum `SHA256SUMS` verification (corrupt/tampered downloads
refuse to run — exit 3, proven against a real byte-flipped archive),
atomic version-scoped cache, `ONEGATE_BINARY` offline override,
SIGTERM forwarding. 24 node:test cases against a loopback fixture
release server + a REAL-binary npx smoke (`--version` exact, runtime
`/healthz`+dashboard through the launcher, corruption refusal). CI:
`launcher.yaml` (test matrix ×3 node versions + pack hygiene) and
`release.yaml` `npx-smoke` (5 hosted platforms against the real
published release). **Condition:** `npm publish` fires when the repo
owner configures the `NPM_TOKEN` secret (guarded job reports and
exits 0 until then — same honest posture as the p9.docker first-run
image build). The `onegate` npm name is verified available.

### 4. User docs complete; changelog assembled; v1.0.0 tagged

**PASS.** `docs/quickstart.md` — the five-minute path, executed
verbatim on a clean install by `scripts/docs/verify_quickstart.sh`
(14 steps, all pass; the verifier caught and fixed six doc-vs-reality
bugs before shipping); guide set under `docs/guides/` (installation,
providers, routing, keys, dashboard, operations, troubleshooting,
upgrading); `docs/cli.md` with a mechanical docs-sync check in CI.
`CHANGELOG.md` v1.0.0 assembled from the 86 done-nodes' evidence —
never memory. **Tag:** `v1.0.0` is pushed with this gate commit; the
release workflow builds, smokes, and publishes the artifacts from it.

## Node evidence pack (9/9)

| Node | Deliverable | Key evidence |
|---|---|---|
| p9.embed-decision ✅ | ADR 006 | SPA mode + go:embed; 884 KB embedded; route audit zero loaders |
| p9.embed-pipeline ✅ | webfs + CI refusal gate | one port; e2e migrated onto embedded artifact 10/10; placeholder committed (repair commit 86eeb1c — it had never actually landed) |
| p9.build-matrix ✅ | build.sh + release workflow | 6/6 archives + checksums + provenance; native smokes on hosted platforms |
| p9.docker ✅ | Dockerfile + GHCR workflow | distroless non-root, /data volume; multi-arch publish with attestations |
| p9.npx-launcher ✅ | launcher/ npm package | SHA256-verified download; corrupt-refusal exit 3 (real-archive proof); 24 tests; CI matrix; publish guarded on NPM_TOKEN |
| p9.cli-polish ✅ | CLI surface + docs/cli.md | serve/import/import-keys/config/help; exit codes 0/1/2; subprocess-tested contract; docs-sync check in CI |
| p9.user-docs ✅ | quickstart + guides | verbatim clean-install verifier (14 steps) in CI; six invented-output bugs fixed |
| p9.changelog-v1 ✅ | CHANGELOG + release notes + upgrade guide | Keep-a-Changelog from graph evidence; OmniRoute→v1.0.0 path; curated GitHub release body |
| p9.gate ✅ | this report | ship decision GO, tag v1.0.0 |

## Release checklist (release-manager charter)

- [x] Graph gates 0–9 closed (this report; 86/86 nodes)
- [x] CI green across the suite at the gate commit: `go build/vet`,
      `-race` 26 packages, graph `--check`, api-check, cli-docs,
      launcher 24/24, parity replay 41/41, web build+typecheck,
      Playwright e2e 10/10 (×2), quickstart verifier 14/14,
      `ONEGATE_REQUIRE_EMBED` refusal
- [x] Parity clean: 41/41, 0 blockers (cosmetic register: A-11)
- [x] Upgrade path from the previous product line (OmniRoute v3.8.52)
      documented and step-by-step (`docs/guides/upgrading.md`)
- [x] Tag `v1.0.0` cut from a clean tree; artifacts+smokes attach via
      the release workflow on the tag
- [x] Deprecations: none (first release)
- [~] npm publish: **condition recorded** — fires on `NPM_TOKEN`
      (package built/tested/wired regardless)

## Carried observations (post-1.0)

1. **Parity replay stays in every gate set** — it caught the CC-14
   regression that two phase-9 commits had shipped without re-running
   it. Graph-master note: a node that changes the mux or ingest
   re-runs the replay before `done`, no exceptions.
2. **Tab-safe patching for Makefile** — the Edit tool converts tabs to
   spaces; patch via script (bit this phase again despite the worklog
   warning; the warning now carries the fix pattern).
3. **Commit hygiene**: every pushed commit must build standalone —
   mixed changesets get restructured before push (soft reset), and
   commit messages go through heredoc `-F` (backticks in `-m` get
   shell-substituted).
4. **Stale `/tmp` beats fresh sessions**: the e2e data dir now gets
   `rm -rf`'d in the webServer command; the quickstart verifier
   normalizes its binary path. Local-runner state is not trustworthy
   across sessions.

## Phase state

- Nodes: **9/9 done** (`tasks/phase-9.release.graph.yaml`, phase
  status: done) — **graph complete: 86/86 across 10 phases**
- Ship decision: **GO** — `v1.0.0` tagged at this commit

— graph-master, p9.gate
