# VRAM Budget — RTX 4060 8GB

One chat model resident at a time.

| Resident | Size | Notes |
|---|---|---|
| `nomic-embed-text` | 137M | Always resident. Embeddings only, negligible VRAM. |
| `qwen2.5:3b-instruct` (fast) | ~2GB | Default. Short/simple prompts. |
| `qwen2.5:7b-instruct-q4_K_M` (quality) | ~4.5GB | Long/complex prompts. Unloads 3B first. |
| `qwen3-embedding:8b` | batch-only | Never resident next to a chat model. Offline ingest/rerank, then unload. |

Swap rule: `ollama ps` before each demo. If the wrong chat model is resident, `ollama stop`
it and pull the other — never hold 3B + 7B together with a >2K context. Keep contexts
modest (2-4K tokens); 7B-Q4 + 4K context is the ceiling on 8GB.

Fallback on this box: `qwen3:8b` serves as judge/drafter until the Qwen2.5 pair is pulled.

## Evals and live traffic do not share the GPU well

The judge (`qwen3:8b`, 5.6 GB) and the fast chat model (`qwen2.5:3b`, 2 GB) cannot both stay
resident in 8 GB, so every chat that lands during an eval run forces two model swaps. Measured
2026-09-15: an idle box scores 48 pairs in ~8 min; the same run with a workflow and a handful
of chats in flight was still at pair 38 after 30 min and hit the old hard-coded deadline.
Rules: run `--score` (or the console's *Run eval suite*) on a quiet box or on a schedule
(`--schedule-evals`) outside traffic hours; `EVAL_TIMEOUT` (default 90m) bounds a run; a run
that dies writes nothing (header + pair rows are one transaction).

## Measured, not assumed

The numbers above are a budget for the 8 GB demo box, not a reading. `#/gpu` in the
console (and `GET /v1/gpu`) report what the driver and Ollama say right now:
utilisation, VRAM used vs installed, watts, temperature, which models are resident and
how much VRAM each holds, and how many model swaps happened in the last ten minutes.
`go run . --advise` reads the total VRAM from the card when a driver is present and falls
back to the 8 GB budget when it is not - the report always prints which one it used
(`measured: NVIDIA GeForce GTX 1650` vs `docs/VRAM.md`). On the 4 GB dev box that means
the 7B is now vetoed there, instead of being told it "fits alone".

## Heat is a latency fact, not an answer fact

A hot card does not give different answers. What it does is make the same work take longer,
and that invalidates exactly the comparisons an eval run exists to make. The prober records
both halves: `temp_c` is the reading, `throttled` is the consequence (the driver's
`clocks_event_reasons.active` SW/HW thermal bits, `0012_gpu_thermal.sql`), and `NULL` there
means the driver was never asked - not "cool".

Measured on the 4 GB GTX 1650 dev box, 2026-11-17, three back-to-back `phi4-mini:3.8b`
generations (300 tokens each, ~40 s of sampling every 1.5 s):

| Reading | Idle | Under load | Meaning |
|---|---|---|---|
| temperature | 56 °C | 76 °C | below the driver's slowdown point |
| graphics clock | 300 MHz | 1785 MHz | the card's maximum, no cut |
| utilisation | 0 % | 96 % | compute-bound, as expected |
| board power | 4 W | 44 W | well inside the card's envelope |
| `throttled` samples | 0 | 0 | nothing was cut for heat in this window |
| decode rate | - | 21.2 tok/s | phi4-mini 3.8b q4, warm card |

So on this box today there is ~20 °C of headroom and no throttling, and a run's latency is
comparable with any other run at similar clocks. The driver's own counters show the other
case is real though: since the current driver load `nvidia-smi` reports ~42 minutes of SW
thermal slowdown and 0.1 s of HW thermal slowdown (plus 33 s of power capping). During those
windows the clock was cut by the driver, so a slow run from that period cannot be compared
with a quiet one - which is what the `throttled` chip on the runs table and the amber pill
on `#/gpu` are there to say.

Sampling temperature is the opposite kind of knob: it changes what the model computes, so
the harness pins it to 0 wherever a comparison happens - the judge on both backends
(`evals/judge.go`, `evals/judge_temperature_test.go`) and Track L answer generation
(`--score-model`). Heat has no equivalent effect on the answer, only on the stopwatch.
