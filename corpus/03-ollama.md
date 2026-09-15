# Ollama

Ollama serves local models through a simple HTTP API on port 11434. The router
calls POST /api/generate with the model name, the prompt, and stream set to
false. The response contains the generated text plus token counts:
`prompt_eval_count` for input tokens and `eval_count` for output tokens.

Ollama handles requests one at a time instead of batching them, so it suits
single-developer scale but falls over past a handful of simultaneous users. That
is acceptable for Phase 1. vLLM batches requests and manages GPU memory with a
technique called PagedAttention, but its advantages only show at higher
concurrent load than 8GB of VRAM supports.

The router treats any non-200 status or connection failure from Ollama as a
502 `ollama_unavailable` error with a trace id. It never retries 4xx responses.
