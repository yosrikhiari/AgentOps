# Judge models

Everyday judging is done by a local model through Ollama, selected with the
`JUDGE_MODEL` environment variable. The planned judge is Qwen2.5 7B at 4-bit
quantization; on the current box the fallback is `qwen3:8b`. Local judging is
free and private, but its weakness is self-preference bias: a model rates its own
family's outputs higher than they deserve, so local-only grades can lie.

The cross-check is Groq's free tier, selected with `JUDGE_BACKEND=groq` and a
`GROQ_API_KEY`. It needs no credit card. The frequent checker is
`llama-3.1-8b-instant` at 30 requests per minute and 14,400 requests per day. The
strict weekly spot-check is `llama-3.3-70b-versatile` at 30 requests per minute but
only 1,000 requests per day with a tight 12-thousand-tokens-per-minute ceiling.

Groq returns HTTP 429 when a limit is hit, so the scheduler backs off and retries.
A hard zero-dollar cost cap means the project never pays for judging. Both judges
run at temperature 0 so the same claim gets the same verdict on rerun, and every
run stores the judge model name so a judge swap is never mistaken for drift.
