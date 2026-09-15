# VRAM budget

The RTX 4060 has 8GB of VRAM, so only one large model is resident on the GPU at
a time. The router keeps the Qwen2.5 3B model loaded for fast traffic and
unloads it before loading the 7B model at 4-bit quantization for quality
traffic. The everyday judge runs the same 7B model, never alongside a chat
model.

Embeddings are always available because `nomic-embed-text` needs only about 274
megabytes. The heavy `qwen3-embedding:8b` model at 7.6 billion parameters is
batch-only: load it, embed a backlog, unload it. Running it next to a chat
model causes cold-load stalls of 15 to 25 seconds.

The swap rule is documented in `docs/VRAM.md`: embed-small always resident,
one chat or judge model at a time, Groq's 70B model for heavy judging so VRAM
stays free.
