# VRAM budget

The RTX 4060 has 8GB of VRAM, so only one large model is resident on the GPU at
a time. The router's fast model is Qwen2.5 3B and its quality model is Qwen2.5 7B
at 4-bit quantization; Ollama unloads one before loading the other. The everyday
judge runs as a separate model through Ollama and is never loaded alongside a chat
model during an eval run.

Embeddings are always available because `nomic-embed-text` needs only about 274
megabytes. The heavy `qwen3-embedding:8b` model at 7.6 billion parameters is
batch-only: load it, embed a backlog, unload it. Running it next to a chat model
causes cold-load stalls of 15 to 25 seconds.

The swap rule is documented in `docs/VRAM.md`: embed-small always resident, one
chat or judge model at a time, and Groq's 70B model for heavy judging so VRAM
stays free. On the current development box `qwen3:8b` serves as judge and
drafter until the Qwen2.5 pair is pulled.
