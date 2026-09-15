# Ollama

Ollama serves local models through a simple HTTP API on port 11434. The gateway calls
POST /api/chat with the model name, the messages array and a stream flag. With stream set
to false the response is one JSON object whose message.content holds the text plus token
counts: prompt_eval_count for input tokens and eval_count for output tokens. With stream
set to true Ollama returns newline-delimited JSON objects, each carrying a content delta,
and the final object has done set to true and carries the token counts. The gateway's
health probe is GET /api/tags. The evaluation judge and the golden-set drafter use the
older POST /api/generate endpoint because they send one prompt string.

Ollama handles requests one at a time instead of batching them, so it suits
single-developer scale but falls over past a handful of simultaneous users. That is
acceptable for this project. vLLM batches requests and manages GPU memory with a
technique called PagedAttention, but its advantages only show at higher concurrent load
than 8GB of VRAM supports.

When every candidate backend fails, the gateway returns 502 with the error code
ollama_unavailable and a trace id; when the request deadline passes it returns 504 with the
code timeout. Judge calls never retry 4xx responses except 429.
