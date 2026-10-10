# Parity Replay Report

Generated 2026-10-10 12:44:04 by `scripts/parity/replay.py` (p7.replay-harness).

- Cases: **41/41 passing**
- Blocker diffs: **0**
- Cosmetic diffs: 0
- Elapsed: 4.1s

## Environment

```
provider prov-openai-a -> http://127.0.0.1:55003
provider prov-openai-b -> http://127.0.0.1:44271
provider prov-anthropic -> http://127.0.0.1:55003
provider prov-gemini -> http://127.0.0.1:55003
model gpt-4o (2 targets, ordered)
model openai-echo (1 targets, ordered)
model claude-sonnet (1 targets, ordered)
model gemini-flash (1 targets, ordered)
model fallback-500 (2 targets, ordered)
model fail-500 (2 targets, ordered)
model fail-401 (2 targets, ordered)
model fail-429 (2 targets, ordered)
model overloaded-model (1 targets, ordered)
model ctxlen-model (1 targets, ordered)
model slow-model (1 targets, ordered)
model midstream-model (1 targets, ordered)
model badjson-model (1 targets, ordered)
model tool-model (1 targets, ordered)
model think-model (1 targets, ordered)
model ping-model (1 targets, ordered)
model weighted-model (2 targets, weighted)
model stall-model (1 targets, ordered)
model circuit-model (1 targets, ordered)
key primary -> vkey_1791636240506_677072
key scoped-model -> vkey_1791636240507_025315
key scoped-provider -> vkey_1791636240507_297468
key revoked -> vkey_1791636240507_534357
key expired -> vkey_1791636240508_217768
key rpm2 -> vkey_1791636240508_498997
key tpm10 -> vkey_1791636240508_755838
key spend100 -> vkey_1791636240509_020262
key conc1 -> vkey_1791636240509_274896
key override-ordered -> vkey_1791636240509_516583
```

## Matrix

| Case | Rows | Severity | Result |
| --- | --- | --- | --- |
| CC-01 | A-1, B-4 | blocker | PASS |
| CC-02 | A-2, B-3, B-9, D-1, D-9 | blocker | PASS |
| CC-03 | D-2 | blocker | PASS |
| CC-04 | C-18, D-10 | blocker | PASS |
| CC-05 | G-1 | blocker | PASS |
| CC-06 | A-4, D-4 | blocker | PASS |
| CC-07 | D-5 | blocker | PASS |
| CC-08 | D-6 | blocker | PASS |
| CC-09 | G-2 | blocker | PASS |
| CC-10 | A-6, D-7 | blocker | PASS |
| CC-11 | G-3, G-4 | blocker | PASS |
| CC-12 | A-10 | blocker | PASS |
| CC-13 | A-7 | blocker | PASS |
| CC-14 | A-12 | blocker | PASS |
| CC-15 | B-7 | blocker | PASS |
| CC-16 | C-1, C-24 | blocker | PASS |
| CC-17 | C-6, B-5 | blocker | PASS |
| CC-18 | B-6, C-17 | blocker | PASS |
| CC-19 | C-4 | blocker | PASS |
| CC-20 | C-5 | blocker | PASS |
| CC-21 | C-10, G-5 | blocker | PASS |
| CC-22 | C-11 | blocker | PASS |
| CC-23 | C-12 | blocker | PASS |
| CC-24 | C-13 | blocker | PASS |
| CC-25 | C-14 | blocker | PASS |
| CC-26 | C-15 | blocker | PASS |
| CC-27 | C-16 | blocker | PASS |
| CC-28 | C-18 | blocker | PASS |
| CC-29 | C-19 | blocker | PASS |
| CC-30 | C-20 | blocker | PASS |
| CC-31 | C-16 | blocker | PASS |
| CC-32 | C-23 | blocker | PASS |
| CC-33 | D-3, D-8 | blocker | PASS |
| CC-34 | G-6 | blocker | PASS |
| CC-35 | G-7 | blocker | PASS |
| CC-36 | C-2 | blocker | PASS |
| CC-37 | C-3 | blocker | PASS |
| CC-38 | C-7 | blocker | PASS |
| CC-39 | C-8 | blocker | PASS |
| CC-40 | C-9 | blocker | PASS |
| CC-41 | C-22 | blocker | PASS |
