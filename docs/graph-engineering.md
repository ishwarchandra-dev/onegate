# Graph Engineering Protocol

How OneGate gets built: work is a **task graph**, not a backlog. Each phase is
a DAG in `tasks/phase-N.<name>.graph.yaml`; agents implement nodes; gates
close phases; evidence moves everything.

## File format

```yaml
phase: 3
name: proxy-core
title: "Phase 3 — Reverse Proxy Core & Fallback Chains"
status: pending            # pending | in-progress | done
gate:                      # phase definition-of-done criteria
  - "criterion..."
nodes:
  - id: p3.stream-pipeline          # p<phase>.<slug>, unique across ALL phases
    title: "Human title"
    owner: streaming-engineer       # an agent from .claude/agents/
    status: pending                 # pending | in-progress | review | done | blocked
    depends_on: [p3.upstream-client] # node ids; empty = phase-entry
    deliverables:
      - "concrete artifact paths or descriptions"
    acceptance:
      - "testable criteria; a reviewer can check each one"
```

## The rules

1. **Validation is mechanical.** `python3 scripts/graph_status.py --check`
   enforces: unique IDs, resolvable deps, no cycles, per-phase gate nodes,
   legal statuses, status sanity (nothing is `done` on an undone dependency).
   CI runs it on every push.

2. **Node lifecycle.** `pending → in-progress → review → done`.
   - `in-progress`: an agent holds it; nobody else touches it.
   - `review`: implementation complete, awaiting code-reviewer against the
     acceptance criteria.
   - `done`: acceptance criteria demonstrated with evidence (tests run,
     benchmarks attached, review sign-off recorded).
   - `blocked`: set with a reason in the node; graph-master tracks it.

3. **Dependencies are hard.** A node starts only when every `depends_on` is
   `done`. Cross-phase dependencies must go through the previous phase's gate.

4. **Acceptance criteria are written before work starts** and never edited
   after. If scope changes, supersede the node (`pN.slug-2`) instead of
   moving the goalposts.

5. **Gates close phases.** `pN.gate` depends on all nodes of the phase and
   verifies the phase's `gate:` criteria list. The next phase's first nodes
   depend on the gate.

6. **Evidence or it didn't happen.** "Done" without linked proof is a graph
   violation — graph-master reverts the status.

## Working a node (agent loop)

```
1. graph_status.py                     → find the ready queue (unblocked, pending)
2. claim: status → in-progress
3. read the owning agent charter       → .claude/agents/<owner>.md
4. implement deliverables
5. satisfy acceptance criteria         → run the checks, capture results
6. status → review                     → code-reviewer runs the checklist
7. address findings                    → status → done (graph-master records evidence)
```

## Agents

24 charters live in `.claude/agents/`:

| Group | Agents |
|-------|--------|
| Core engineering | architect, go-engineer, protocol-engineer, streaming-engineer, storage-engineer |
| Frontend | frontend-engineer, design-engineer, api-designer |
| Gateway domain | routing-specialist, rate-limit-specialist, security-engineer, observability-engineer |
| Quality & parity | performance-engineer, compatibility-auditor, migration-engineer, qa-engineer, chaos-engineer |
| Process | code-reviewer, graph-master, devops-engineer, release-manager |
| Support | technical-writer, risk-analyst, research-analyst |

Each charter defines: responsibilities, working rules, outputs, and
**guardrails** (the things the agent must never do). When two charters
conflict, architect arbitrates and records an ADR.

## Board

```bash
make graph                        # full board, all phases
python3 scripts/graph_status.py --phase 3   # one phase
python3 scripts/graph_status.py --check     # CI mode
```

Status icons: `○` pending · `◐` in-progress · `◑` review · `●` done · `✖` blocked
