# Judge models

Everyday judging is done by the local Qwen2.5 7B model at 4-bit quantization. It
is free, private, and runs constantly on the RTX 4060. Its weakness is
self-preference bias: a model rates its own family's outputs higher than they
deserve, so local-only grades can lie.

The cross-check is Groq's free tier, which needs no credit card. The frequent
checker is `llama-3.1-8b-instant` at 30 requests per minute and 14,400 requests
per day. The strict weekly spot-check is `llama-3.3-70b-versatile` at 30
requests per minute but only 1,000 requests per day with a tight
12-thousand-tokens-per-minute ceiling.

Groq returns HTTP 429 when a limit is hit, so the scheduler backs off and
retries. A hard zero-dollar cost cap means the project never pays for judging.
Known judge biases are guarded minimally: candidate order is shuffled to fight
position bias, and long outputs are truncated equally to fight length bias.
