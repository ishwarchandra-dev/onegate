# Parity Replay Corpus

Captured OmniRoute v3.8.52 traffic (anonymized) for replay diffing against
OneGate. This is the fixture half of the parity harness (p7.capture-corpus);
the executor is `scripts/parity/replay.py` (p7.replay-harness).

## Layout

| Path | Role |
| --- | --- |
| `corpus.json` | The corpus: seed spec + 41 replay cases (CC-01 … CC-41) |
| `../../internal/mockprovider/scenarios.go` | Scenario engine the corpus drives: provider-side model names (`sc-500`, `sc-delay-250`, `sc-midstream`, …) select deterministic upstream behaviors, because the gateway does not forward client headers upstream |

## Case format

Each case replays one OmniRoute client exchange:

```jsonc
{
  "id": "CC-01",
  "title": "…",
  "rows": ["A-1"],            // checklist rows evidenced (docs/compat/checklist.md)
  "severity": "blocker",      // diff classification: blocker | cosmetic
  "concurrent": false,        // true: steps fire in parallel, expectations match as a set
  "steps": [
    {
      "name": "…",
      "request": {
        "method": "POST",
        "path": "/v1/chat/completions",
        "headers": {"Authorization": "Bearer {{KEY:primary}}"},
        "body": {…},           // JSON body
        "raw_body": "…",       // or raw string (invalid-JSON cases)
        "oversize_body_bytes": 33554433,  // or generated oversize payload
        "abort_after_ms": 800  // client disconnects mid-request
      },
      "expect": {
        "status": 200,
        "headers": {"Content-Type": "…"},      // subset match
        "headers_absent": ["access-control-allow-origin"],
        "json": {…},                            // normalized deep-compare
        "sse": [{"event": "…", "data": {…}}],   // ordered frame compare
        "sse_absent_data": ["[DONE]"]           // frames that must NOT appear
      }
    }
  ],
  "post": {                                    // management-API assertion after the steps
    "path": "/api/requests?limit=20",
    "where": {"model_requested": "fallback-500"},
    "expect": {"attempts": 2, "status": "success"},
    "retry_ms": 3000                           // bounded wait for the async usage pipeline
  }
}
```

Placeholders: `{{KEY:<id>}}` (raw key captured at mint), `{{MOCK_A}}` /
`{{MOCK_B}}` (mock provider base URLs), `{{NOW-<ms>}}` (relative time).
The `<any>` sentinel in expected JSON matches any value at that position.

## Severity classification

- **blocker** — any diff fails the phase gate until fixed or waived with a
  migration note.
- **cosmetic** — diffs allowed for the documented deviations listed on the
  row (every cosmetic row carries a divergence note in the checklist).

## Coverage

### Top-20 error contract (checklist area C)

| Row | Case(s) | Row | Case(s) |
| --- | --- | --- | --- |
| C-1 missing key | CC-16 | C-11 scope: model | CC-22 |
| C-2 malformed key | CC-36 | C-12 scope: providers | CC-23 |
| C-3 unknown key | CC-37 | C-13 invalid JSON | CC-24 |
| C-4 revoked | CC-19 | C-14 missing field | CC-25 |
| C-5 expired | CC-20 | C-15 oversize | CC-26 |
| C-6 RPM | CC-17 | C-16 upstream auth | CC-27, CC-31 |
| C-7 TPM | CC-38 | C-17 upstream 429 | CC-18 |
| C-8 spend | CC-39 | C-18 upstream 5xx | CC-04, CC-28 |
| C-9 concurrency | CC-40 | C-19 overload 529 | CC-29 |
| C-10 model not found | CC-21 | C-20 context length | CC-30 |
|  |  | C-21 gateway timeout | unit: `internal/proxy/client/client_test.go` (120 s default budget is too long for replay; classified by the same code path CC-31 exercises) |
|  |  | C-22 disconnect | CC-41 |
|  |  | C-23 circuit open | CC-32 |

### Stream event orders (checklist area D)

| Row | Case | Row | Case |
| --- | --- | --- | --- |
| D-1 OpenAI order | CC-02 | D-6 thinking deltas | CC-08 |
| D-2 tool-call deltas | CC-03 | D-7 Gemini order | CC-10 |
| D-3 [DONE] terminal | CC-33 | D-8 mid-stream error | CC-33 |
| D-4 Anthropic order | CC-06 | D-9 no empty deltas | CC-02 |
| D-5 ping | CC-07 | D-10 usage attribution | CC-04 |

### Endpoints / headers / cross-protocol (areas A, B, G)

A-1..A-6, A-10, A-12 (CC-01/02/05/06/09/10/12/14); A-7 (CC-13);
B-3/B-4/B-9 (CC-01/02); B-5 (CC-17/38/39/40); B-6 (CC-18); B-7 (CC-15);
G-1 (CC-05), G-2 (CC-09), G-3/G-4 (CC-11), G-5 (CC-21), G-6 (CC-34),
G-7 (CC-35).

## Reproducing

The corpus executes against a real `onegate` binary with a freshly seeded
data directory (via the management API) and two `cmd/mockprovider`
instances. Run everything with:

```bash
python3 scripts/parity/replay.py            # builds, seeds, replays, diffs
python3 scripts/parity/replay.py --keep     # keep the scratch dir for debugging
```

Exit code 0 = zero unexplained blocker diffs. The markdown report lands in
`docs/reports/parity-replay.md` (overwritten per run; committed at gate
time as evidence).
