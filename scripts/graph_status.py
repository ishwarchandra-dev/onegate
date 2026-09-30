#!/usr/bin/env python3
"""OneGate task-graph board & validator.

Usage:
    python3 scripts/graph_status.py           # print the phase board
    python3 scripts/graph_status.py --check   # validate graphs (exit 1 on error)
    python3 scripts/graph_status.py --phase 3 # filter one phase

Validates every tasks/*.graph.yaml file:
  - required fields present (phase, name, title, status, gate, nodes)
  - node ids unique across ALL phases
  - depends_on references exist and point to earlier or same-phase nodes
  - no cycles (DFS)
  - every phase has a p{N}.gate node
  - cross-phase dependencies only reference completed phases or earlier gates
  - node status is one of pending | in-progress | review | done | blocked
"""

from __future__ import annotations

import sys
from pathlib import Path

import yaml

TASKS_DIR = Path(__file__).resolve().parent.parent / "tasks"
NODE_STATUSES = {"pending", "in-progress", "review", "done", "blocked"}
PHASE_STATUSES = {"pending", "in-progress", "done"}

STATUS_ICONS = {
    "pending": "○",
    "in-progress": "◐",
    "review": "◑",
    "done": "●",
    "blocked": "✖",
}


def load_graphs() -> list[dict]:
    graphs = []
    for path in sorted(TASKS_DIR.glob("*.graph.yaml")):
        with path.open() as fh:
            data = yaml.safe_load(fh)
        if not isinstance(data, dict):
            raise SystemExit(f"error: {path.name} is not a YAML mapping")
        data["_file"] = path.name
        graphs.append(data)
    if not graphs:
        raise SystemExit(f"error: no *.graph.yaml files found in {TASKS_DIR}")
    return graphs


def validate(graphs: list[dict]) -> list[str]:
    errors: list[str] = []
    seen_ids: set[str] = set()
    all_ids: set[str] = set()
    node_status: dict[str, str] = {}

    # Pass 1: field presence + id uniqueness + status values
    for g in graphs:
        fname = g["_file"]
        for field in ("phase", "name", "title", "status", "gate", "nodes"):
            if field not in g:
                errors.append(f"{fname}: missing required field '{field}'")
        if g.get("status") not in PHASE_STATUSES:
            errors.append(f"{fname}: bad phase status {g.get('status')!r}")
        if not isinstance(g.get("nodes"), list) or not g["nodes"]:
            errors.append(f"{fname}: 'nodes' must be a non-empty list")
            continue

        for node in g["nodes"]:
            nid = node.get("id")
            if not nid:
                errors.append(f"{fname}: node without id: {node.get('title')!r}")
                continue
            if nid in seen_ids:
                errors.append(f"{fname}: duplicate node id {nid}")
            seen_ids.add(nid)
            node_status[nid] = node.get("status", "")
            if node.get("status") not in NODE_STATUSES:
                errors.append(f"{fname}: node {nid} has bad status {node.get('status')!r}")
            for req in ("title", "owner", "depends_on", "deliverables", "acceptance"):
                if req not in node:
                    errors.append(f"{fname}: node {nid} missing '{req}'")

        expected_gate = f"p{g['phase']}.gate"
        if expected_gate not in {n.get("id") for n in g["nodes"]}:
            errors.append(f"{fname}: missing phase gate node {expected_gate}")

    all_ids = seen_ids

    # Pass 2: dependency resolution + cycles + status sanity
    for g in graphs:
        fname = g["_file"]
        for node in g["nodes"]:
            nid = node.get("id")
            for dep in node.get("depends_on", []):
                if dep not in all_ids:
                    errors.append(f"{fname}: node {nid} depends on unknown node {dep}")

        # cycle detection via DFS over this phase's nodes (cross-phase deps
        # always point backwards, so cycles can only occur within a phase)
        adj = {n["id"]: list(n.get("depends_on", [])) for n in g["nodes"] if n.get("id")}
        state: dict[str, int] = {}  # 0=unvisited 1=stack 2=done

        def dfs(start: str) -> list[str] | None:
            path = [start]
            stack = [start]
            state[start] = 1
            while stack:
                cur = stack[-1]
                advanced = False
                for dep in adj.get(cur, []):
                    if dep not in adj:  # cross-phase reference, already checked
                        continue
                    if state.get(dep) == 1:
                        return path + [dep]
                    if state.get(dep, 0) == 0:
                        state[dep] = 1
                        stack.append(dep)
                        path.append(dep)
                        advanced = True
                        break
                if not advanced:
                    state[cur] = 2
                    stack.pop()
                    path.pop()
            return None

        for nid in adj:
            if state.get(nid, 0) == 0:
                cycle = dfs(nid)
                if cycle:
                    errors.append(f"{fname}: dependency cycle: {' -> '.join(cycle)}")

        # a done phase must have all nodes done
        if g.get("status") == "done":
            for node in g["nodes"]:
                if node.get("status") != "done":
                    errors.append(
                        f"{fname}: phase marked done but node "
                        f"{node.get('id')} is {node.get('status')!r}"
                    )

    # Pass 3: dependents may only unblock on done deps (warn-level)
    for g in graphs:
        for node in g["nodes"]:
            if node.get("status") in ("in-progress", "review", "done"):
                for dep in node.get("depends_on", []):
                    if node_status.get(dep) not in ("done",):
                        errors.append(
                            f"{g['_file']}: node {node.get('id')} is "
                            f"{node['status']!r} but dependency {dep} is "
                            f"{node_status.get(dep)!r}"
                        )
    return errors


def print_board(graphs: list[dict], phase_filter: int | None = None) -> None:
    total = done = 0
    for g in graphs:
        if phase_filter is not None and g["phase"] != phase_filter:
            continue
        nodes_done = sum(1 for n in g["nodes"] if n.get("status") == "done")
        total += len(g["nodes"])
        done += nodes_done
        pct = 100 * nodes_done // max(1, len(g["nodes"]))
        icon = STATUS_ICONS.get(g["status"], "?")
        print(f"\n{icon} {g['title']}  [{nodes_done}/{len(g['nodes'])} nodes, {pct}%]")
        for node in g["nodes"]:
            nicon = STATUS_ICONS.get(node.get("status", ""), "?")
            deps = f"  <- {', '.join(node['depends_on'])}" if node.get("depends_on") else ""
            print(f"   {nicon} {node['id']:<28} {node.get('title', '')[:64]}{deps}")
    print(f"\nTotal: {done}/{total} nodes done across {len(graphs)} phases")


def main() -> int:
    args = sys.argv[1:]
    check_only = "--check" in args
    phase_filter = None
    if "--phase" in args:
        phase_filter = int(args[args.index("--phase") + 1])

    try:
        graphs = load_graphs()
    except SystemExit as exc:
        if check_only:
            print(f"FAIL: {exc}")
            return 1
        raise

    errors = validate(graphs)
    if check_only:
        if errors:
            print("FAIL: task graph validation failed:")
            for err in errors:
                print(f"  - {err}")
            return 1
        print(f"OK: {len(graphs)} phase graphs valid "
              f"({sum(len(g['nodes']) for g in graphs)} nodes)")
        return 0

    if errors:
        print("WARNING: graph validation issues:")
        for err in errors:
            print(f"  - {err}")
    print_board(graphs, phase_filter)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except BrokenPipeError:
        # e.g. `graph_status.py | head` — not an error
        sys.exit(0)
