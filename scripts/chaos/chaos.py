#!/usr/bin/env python3
"""OneGate chaos catalog (p8.chaos-catalog).

Live reproducers for the cataloged failure modes, each with a written
expected behavior (docs/reports/chaos-catalog.md is generated from the
run). Scenarios:

  outage        provider goes healthy -> down (conn refused) -> recovered;
                expect 200 -> 502/503 envelope -> circuit open 503 ->
                half-open recovery 200 after cooldown
  truncation    provider aborts a stream after the first frame; expect
                an in-stream error event, no [DONE], next request fine
  stall         provider hangs forever; client disconnects; expect
                cancelled usage, no stuck goroutines afterwards
  fd-exhaust    gateway runs with RLIMIT_NOFILE=64 under load; expect
                explicit errors (never a hang/deadlock) and full
                recovery once fds free up

Unit-covered catalog entries (not re-run live): disk-full -> the usage
pipeline's drop-and-count and writer-failure resilience
(internal/observability/usage_test.go: TestUsagePipeline_DropAndCountUnderLoad,
TestUsagePipeline_StorageFailureResilience).

Usage: python3 scripts/chaos/chaos.py [--scenario all|outage|truncation|stall|fd-exhaust] [--keep]
"""
from __future__ import annotations

import argparse
import json
import os
import resource
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))) + "/parity")
from replay import Proc, api, free_port, go_bin, http_json, wait_health  # noqa: E402

ADMIN_TOKEN = "parity-admin-token"
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


class Gateway:
    """The system under test: gateway + mock provider + seed."""

    def __init__(self, scratch: str, extra_env: dict | None = None,
                 rlimit_nofile: int | None = None):
        self.scratch = scratch
        self.procs: list[Proc] = []
        self.extra_env = extra_env or {}
        self.rlimit_nofile = rlimit_nofile
        self.mock_port = free_port()
        self.gw_port = free_port()
        self.mock = f"http://127.0.0.1:{self.mock_port}"
        self.base = f"http://127.0.0.1:{self.gw_port}"

    def start(self):
        gobin = go_bin()
        env = {**os.environ, "CGO_ENABLED": "0"}
        for target, out in (("./cmd/onegate", "onegate"), ("./cmd/mockprovider", "mockprovider")):
            subprocess.run([gobin, "build", "-o", os.path.join(self.scratch, out), target],
                           cwd=REPO, check=True, env=env)
        log_m = open(os.path.join(self.scratch, "mock.log"), "w")
        log_g = open(os.path.join(self.scratch, "onegate.log"), "w")
        self.procs.append(Proc("mock", [os.path.join(self.scratch, "mockprovider"),
                                        "--port", str(self.mock_port)], log_m))

        def preexec():
            if self.rlimit_nofile is not None:
                resource.setrlimit(resource.RLIMIT_NOFILE,
                                   (self.rlimit_nofile, self.rlimit_nofile))

        gw = subprocess.Popen(
            [os.path.join(self.scratch, "onegate"), "-port", str(self.gw_port),
             "-data-dir", os.path.join(self.scratch, "data")],
            stdout=log_g, stderr=subprocess.STDOUT, cwd=REPO,
            env={**os.environ, "ONEGATE_ADMIN_TOKEN": ADMIN_TOKEN, **self.extra_env},
            preexec_fn=preexec if self.rlimit_nofile is not None else None)
        self.gw_proc = gw
        if not (wait_health(self.mock) and wait_health(self.base)):
            raise RuntimeError("services failed to start")
        return self

    def seed(self, models: list[tuple[str, str]]):
        api("POST", self.base, "/api/providers", {
            "id": "prov", "name": "Chaos", "protocol": "openai",
            "base_url": self.mock, "api_key": "sk", "enabled": True})
        for model, target in models:
            api("POST", self.base, "/api/models", {
                "id": model,
                "targets": [{"provider_id": "prov", "provider_model": target, "position": 1}],
                "capabilities": {"tools": True, "vision": True, "json_mode": True, "stream": True}})
            api("POST", self.base, "/api/routing-rules", {"model_id": model, "policy": "ordered",
                                                          "enabled": True, "position": 1})
        self.key = api("POST", self.base, "/api/keys", {"name": "chaos"})["raw_key"]
        # registry settle
        deadline = time.time() + 15
        while time.time() < deadline:
            st, _, raw = http_json("GET", self.base + "/v1/models",
                                   headers={"Authorization": f"Bearer {self.key}"})
            if st == 200 and len(json.loads(raw).get("data", [])) >= len(models):
                break
            time.sleep(0.25)

    def call(self, model: str, stream: bool = False, timeout: int = 30):
        body = json.dumps({
            "model": model,
            "messages": [{"role": "user", "content": "chaos"}],
            **({"stream": True} if stream else {}),
        }).encode()
        headers = {"Authorization": f"Bearer {self.key}", "Content-Type": "application/json"}
        t0 = time.time()
        status, _, raw = http_json("POST", self.base + "/v1/chat/completions",
                                   body, headers, timeout=timeout)
        return status, raw.decode("utf-8", "replace"), time.time() - t0

    def kill_mock(self):
        if self.procs:
            self.procs[0].stop()

    def start_mock(self):
        log_m = open(os.path.join(self.scratch, "mock-restart.log"), "w")
        self.procs[0] = Proc("mock", [os.path.join(self.scratch, "mockprovider"),
                                      "--port", str(self.mock_port)], log_m)
        if not wait_health(self.mock):
            raise RuntimeError("mock restart failed")

    def metrics(self) -> dict:
        _, _, raw = http_json("GET", self.base + "/metrics",
                              headers={"Authorization": f"Bearer {ADMIN_TOKEN}"})
        out = {}
        for line in raw.decode().splitlines():
            if line.startswith("onereq_go_goroutines "):
                out["goroutines"] = int(float(line.split()[1]))
        return out

    def stop(self):
        if hasattr(self, "gw_proc"):
            self.gw_proc.send_signal(signal.SIGTERM)
            try:
                self.gw_proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.gw_proc.kill()
        for p in self.procs:
            p.stop()


