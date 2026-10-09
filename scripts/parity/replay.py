#!/usr/bin/env python3
"""OneGate parity replay harness (p7.replay-harness).

Replays the captured OmniRoute v3.8.52 corpus (test/parity/corpus.json)
against a real OneGate binary backed by two mock providers, diffs every
response against the captured OmniRoute transcript, classifies the diffs
(blocker / cosmetic per case), and writes a markdown report.

Stdlib only. Usage:

    python3 scripts/parity/replay.py [--only CC-01,CC-02] [--skip-build] [--keep]

Exit codes: 0 = no unexplained blocker diffs; 1 = blocker diffs present
(or harness failure); 2 = environment error.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
CORPUS = os.path.join(REPO, "test", "parity", "corpus.json")
REPORT = os.path.join(REPO, "docs", "reports", "parity-replay.md")
ADMIN_TOKEN = "parity-admin-token"
ANY = "<any>"


# ---------------------------------------------------------------------------
# Process management
# ---------------------------------------------------------------------------

def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Proc:
    def __init__(self, name: str, cmd: list[str], log, env: dict | None = None):
        self.name = name
        full_env = {**os.environ, **(env or {})}
        self.proc = subprocess.Popen(cmd, stdout=log, stderr=subprocess.STDOUT,
                                     cwd=REPO, env=full_env)

    def stop(self):
        if self.proc.poll() is None:
            self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait(timeout=5)


def wait_health(base: str, timeout_s: float = 20.0) -> bool:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(base + "/healthz", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            time.sleep(0.25)
    return False


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

def http_json(method: str, url: str, body=None, headers=None, timeout=60):
    """Perform one HTTP exchange. NEVER raises on HTTP error statuses —
    a replay harness must diff 4xx/5xx bodies, not crash on them."""
    data = None
    hdrs = {"Content-Type": "application/json"}
    if headers:
        hdrs.update(headers)
    if body is not None:
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    try:
        r = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()
    with r:
        raw = r.read()
        return r.status, dict(r.headers), raw


def api(method: str, base: str, path: str, body=None):
    """Management-API call with admin-token auth. Retries on the fixed-window
    rate limit (write class: 30/min) by waiting for the window to roll over."""
    deadline = time.time() + 180
    while True:
        status, headers, raw = http_json(
            method, base + path, body,
            headers={"Authorization": f"Bearer {ADMIN_TOKEN}"},
        )
        if status == 429 and time.time() < deadline:
            # Fixed one-minute windows: sleep until the next boundary.
            now_ms = time.time() * 1000
            wait_s = (60000 - (now_ms % 60000)) / 1000.0 + 0.5
            print(f"  (rate-limited on {method} {path}; waiting {wait_s:.0f}s for window)")
            time.sleep(wait_s)
            continue
        if status >= 400:
            raise RuntimeError(f"management API {method} {path} -> {status}: {raw[:400]!r}")
        return json.loads(raw) if raw else {}


# ---------------------------------------------------------------------------
# Placeholder substitution
# ---------------------------------------------------------------------------

class Ctx:
    def __init__(self, keys: dict, mock_a: str, mock_b: str, base: str):
        self.keys = keys
        self.mock_a = mock_a
        self.mock_b = mock_b
        self.base = base


def substitute(value, ctx: Ctx):
    """Replace corpus placeholders. Pure values keep their type (ints for
    timestamps); placeholders embedded inside larger strings (e.g.
    "Bearer {{KEY:primary}}") are substituted textually."""
    if isinstance(value, str):
        if value.startswith("{{KEY:") and value.endswith("}}"):
            return ctx.keys[value[6:-2]]
        if value == "{{MOCK_A}}":
            return ctx.mock_a
        if value == "{{MOCK_B}}":
            return ctx.mock_b
        if value.startswith("{{NOW-") and value.endswith("}}"):
            ms = int(value[6:-2])
            return int(time.time() * 1000) - ms
        if "{{" in value:
            def repl(m):
                inner = m.group(0)
                got = substitute(inner, ctx)
                return got if isinstance(got, str) else str(got)
            return re.sub(r"\{\{[^}]+\}\}", repl, value)
        return value
    if isinstance(value, list):
        return [substitute(v, ctx) for v in value]
    if isinstance(value, dict):
        return {k: substitute(v, ctx) for k, v in value.items()}
    return value


# ---------------------------------------------------------------------------
# Diffing
# ---------------------------------------------------------------------------

def diff_json(expected, actual, path="$") -> list[str]:
    """Wildcard-aware deep compare. Returns a list of human diffs."""
    out: list[str] = []
    if isinstance(expected, str) and expected == ANY:
        return out
    if isinstance(expected, dict) and isinstance(actual, dict):
        for k, v in expected.items():
            if k not in actual:
                out.append(f"{path}.{k}: missing (expected {json.dumps(v)[:120]})")
            else:
                out += diff_json(v, actual[k], f"{path}.{k}")
        return out
    if isinstance(expected, list) and isinstance(actual, list):
        if len(expected) != len(actual):
            out.append(f"{path}: length {len(actual)} != expected {len(expected)}")
        for i, (e, a) in enumerate(zip(expected, actual)):
            out += diff_json(e, a, f"{path}[{i}]")
        return out
    if expected != actual:
        out.append(f"{path}: {json.dumps(actual)[:120]} != expected {json.dumps(expected)[:120]}")
    return out


def parse_sse(text: str) -> list[dict]:
    frames = []
    for block in text.split("\n\n"):
        block = block.strip("\r")
        if not block:
            continue
        frame = {"event": None, "data": None}
        for line in block.split("\n"):
            if line.startswith(":"):
                continue  # comment frame (SSE greeting etc.)
            if line.startswith("event:"):
                frame["event"] = line[6:].strip()
            elif line.startswith("data:"):
                frame["data"] = line[5:].strip()
        if frame["event"] is not None or frame["data"] is not None:
            frames.append(frame)
    return frames


def check_headers(expected: dict, actual: dict) -> list[str]:
    out = []
    low = {k.lower(): v for k, v in actual.items()}
    for k, v in expected.items():
        got = low.get(k.lower())
        if got is None:
            out.append(f"header {k}: missing")
        elif v != ANY and got != v:
            out.append(f"header {k}: {got!r} != expected {v!r}")
    return out


# ---------------------------------------------------------------------------
# Request execution
# ---------------------------------------------------------------------------

def execute_step(step: dict, ctx: Ctx) -> dict:
    """Send one request. Returns response info (or {aborted: true})."""
    req = substitute(step["request"], ctx)
    method = req.get("method", "POST")
    path = req["path"]
    headers = {k: v for k, v in req.get("headers", {}).items()}

    body_bytes = None
    if "raw_body" in req:
        body_bytes = req["raw_body"].encode()
    elif "oversize_body_bytes" in req:
        n = req["oversize_body_bytes"]
        body_bytes = (
            b'{"model":"gpt-4o","messages":[{"role":"user","content":"'
            + b"a" * (n - 56)
            + b'"}]}'
        )
    elif "body" in req:
        body_bytes = json.dumps(req["body"]).encode()

    # abort_after_ms is a step-level directive (not forwarded upstream):
    # send the request over a raw socket, then disconnect mid-flight.
    if "abort_after_ms" in step:
        abort_request(ctx.base, method, path, headers, body_bytes, step["abort_after_ms"])
        return {"aborted": True}

    url = ctx.base + path
    hdrs = dict(headers)
    if body_bytes is not None:
        hdrs.setdefault("Content-Type", "application/json")
    status, resp_headers, raw = http_json(method, url, body_bytes, hdrs)
    return {
        "status": status,
        "headers": resp_headers,
        "body": raw.decode("utf-8", "replace"),
    }


def abort_request(base: str, method: str, path: str, headers: dict, body, after_ms: int):
    """Send a request over a raw socket, then close mid-flight."""
    hostport = base.split("//", 1)[1]
    host, port = hostport.split(":")
    s = socket.create_connection((host, int(port)), timeout=10)
    try:
        lines = [f"{method} {path} HTTP/1.1", f"Host: {hostport}"]
        for k, v in headers.items():
            lines.append(f"{k}: {v}")
        if body is not None:
            lines.append("Content-Type: application/json")
            lines.append(f"Content-Length: {len(body)}")
        lines.append("Connection: close")
        payload = ("\r\n".join(lines) + "\r\n\r\n").encode() + (body or b"")
        s.sendall(payload)
        time.sleep(after_ms / 1000.0)
    finally:
        s.close()


# ---------------------------------------------------------------------------
# Expectation checking
# ---------------------------------------------------------------------------

def check_expect(expect: dict, info: dict) -> list[str]:
    errors = []
    if "status" in expect and info["status"] != expect["status"]:
        errors.append(f"status: {info['status']} != expected {expect['status']}")
    if "headers" in expect:
        errors += check_headers(expect["headers"], info["headers"])
    for h in expect.get("headers_absent", []):
        if any(k.lower() == h.lower() for k in info["headers"]):
            errors.append(f"header {h}: present but must be absent")
    if "json" in expect:
        try:
            actual = json.loads(info["body"])
        except json.JSONDecodeError as e:
            errors.append(f"body is not JSON: {e}: {info['body'][:200]!r}")
        else:
            errors += diff_json(expect["json"], actual)
    if "sse" in expect:
        actual_frames = parse_sse(info["body"])
        expected_frames = expect["sse"]
        if len(actual_frames) != len(expected_frames):
            errors.append(
                f"sse: {len(actual_frames)} frames != expected {len(expected_frames)}"
            )
        for i, exp in enumerate(expected_frames):
            if i >= len(actual_frames):
                break
            act = actual_frames[i]
            if exp.get("event") is not None and act["event"] != exp["event"]:
                errors.append(f"sse[{i}].event: {act['event']!r} != expected {exp['event']!r}")
            ed, ad = exp.get("data"), act.get("data")
            if ed is not None:
                if isinstance(ed, str) and ed == ad:
                    continue
                try:
                    aj = json.loads(ad) if ad is not None else None
                except (json.JSONDecodeError, TypeError):
                    errors.append(f"sse[{i}].data: {ad!r} is not JSON, expected {ed!r}")
                    continue
                errors += diff_json(ed, aj, f"sse[{i}].data")
        for forbidden in expect.get("sse_absent_data", []):
            if any(f.get("data") == forbidden for f in actual_frames):
                errors.append(f"sse: forbidden frame data {forbidden!r} present")
    return errors


# ---------------------------------------------------------------------------
# Case execution
# ---------------------------------------------------------------------------

def run_step(step: dict, ctx: Ctx) -> tuple[str, list[str], dict | None]:
    """Execute one step. Returns (name, errors, response-info)."""
    name = step.get("name", "step")
    if "expect" not in step:
        execute_step(step, ctx)
        return name, [], None
    info = execute_step(step, ctx)
    if info.get("aborted"):
        return name, [], None
    return name, check_expect(step["expect"], info), info


def run_post(post: dict, ctx: Ctx) -> list[str]:
    post = substitute(post, ctx)
    deadline = time.time() + post.get("retry_ms", 3000) / 1000.0
    last = ["post-check: record not found"]
    while time.time() < deadline:
        try:
            page = api("GET", ctx.base, post["path"])
        except RuntimeError as e:
            last = [f"post-check: {e}"]
            time.sleep(0.25)
            continue
        for item in page.get("items", []):
            if all(item.get(k) == v for k, v in post["where"].items()):
                diffs = diff_json(post["expect"], item, "post")
                if not diffs:
                    return []
                last = diffs
        time.sleep(0.25)
    return last


def run_case(case: dict, ctx: Ctx) -> dict:
    result = {"id": case["id"], "title": case["title"], "rows": case.get("rows", []),
              "severity": case.get("severity", "blocker"), "steps": [], "ok": True}
    steps = case.get("steps", [])
    if case.get("concurrent"):
        results: list = [None] * len(steps)

        def worker(i, step):
            results[i] = run_step(step, ctx)

        threads = [threading.Thread(target=worker, args=(i, s)) for i, s in enumerate(steps)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        # Concurrent expectations match as a SET: each step's expect may be
        # satisfied by ANY response (which request wins the race is
        # nondeterministic). Leftover responses carry the reported errors.
        remaining = [r for r in results if r is not None]
        for step in steps:
            expect = step.get("expect")
            if expect is None:
                if remaining:
                    remaining.pop(0)
                result["steps"].append({"name": step.get("name", "step"), "ok": True})
                continue
            match = None
            for r in remaining:
                if r[2] is not None and not check_expect(expect, r[2]):
                    match = r
                    break
            if match is not None:
                remaining.remove(match)
                result["steps"].append({"name": step.get("name", "step"), "ok": True})
            else:
                errs = remaining[0][1] if remaining else ["no matching response"]
                if remaining:
                    remaining.pop(0)
                result["steps"].append({"name": step.get("name", "step"), "ok": False,
                                        "errors": errs})
                result["ok"] = False
    else:
        for step in steps:
            name, errs, _ = run_step(step, ctx)
            entry = {"name": name, "ok": not errs}
            if errs:
                entry["errors"] = errs
                result["ok"] = False
            result["steps"].append(entry)
    if "post" in case and result["ok"]:
        errs = run_post(case["post"], ctx)
        if errs:
            result["steps"].append({"name": "post-check", "ok": False, "errors": errs})
            result["ok"] = False
        else:
            result["steps"].append({"name": "post-check", "ok": True})
    return result


# ---------------------------------------------------------------------------
# Seeding
# ---------------------------------------------------------------------------

def seed(corpus: dict, ctx: Ctx, log: list[str]):
    spec = corpus["seed"]
    for p in spec["providers"]:
        body = substitute(p, ctx)
        api("POST", ctx.base, "/api/providers", body)
        log.append(f"provider {body['id']} -> {body['base_url']}")
    for m in spec["models"]:
        body = {
            "id": m["id"],
            "aliases": m.get("aliases", []),
            "targets": m["targets"],
            "capabilities": m.get("capabilities",
                                  {"tools": True, "vision": True, "json_mode": True, "stream": True}),
        }
        api("POST", ctx.base, "/api/models", body)
        api("POST", ctx.base, "/api/routing-rules", {
            "model_id": m["id"], "policy": m.get("policy", "ordered"),
            "enabled": True, "position": 1,
        })
        log.append(f"model {m['id']} ({len(m['targets'])} targets, {m.get('policy', 'ordered')})")
    for k in spec["keys"]:
        body = {
            "name": k["name"],
            "scopes": substitute(k.get("scopes"), ctx) if k.get("scopes") else None,
            "limits": k.get("limits"),
        }
        if "expires_at_ms" in k:
            body["expires_at_ms"] = substitute(k["expires_at_ms"], ctx)
        created = api("POST", ctx.base, "/api/keys", body)
        ctx.keys[k["id"]] = created["raw_key"]
        if k.get("revoke"):
            api("POST", ctx.base, f"/api/keys/{created['key']['id']}/revoke")
        log.append(f"key {k['id']} -> {created['key']['id']}")


# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

def write_report(results: list[dict], seed_log: list[str], elapsed: float) -> int:
    total = len(results)
    passed = sum(1 for r in results if r["ok"])
    failed = [r for r in results if not r["ok"]]
    blockers = [r for r in failed if r["severity"] == "blocker"]
    cosmetic = [r for r in failed if r["severity"] != "blocker"]
    now = _dt.datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    lines = [
        "# Parity Replay Report",
        "",
        f"Generated {now} by `scripts/parity/replay.py` (p7.replay-harness).",
        "",
        f"- Cases: **{passed}/{total} passing**",
        f"- Blocker diffs: **{len(blockers)}**",
        f"- Cosmetic diffs: {len(cosmetic)}",
        f"- Elapsed: {elapsed:.1f}s",
        "",
        "## Environment",
        "",
        "```",
        *seed_log,
        "```",
        "",
        "## Matrix",
        "",
        "| Case | Rows | Severity | Result |",
        "| --- | --- | --- | --- |",
    ]
    for r in results:
        verdict = "PASS" if r["ok"] else "FAIL"
        lines.append(f"| {r['id']} | {', '.join(r['rows'])} | {r['severity']} | {verdict} |")
    if failed:
        lines += ["", "## Failures", ""]
        for r in failed:
            lines += [f"### {r['id']} — {r['title']}", ""]
            for s in r["steps"]:
                if s["ok"]:
                    continue
                lines += [f"**{s['name']}**", ""]
                for e in s.get("errors", [])[:12]:
                    lines.append(f"- `{e}`")
                lines.append("")
    os.makedirs(os.path.dirname(REPORT), exist_ok=True)
    with open(REPORT, "w") as f:
        f.write("\n".join(lines).rstrip() + "\n")
    return len(blockers)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def go_bin() -> str:
    """Locate the go tool: PATH first, then common install locations."""
    for cand in (shutil.which("go"),
                 os.path.expanduser("~/local/go/bin/go"),
                 "/usr/local/go/bin/go"):
        if cand and os.path.exists(cand):
            return cand
    raise RuntimeError("go toolchain not found (PATH or ~/local/go/bin/go)")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", help="comma-separated case ids")
    ap.add_argument("--skip-build", action="store_true")
    ap.add_argument("--keep", action="store_true", help="keep the scratch dir")
    args = ap.parse_args()

    corpus = json.load(open(CORPUS))
    cases = corpus["cases"]
    if args.only:
        wanted = {c.strip() for c in args.only.split(",")}
        cases = [c for c in cases if c["id"] in wanted]

    scratch = tempfile.mkdtemp(prefix="onegate-parity-")
    print(f"scratch: {scratch}")
    procs: list[Proc] = []
    try:
        if not args.skip_build:
            print("building binaries...")
            env = {**os.environ, "CGO_ENABLED": "0", "PATH": os.environ.get("PATH", "")}
            gobin = go_bin()
            for target, out in (("./cmd/onegate", "onegate"), ("./cmd/mockprovider", "mockprovider")):
                subprocess.run([gobin, "build", "-o", os.path.join(scratch, out), target],
                               cwd=REPO, check=True, env=env)

        port_a, port_b, port_gw = free_port(), free_port(), free_port()
        log_a = open(os.path.join(scratch, "mock-a.log"), "w")
        log_b = open(os.path.join(scratch, "mock-b.log"), "w")
        log_gw = open(os.path.join(scratch, "onegate.log"), "w")
        procs.append(Proc("mock-a", [os.path.join(scratch, "mockprovider"),
                                     "--port", str(port_a)], log_a))
        procs.append(Proc("mock-b", [os.path.join(scratch, "mockprovider"),
                                     "--port", str(port_b)], log_b))

        base_a, base_b = f"http://127.0.0.1:{port_a}", f"http://127.0.0.1:{port_b}"
        if not (wait_health(base_a) and wait_health(base_b)):
            print("mock providers failed to start", file=sys.stderr)
            return 2

        gw_base = f"http://127.0.0.1:{port_gw}"
        procs.append(Proc("onegate", [
            os.path.join(scratch, "onegate"),
            "-host", "127.0.0.1", "-port", str(port_gw),
            "-data-dir", os.path.join(scratch, "data"),
        ], log_gw, env={"ONEGATE_ADMIN_TOKEN": ADMIN_TOKEN}))
        if not wait_health(gw_base):
            print("gateway failed to start; log tail:", file=sys.stderr)
            with open(os.path.join(scratch, "onegate.log")) as f:
                print(f.read()[-2000:], file=sys.stderr)
            return 2

        ctx = Ctx(keys={}, mock_a=base_a, mock_b=base_b, base=gw_base)
        seed_log: list[str] = []
        seed(corpus, ctx, seed_log)

        # Settle: the routing watcher polls storage every second — wait
        # until the REGISTRY (not storage) serves the full model catalog
        # before replaying. /v1/models is registry-backed, so it is the
        # authoritative signal that the data plane is live.
        want_models = len(corpus["seed"]["models"])
        deadline = time.time() + 20
        while time.time() < deadline:
            try:
                status, _, raw = http_json(
                    "GET", ctx.base + "/v1/models",
                    headers={"Authorization": f"Bearer {ctx.keys['primary']}"} if ctx.keys else {})
                if status == 200 and len(json.loads(raw).get("data", [])) >= want_models:
                    break
            except Exception:
                pass
            time.sleep(0.25)
        print(f"seeded: {len(corpus['seed']['keys'])} keys, "
              f"{len(corpus['seed']['models'])} models, "
              f"{len(corpus['seed']['providers'])} providers")

        results = []
        start = time.time()
        for case in cases:
            r = run_case(case, ctx)
            results.append(r)
            mark = "ok  " if r["ok"] else "FAIL"
            print(f"[{mark}] {r['id']} {r['title']}")
        elapsed = time.time() - start

        blockers = write_report(results, seed_log, elapsed)
        print(f"\n{sum(1 for r in results if r['ok'])}/{len(results)} cases pass; "
              f"{blockers} blocker diffs; report: {REPORT}")
        return 1 if blockers else 0
    finally:
        for p in procs:
            p.stop()
        if not args.keep:
            shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
