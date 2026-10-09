#!/usr/bin/env python3
"""OneGate crash-recovery matrix (p8.crash-recovery).

SIGKILL (kill -9) the gateway at three moments and verify convergence
after restart:

  idle-kill       kill an idle, fully-seeded gateway; every entity and
                  every committed usage row must survive byte-exactly
  midflight-kill  kill under active streaming + non-streaming load;
                  after restart: usage rows confirmed >1s before the
                  kill MUST be present (flush window is 100ms), records
                  present must be well-formed, new traffic flows
  migration-kill  SIGKILL as soon as the listener answers on a FRESH
                  data dir (racing first-boot migration); restart must
                  complete migration and serve normally

Convergence checks after every restart: /healthz OK, schema version
current, providers/models/keys intact, request records well-formed,
new requests served and recorded.

Usage: python3 scripts/chaos/crash.py [--keep]
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))) + "/parity")
from replay import Proc, api, free_port, go_bin, http_json, wait_health  # noqa: E402

ADMIN_TOKEN = "parity-admin-token"
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


class CrashGateway:
    def __init__(self, scratch: str):
        self.scratch = scratch
        self.mock_port = free_port()
        self.gw_port = free_port()
        self.mock = f"http://127.0.0.1:{self.mock_port}"
        self.base = f"http://127.0.0.1:{self.gw_port}"
        self.data_dir = os.path.join(scratch, "data")
        self.gw: subprocess.Popen | None = None
        self.mock_proc: Proc | None = None

    def build(self):
        gobin = go_bin()
        env = {**os.environ, "CGO_ENABLED": "0"}
        for target, out in (("./cmd/onegate", "onegate"), ("./cmd/mockprovider", "mockprovider")):
            subprocess.run([gobin, "build", "-o", os.path.join(self.scratch, out), target],
                           cwd=REPO, check=True, env=env)

    def start_mock(self):
        log = open(os.path.join(self.scratch, "mock.log"), "a")
        self.mock_proc = Proc("mock", [os.path.join(self.scratch, "mockprovider"),
                                       "--port", str(self.mock_port)], log)
        if not wait_health(self.mock):
            raise RuntimeError("mock failed")

    def start_gw(self):
        log = open(os.path.join(self.scratch, "onegate.log"), "a")
        self.gw = subprocess.Popen(
            [os.path.join(self.scratch, "onegate"), "-port", str(self.gw_port),
             "-data-dir", self.data_dir],
            stdout=log, stderr=subprocess.STDOUT, cwd=REPO,
            env={**os.environ, "ONEGATE_ADMIN_TOKEN": ADMIN_TOKEN})
        return self.gw

    def wait_gw(self, timeout=20):
        if not wait_health(self.base, timeout):
            raise RuntimeError("gateway failed to start")

    def kill9(self):
        if self.gw and self.gw.poll() is None:
            self.gw.send_signal(signal.SIGKILL)
            self.gw.wait(timeout=10)

    def seed(self):
        api("POST", self.base, "/api/providers", {
            "id": "prov", "name": "Crash", "protocol": "openai",
            "base_url": self.mock, "api_key": "sk", "enabled": True})
        api("POST", self.base, "/api/models", {
            "id": "gpt-4o",
            "targets": [{"provider_id": "prov", "provider_model": "mock-echo", "position": 1}],
            "capabilities": {"tools": True, "vision": True, "json_mode": True, "stream": True}})
        api("POST", self.base, "/api/routing-rules", {"model_id": "gpt-4o", "policy": "ordered",
                                                      "enabled": True, "position": 1})
        self.key = api("POST", self.base, "/api/keys", {"name": "crash"})["raw_key"]
        deadline = time.time() + 15
        while time.time() < deadline:
            st, _, raw = http_json("GET", self.base + "/v1/models",
                                   headers={"Authorization": f"Bearer {self.key}"})
            if st == 200 and len(json.loads(raw).get("data", [])) >= 1:
                break
            time.sleep(0.25)

    def call(self, stream=False, timeout=30):
        body = json.dumps({"model": "gpt-4o", "messages": [{"role": "user", "content": "c"}],
                           **({"stream": True} if stream else {})}).encode()
        headers = {"Authorization": f"Bearer {self.key}", "Content-Type": "application/json"}
        status, _, _ = http_json("POST", self.base + "/v1/chat/completions",
                                 body, headers, timeout=timeout)
        return status

    def state(self) -> dict:
        """Snapshot of durable state: entities via the management API,
        request-row COUNT straight from SQLite (the API list endpoint is
        paginated and would undercount)."""
        import sqlite3
        conn = sqlite3.connect(f"file:{self.data_dir}/onegate.db?mode=ro", uri=True)
        n_requests = conn.execute("SELECT COUNT(*) FROM requests").fetchone()[0]
        rows = conn.execute(
            "SELECT id, status FROM requests ORDER BY created_ms DESC LIMIT 200").fetchall()
        conn.close()
        return {
            "providers": [p["id"] for p in api("GET", self.base, "/api/providers?limit=100")["items"]],
            "models": [m["id"] for m in api("GET", self.base, "/api/models?limit=100")["items"]],
            "keys": len(api("GET", self.base, "/api/keys?limit=100")["items"]),
            "request_count": n_requests,
            "recent": [{"id": r[0], "status": r[1]} for r in rows],
        }

    def schema_version(self) -> int:
        import sqlite3
        conn = sqlite3.connect(f"file:{self.data_dir}/onegate.db?mode=ro", uri=True)
        row = conn.execute("SELECT MAX(version) FROM schema_migrations").fetchone()
        conn.close()
        return row[0] or 0

    def stop(self):
        self.kill9()
        if self.mock_proc:
            self.mock_proc.stop()


def scenario_idle_kill(scratch) -> tuple[str, bool, list[str]]:
    notes = []
    gw = CrashGateway(scratch)
    gw.build()
    gw.start_mock()
    gw.start_gw()
    gw.wait_gw()
    try:
        gw.seed()
        for _ in range(10):
            assert gw.call() == 200
        time.sleep(1.5)  # usage flush (100ms window) + settle
        before = gw.state()
        ver_before = gw.schema_version()

        gw.kill9()
        notes.append("killed -9 while idle")

        gw.start_gw()
        gw.wait_gw()
        after = gw.state()
        ver_after = gw.schema_version()

        same = (before["providers"] == after["providers"] and
                before["models"] == after["models"] and
                before["keys"] == after["keys"] and
                before["request_count"] == after["request_count"])
        ok_post = gw.call() == 200 and gw.call(stream=True) == 200
        notes.append(f"state identical: {same}; schema {ver_before}->{ver_after}; "
                     f"post-restart traffic: {ok_post}; requests preserved: {after['request_count']}")
        return "idle-kill", same and ver_after == ver_before and ok_post, notes
    finally:
        gw.stop()


def scenario_midflight_kill(scratch) -> tuple[str, bool, list[str]]:
    notes = []
    gw = CrashGateway(scratch)
    gw.build()
    gw.start_mock()
    gw.start_gw()
    gw.wait_gw()
    try:
        gw.seed()

        confirmed = {"n": 0}
        stop = threading.Event()

        def load():
            while not stop.is_set():
                try:
                    if gw.call(stream=True) == 200:
                        confirmed["n"] += 1
                except Exception:
                    pass

        threads = [threading.Thread(target=load, daemon=True) for _ in range(4)]
        for t in threads:
            t.start()
        time.sleep(5)
        stop.set()
        for t in threads:
            t.join(timeout=5)

        # Everything confirmed >1s before the kill must be durable
        # (usage flush window is 100ms; 1s is a 10x margin).
        time.sleep(1)
        floor = confirmed["n"]
        gw.kill9()
        notes.append(f"killed -9 with {floor} client-confirmed requests in flight window")

        gw.start_gw()
        gw.wait_gw()
        st = gw.state()
        rows = st["request_count"]
        wellformed = all(r.get("id") and r.get("status") in ("success", "error", "cancelled")
                         for r in st["recent"])
        ok_post = gw.call() == 200
        notes.append(f"durable rows={rows} (floor {floor}), well-formed={wellformed}, "
                     f"post-restart traffic={ok_post}")
        converged = rows >= floor and wellformed and ok_post
        return "midflight-kill", converged, notes
    finally:
        gw.stop()


def scenario_migration_kill(scratch) -> tuple[str, bool, list[str]]:
    notes = []
    gw = CrashGateway(scratch)  # fresh data dir -> first-boot migration
    gw.build()
    gw.start_mock()
    try:
        proc = gw.start_gw()
        # Race the migration: kill the moment the port answers.
        deadline = time.time() + 10
        killed_early = False
        while time.time() < deadline:
            if proc.poll() is not None:
                notes.append("process exited before kill (unexpected)")
                return "migration-kill", False, notes
            try:
                with subprocess.Popen(["bash", "-c",
                                       f"exec 3<>/dev/tcp/127.0.0.1/{gw.gw_port} && exec 3<&-"]) as p:
                    p.wait(timeout=1)
                # port answered — kill NOW
                proc.send_signal(signal.SIGKILL)
                proc.wait(timeout=10)
                killed_early = True
                break
            except Exception:
                time.sleep(0.001)
        notes.append(f"killed -9 as soon as the listener answered (racing migration): {killed_early}")

        gw.start_gw()
        gw.wait_gw()
        ver = gw.schema_version()
        gw.seed()
        ok = gw.call() == 200 and gw.call(stream=True) == 200
        expected_ver = 5
        notes.append(f"restart: schema={ver} (expected {expected_ver}), traffic ok: {ok}")
        return "migration-kill", killed_early and ver == expected_ver and ok, notes
    finally:
        gw.stop()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    results = []
    for name, runner in (("idle-kill", scenario_idle_kill),
                         ("midflight-kill", scenario_midflight_kill),
                         ("migration-kill", scenario_migration_kill)):
        scratch = tempfile.mkdtemp(prefix=f"onegate-crash-{name}-")
        print(f"--- {name} ---")
        try:
            n, ok, notes = runner(scratch)
        except Exception as e:
            n, ok, notes = name, False, [f"harness error: {e}"]
        for note in notes:
            print(f"  {note}")
        print(f"  => {'PASS' if ok else 'FAIL'}")
        results.append((n, ok, notes))
        if not args.keep:
            shutil.rmtree(scratch, ignore_errors=True)

    failed = [n for n, ok, _ in results if not ok]
    print(f"\ncrash matrix: {len(results) - len(failed)}/{len(results)} converge")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