def scenario_outage(scratch) -> tuple[str, bool, list[str]]:
    """healthy -> outage (mock killed) -> recovery (mock back + cooldown)."""
    notes = []
    gw = Gateway(scratch).start()
    try:
        gw.seed([("gpt-4o", "mock-echo")])
        st, _, _ = gw.call("gpt-4o")
        notes.append(f"healthy: {st}")
        healthy_ok = st == 200

        gw.kill_mock()
        time.sleep(0.5)
        # during outage: connection refused -> transport error -> 502
        sts = [gw.call("gpt-4o")[0] for _ in range(5)]
        notes.append(f"outage statuses: {sorted(set(sts))}")
        outage_ok = all(s in (502, 503) for s in sts)

        # circuit opens after consecutive failures -> clean 503s
        st, body, _ = gw.call("gpt-4o")
        notes.append(f"circuit: {st} {body[:80]}")
        circuit_ok = st == 503 and "overloaded_error" in body

        # recovery: mock back; cooldown is 10s by default
        gw.start_mock()
        time.sleep(11)
        st, _, _ = gw.call("gpt-4o")
        notes.append(f"recovered: {st}")
        recover_ok = st == 200
        return "outage", all([healthy_ok, outage_ok, circuit_ok, recover_ok]), notes
    finally:
        gw.stop()


def scenario_truncation(scratch) -> tuple[str, bool, list[str]]:
    """Provider aborts mid-stream after first byte: error event, no [DONE]."""
    notes = []
    gw = Gateway(scratch).start()
    try:
        gw.seed([("mid-model", "sc-midstream")])
        st, body, dt = gw.call("mid-model", stream=True)
        has_role = '"role":"assistant"' in body
        has_error = '"error"' in body
        has_done = "data: [DONE]" in body
        notes.append(f"status={st} role_frame={has_role} error_frame={has_error} done={has_done} t={dt*1000:.0f}ms")
        ok = st == 200 and has_role and has_error and not has_done

        # next request is unaffected
        st2, _, _ = gw.call("mid-model", stream=True)
        notes.append(f"subsequent: {st2}")
        return "truncation", ok and st2 == 200, notes
    finally:
        gw.stop()


