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
