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

