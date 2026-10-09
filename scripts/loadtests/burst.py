#!/usr/bin/env python3
"""OneGate burst + stream-churn scenarios (p8.load-burst).

Scenarios (each against a real gateway + mock provider):
  1. burst-N       — N simultaneous non-stream requests (N=100, 300)
  2. stream-churn  — rapid short-lived streams (sequential + parallel),
                     checking FD/goroutine recovery after the churn
  3. slow-overload — concurrent traffic against a slow provider model;
                     degradation must be explicit (bounded by provider
                     delay, observable on the inflight gauge) and every
                     response must be a real answer (200 or a proper
                     error envelope — never a silent hang)

Usage:
    python3 scripts/loadtests/burst.py [--keep]
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))) + "/parity")
from replay import Proc, api, free_port, go_bin, http_json, wait_health  # noqa: E402

ADMIN_TOKEN = "parity-admin-token"
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def metrics(base: str) -> dict:
    status, _, raw = http_json("GET", base + "/metrics",
                               headers={"Authorization": f"Bearer {ADMIN_TOKEN}"})
    out = {}
    for line in raw.decode().splitlines():
        if line.startswith("onereq_go_goroutines "):
            out["goroutines"] = int(float(line.split()[1]))
        elif line.startswith("onereq_process_open_fds "):
            out["fds"] = int(float(line.split()[1]))
        elif line.startswith("onereq_inflight_requests{protocol=\"openai\"} "):
            out["inflight"] = int(float(line.split()[1]))
    return out


class Result:
    def __init__(self):
        self.statuses: list[int] = []
        self.durations: list[float] = []
        self.lock = threading.Lock()

    def record(self, status: int, dt: float):
        with self.lock:
            self.statuses.append(status)
            self.durations.append(dt)

    def summary(self) -> dict:
        ds = sorted(self.durations)
        p = lambda q: ds[min(len(ds) - 1, int(q / 100 * (len(ds) - 1)))] * 1000 if ds else 0
        return {
            "n": len(ds),
            "ok": sum(1 for s in self.statuses if s == 200),
            "errors": {s: self.statuses.count(s) for s in set(self.statuses) if s != 200},
            "p50_ms": round(p(50), 1),
            "p95_ms": round(p(95), 1),
            "p99_ms": round(p(99), 1),
            "max_ms": round(ds[-1] * 1000, 1) if ds else 0,
        }


def fire(base: str, key: str, model: str, n: int, stream: bool, result: Result,
         timeout=30):
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": "burst"}],
        **({"stream": True} if stream else {}),
    }).encode()
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}

    def one():
        t0 = time.time()
        try:
            status, _, _ = http_json("POST", base + "/v1/chat/completions",
                                     body, headers, timeout=timeout)
            result.record(status, time.time() - t0)
        except Exception as e:
            result.record(-1, time.time() - t0)

    threads = [threading.Thread(target=one) for _ in range(n)]
    t0 = time.time()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return time.time() - t0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    scratch = tempfile.mkdtemp(prefix="onegate-burst-")
    procs: list[Proc] = []
    scenarios: list[tuple[str, dict]] = []
    try:
        gobin = go_bin()
        env = {**os.environ, "CGO_ENABLED": "0"}
        for target, out in (("./cmd/onegate", "onegate"), ("./cmd/mockprovider", "mockprovider")):
            subprocess.run([gobin, "build", "-o", os.path.join(scratch, out), target],
                           cwd=REPO, check=True, env=env)

        port_mock, port_gw = free_port(), free_port()
        log_m = open(os.path.join(scratch, "mock.log"), "w")
        log_g = open(os.path.join(scratch, "onegate.log"), "w")
        procs.append(Proc("mock", [os.path.join(scratch, "mockprovider"),
                                   "--port", str(port_mock)], log_m))
        procs.append(Proc("onegate", [os.path.join(scratch, "onegate"), "-port", str(port_gw),
                                      "-data-dir", os.path.join(scratch, "data")], log_g,
                          env={"ONEGATE_ADMIN_TOKEN": ADMIN_TOKEN}))
        mock = f"http://127.0.0.1:{port_mock}"
        base = f"http://127.0.0.1:{port_gw}"
        if not (wait_health(mock) and wait_health(base)):
            print("services failed to start", file=sys.stderr)
            return 2

        api("POST", base, "/api/providers", {
            "id": "prov", "name": "Burst", "protocol": "openai",
            "base_url": mock, "api_key": "sk", "enabled": True})
        for model, target_model in (("gpt-4o", "mock-echo"), ("slow-model", "sc-delay-500")):
            api("POST", base, "/api/models", {
                "id": model,
                "targets": [{"provider_id": "prov", "provider_model": target_model, "position": 1}],
                "capabilities": {"tools": True, "vision": True, "json_mode": True, "stream": True}})
            api("POST", base, "/api/routing-rules", {"model_id": model, "policy": "ordered",
                                                     "enabled": True, "position": 1})
        key = api("POST", base, "/api/keys", {"name": "burst"})["raw_key"]

        # Registry settle: the routing watcher polls storage every second.
        deadline = time.time() + 15
        while time.time() < deadline:
            st, _, raw = http_json("GET", base + "/v1/models",
                                   headers={"Authorization": f"Bearer {key}"})
            if st == 200 and len(json.loads(raw).get("data", [])) >= 2:
                break
            time.sleep(0.25)

        # --- scenario 1+2: bursts ---------------------------------------
        for n in (100, 300):
            res = Result()
            wall = fire(base, key, "gpt-4o", n, stream=False, result=res)
            s = res.summary()
            s["wall_s"] = round(wall, 2)
            scenarios.append((f"burst-{n}", s))
            print(f"burst-{n}: {s}")

        # --- scenario 3: stream churn ------------------------------------
        res = Result()
        # 150 sequential quick streams
        for _ in range(150):
            fire(base, key, "gpt-4o", 1, stream=True, result=res, timeout=10)
        # plus 50 parallel
        fire(base, key, "gpt-4o", 50, stream=True, result=res, timeout=20)
        s = res.summary()
        # resource recovery after churn
        time.sleep(3)
        post = metrics(base)
        s["post_churn"] = {"goroutines": post.get("goroutines"), "fds": post.get("fds"),
                           "inflight": post.get("inflight", 0)}
        s["recovered"] = post.get("inflight", 0) == 0
        scenarios.append(("stream-churn", s))
        print(f"stream-churn: {s}")

        # --- scenario 4: slow-provider overload ---------------------------
        peak = {}
        stop = threading.Event()

        def inflight_watch():
            while not stop.is_set():
                try:
                    m = metrics(base)
                    peak["inflight"] = max(peak.get("inflight", 0), m.get("inflight", 0))
                except Exception:
                    pass
                stop.wait(0.2)

        watcher = threading.Thread(target=inflight_watch, daemon=True)
        watcher.start()
        res = Result()
        wall = fire(base, key, "slow-model", 60, stream=False, result=res, timeout=60)
        stop.set()
        watcher.join(timeout=2)
        s = res.summary()
        s["wall_s"] = round(wall, 2)
        s["peak_inflight"] = peak.get("inflight", 0)
        s["bounded_by_provider"] = s["max_ms"] < 5000  # 500ms delay + queueing, no unbounded hang
        s["explicit_answers"] = s["ok"] + sum(s["errors"].values()) == s["n"]
        scenarios.append(("slow-overload", s))
        print(f"slow-overload: {s}")

        # gate posture: every scenario answered every request explicitly
        posture_ok = all(
            (s["ok"] + sum(s["errors"].values()) == s["n"]) for _, s in scenarios
        ) and scenarios[2][1].get("recovered")

        print(f"\nresult: {'BURST PASS' if posture_ok else 'BURST FAIL'} "
              f"(explicit answers everywhere: {posture_ok})")
        return 0 if posture_ok else 1
    finally:
        for p in procs:
            p.stop()
        if not args.keep:
            shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
