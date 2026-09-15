# Privacy

- Prompts are stored in `spans.attrs` for debugging but **redacted on every read path** (HTTP `/v1/traces`, CLI `--trace`, MCP `inspect_trace`) — `prompt/input/text/output` keys never leave the database.
- Span rows are retained 30 days; eval scores are kept indefinitely (no personal data in them).
- No raw embeddings are written to logs.
- Everything runs locally against Ollama; the only optional outbound call is the Groq judge (`JUDGE_BACKEND=groq`), which receives golden claims and corpus chunks — never live user prompts.
- No accounts, no telemetry, no real users; this is a solo portfolio project.
