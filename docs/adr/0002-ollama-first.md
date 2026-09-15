# ADR-0002 — Ollama first, vLLM later

**Status:** accepted (2026-09-13) · **Context:** RTX 4060, 8 GB VRAM, one developer.

**Decision.** Serve models through a local Ollama over its HTTP API (`/api/chat`, streaming NDJSON), calling it with `net/http` — no SDK, no gateway library. vLLM (batching, PagedAttention) is deferred until there is concurrent load that would show its value.

**Consequences.** One request at a time per model; model swaps cost seconds on 8 GB (`docs/VRAM.md`). The `Backend` interface (ADR-0007) keeps Ollama one implementation among others, so a vLLM or OpenAI-style backend is one file.