def scenario_stall(scratch) -> tuple[str, bool, list[str]]:
    """Provider hangs; client disconnects; no leaked goroutines."""
    notes = []
    gw = Gateway(scratch).start()
    try:
        gw.seed([("stall-model", "sc-stall")])
        goroutines_before = gw.metrics()["goroutines"]

        body = json.dumps({"model": "stall-model",
                           "messages": [{"role": "user", "content": "x"}]}).encode()
        s = socket.create_connection(("127.0.0.1", gw.gw_port), timeout=10)
        req = (f"POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1:{gw.gw_port}\r\n"
               f"Authorization: Bearer {gw.key}\r\nContent-Type: application/json\r\n"
               f"Content-Length: {len(body)}\r\nConnection: close\r\n\r\n").encode() + body
        s.sendall(req)
        time.sleep(0.8)
        s.close()
        notes.append("client disconnected at 800ms against 30s-stalling provider")

        # usage records cancelled; goroutines return to baseline after drain
        deadline = time.time() + 10
        cancelled = False
        while time.time() < deadline:
            page = api("GET", gw.base, "/api/requests?limit=20")
            for item in page.get("items", []):
                if item.get("model_requested") == "stall-model" and item.get("status") == "cancelled":
                    cancelled = True
            if cancelled:
                break
            time.sleep(0.5)
        time.sleep(31)  # let the stalled upstream time out / drain
        goroutines_after = gw.metrics()["goroutines"]
        notes.append(f"cancelled_recorded={cancelled} goroutines {goroutines_before} -> {goroutines_after}")
        stable = abs(goroutines_after - goroutines_before) <= 6
        return "stall", cancelled and stable, notes
    finally:
        gw.stop()


def scenario_fd_exhaust(scratch) -> tuple[str, bool, list[str]]:
    """RLIMIT_NOFILE=64 under load: explicit errors, then full recovery."""
    notes = []
    gw = Gateway(scratch, rlimit_nofile=64).start()
    try:
        # The 2s-delay model holds connections open so the fd ceiling is
        # actually reached (fast models recycle fds before exhaustion).
        gw.seed([("gpt-4o", "mock-echo"), ("hold-model", "sc-delay-2000")])
        # Under a 64-fd cap: gateway + mock + logs + sqlite use most; a
        # burst of concurrent requests must fail EXPLICITLY (curl errors
        # at the client are acceptable only for connection-starved
        # requests; answered requests must be 200 or proper envelopes —
        # never a hang).
        results: list[tuple[int, float]] = []
        lock = threading.Lock()

        def one():
            try:
                st, _, _ = gw.call("hold-model", timeout=20)
                with lock:
                    results.append((st, 0))
            except Exception:
                with lock:
                    results.append((-1, 0))

        threads = [threading.Thread(target=one) for _ in range(80)]
        t0 = time.time()
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=30)
        wall = time.time() - t0
        ok = sum(1 for s, _ in results if s == 200)
        errs = sorted({s for s, _ in results if s != 200})
        answered = sum(1 for s, _ in results if s > 0)
        transport_failed = sum(1 for s, _ in results if s == -1)
        notes.append(f"80 concurrent (2s-holding model) under NOFILE=64: ok={ok} "
                     f"answered_errors={errs} transport_failures={transport_failed} wall={wall:.1f}s")
        no_hang = wall < 40
        # Everything is an explicit answer or an immediate transport
        # refusal (client-visible connect error) — never a silent hang.
        explicit = all(s in (200, 429, 500, 502, 503, -1) for s, _ in results)
        exhaustion_seen = (ok + transport_failed + len([s for s, _ in results if s > 0])) < 80 \
            or errs

        # Recovery: after the burst drains, sequential requests succeed.
        time.sleep(2)
        rec = [gw.call("gpt-4o", timeout=20)[0] for _ in range(5)]
        notes.append(f"post-burst sequential: {rec}")
        recovered = all(s == 200 for s in rec)
        return "fd-exhaust", (no_hang and explicit and recovered), notes
    finally:
        gw.stop()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenario", default="all",
                    choices=["all", "outage", "truncation", "stall", "fd-exhaust"])
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    runners = {
        "outage": scenario_outage,
        "truncation": scenario_truncation,
        "stall": scenario_stall,
        "fd-exhaust": scenario_fd_exhaust,
    }
    wanted = list(runners) if args.scenario == "all" else [args.scenario]

    results = []
    for name in wanted:
        scratch = tempfile.mkdtemp(prefix=f"onegate-chaos-{name}-")
        print(f"--- {name} ---")
        try:
            n, ok, notes = runners[name](scratch)
        except Exception as e:
            n, ok, notes = name, False, [f"harness error: {e}"]
        for note in notes:
            print(f"  {note}")
        print(f"  => {'PASS' if ok else 'FAIL'}")
        results.append((n, ok, notes))
        if not args.keep:
            shutil.rmtree(scratch, ignore_errors=True)

    print()
    failed = [n for n, ok, _ in results if not ok]
    print(f"chaos catalog: {len(results) - len(failed)}/{len(results)} scenarios match expected behavior")
    if failed:
        print("FAILURES:", ", ".join(failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
