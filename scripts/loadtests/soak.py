#!/usr/bin/env python3
"""OneGate sustained-RPS soak (p8.load-soak).

Drives mixed stream/non-stream traffic against a real gateway + mock
provider for a sustained window at a target RPS, then reports p50/p95/p99
total latency, TTFT (dedicated streaming probes), throughput, error
counts, and resource stability (goroutines, OS threads, open FDs — read
from /metrics).

Budgets (localhost-mock baseline, p8 gate): p99 non-stream < 100 ms,
p95 < 50 ms, p99 stream TTFT < 60 ms, error rate < 0.1%, and no resource
growth over the soak (first-quarter vs last-quarter gauge deltas within
noise).

Usage:
    python3 scripts/loadtests/soak.py [--duration 60] [--rps 40] [--keep]
"""
from __future__ import annotations

import argparse
import json
import os
import random
import shutil
import statistics
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
        elif line.startswith("onereq_go_os_threads "):
            out["threads"] = int(float(line.split()[1]))
        elif line.startswith("onereq_process_open_fds "):
            out["fds"] = int(float(line.split()[1]))
    return out


class Sampler(threading.Thread):
    """Background resource sampler (1 Hz)."""

    def __init__(self, base: str):
        super().__init__(daemon=True)
        self.base = base
        self.samples: list[dict] = []
        self.stop = threading.Event()

    def run(self):
        while not self.stop.is_set():
            try:
                self.samples.append(metrics(self.base))
            except Exception:
                pass
            self.stop.wait(1.0)


def percentile(values: list[float], p: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    idx = min(len(ordered) - 1, int(round(p / 100 * (len(ordered) - 1))))
    return ordered[idx]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--duration", type=int, default=60, help="soak seconds")
    ap.add_argument("--rps", type=int, default=40, help="target requests per second")
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    scratch = tempfile.mkdtemp(prefix="onegate-soak-")
    procs: list[Proc] = []
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

        # Seed: one provider/model/key.
        api("POST", base, "/api/providers", {
            "id": "prov", "name": "Soak", "protocol": "openai",
            "base_url": mock, "api_key": "sk", "enabled": True})
        api("POST", base, "/api/models", {
            "id": "gpt-4o",
            "targets": [{"provider_id": "prov", "provider_model": "mock-echo", "position": 1}],
            "capabilities": {"tools": True, "vision": True, "json_mode": True, "stream": True}})
        api("POST", base, "/api/routing-rules", {"model_id": "gpt-4o", "policy": "ordered",
                                                 "enabled": True, "position": 1})
        key = api("POST", base, "/api/keys", {"name": "soak"})["raw_key"]

        sampler = Sampler(base)
        sampler.start()
        time.sleep(2)  # baseline samples

        latencies: list[float] = []
        ttfts: list[float] = []
        errors: list[int] = []
        lock = threading.Lock()
        interval = 1.0 / args.rps
        start = time.time()
        next_send = start
        sent = 0

        def worker(stream: bool):
            body = json.dumps({
                "model": "gpt-4o",
                "messages": [{"role": "user", "content": "soak"}],
                **({"stream": True} if stream else {}),
            }).encode()
            headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
            t0 = time.time()
            try:
                status, _, _raw = http_json("POST", base + "/v1/chat/completions",
                                            body, headers, timeout=30)
                with lock:
                    latencies.append(time.time() - t0)
                    errors.append(1 if status != 200 else 0)
            except Exception:
                with lock:
                    latencies.append(time.time() - t0)
                    errors.append(1)

        import socket

        def ttft_probe() -> float:
            """Streaming TTFT: raw socket, time to the first data: frame."""
            body = json.dumps({"model": "gpt-4o", "stream": True,
                               "messages": [{"role": "user", "content": "ttft"}]}).encode()
            s = socket.create_connection(("127.0.0.1", port_gw), timeout=10)
            req = (f"POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1:{port_gw}\r\n"
                   f"Authorization: Bearer {key}\r\nContent-Type: application/json\r\n"
                   f"Content-Length: {len(body)}\r\nConnection: close\r\n\r\n").encode() + body
            t0 = time.time()
            s.sendall(req)
            buf = b""
            while b"data:" not in buf:
                chunk = s.recv(4096)
                if not chunk:
                    break
                buf += chunk
            dt = time.time() - t0
            s.close()
            return dt

        while time.time() - start < args.duration:
            now = time.time()
            if now < next_send:
                time.sleep(min(0.005, max(0, next_send - now)))
                continue
            next_send += interval
            stream = random.random() < 0.5
            threading.Thread(target=worker, args=(stream,), daemon=True).start()
            sent += 1
            if sent % 5 == 0:
                try:
                    with lock:
                        ttfts.append(ttft_probe())
                except Exception:
                    pass

        time.sleep(2)  # drain
        sampler.stop.set()
        sampler.join(timeout=3)

        elapsed = time.time() - start
        ok = sum(1 for e in errors if e == 0)
        err = sum(errors)
        p50 = percentile(latencies, 50) * 1000
        p95 = percentile(latencies, 95) * 1000
        p99 = percentile(latencies, 99) * 1000
        ttft_p50 = percentile(ttfts, 50) * 1000
        ttft_p95 = percentile(ttfts, 95) * 1000
        ttft_p99 = percentile(ttfts, 99) * 1000

        quarter = max(1, len(sampler.samples) // 4)
        first = sampler.samples[:quarter]
        last = sampler.samples[-quarter:]

        def avg(rows, k):
            vals = [r[k] for r in rows if k in r]
            return statistics.mean(vals) if vals else 0

        growth = {
            "goroutines": round(avg(last, "goroutines") - avg(first, "goroutines"), 1),
            "threads": round(avg(last, "threads") - avg(first, "threads"), 1),
            "fds": round(avg(last, "fds") - avg(first, "fds"), 1),
        }

        budget = {
            "p99_ms < 100": p99 < 100,
            "p95_ms < 50": p95 < 50,
            "ttft_p99_ms < 60": ttft_p99 < 60,
            "error_rate < 0.1%": (err / max(1, ok + err)) < 0.001,
            "goroutine growth < 5": abs(growth["goroutines"]) < 5,
            "thread growth < 5": abs(growth["threads"]) < 5,
            "fd growth < 10": abs(growth["fds"]) < 10,
        }

        print(f"soak: {args.duration}s @ {args.rps} rps target, sent={sent}, "
              f"completed={ok}, errors={err}")
        print(f"throughput: {ok / elapsed:.1f} req/s actual")
        print(f"latency: p50={p50:.1f}ms p95={p95:.1f}ms p99={p99:.1f}ms")
        print(f"ttft (probe): p50={ttft_p50:.1f}ms p95={ttft_p95:.1f}ms p99={ttft_p99:.1f}ms")
        print(f"resource growth (first vs last quarter): {growth}")
        print("budgets:")
        for k, v in budget.items():
            print(f"  {'PASS' if v else 'FAIL'}  {k}")
        failed = [k for k, v in budget.items() if not v]
        print(f"\nresult: {'SOAK PASS' if not failed else 'SOAK FAIL: ' + ', '.join(failed)}")
        return 0 if not failed else 1
    finally:
        for p in procs:
            p.stop()
        if not args.keep:
            shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
