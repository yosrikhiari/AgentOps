# Privacy

What AgentOps stores, and what never leaves the machine.

- **Prompts are not persisted by the gateway.** A router chat writes three spans; their attributes are the routing decision (`tier`, `reason`, `sensitive`, `candidates`), the model/backend, latency and token counts — never the prompt or the answer. Tracker steps store an `output_snippet` (first 200 characters of the step's output) for the trace view; every read path (HTTP `/v1/traces`, CLI `--trace`, MCP `inspect_trace`, the console) additionally strips any `prompt/input/text/output` key defensively.
- **Sensitive requests never leave the box.** A request marked sensitive (`X-AgentOps-Sensitive: true`, `"sensitive": true`, or a keyword such as `password`/`iban`/`passport`) is routed to local backends only; a cloud model is never a fallback for it and naming one explicitly is refused. The flag is recorded on the trace.
- **API keys:** only the SHA-256 of a key is stored (`api_keys.key_hash`); the secret is printed once at creation. Usage counters are per key, not per prompt.
- **Cloud judge:** the optional Groq judge (`JUDGE_BACKEND=groq`) receives golden-set claims and corpus chunks during an eval run — never live user traffic. Default judging is local.
- **Conversations are the exception and say so.** `conversations` + `messages` store prompts and answers verbatim — purpose-limited to thread continuity, only ever created when a client asks (`POST /v1/conversations`), retained 90 days of idleness (purged at server boot), erasable per thread (`DELETE /v1/conversations/{id}`). The gateway chat path itself still stores nothing.
- **Retention:** span rows 30 days (policy; no automatic sweep yet — see the plan backlog), eval scores indefinitely (no personal data in them), API-key usage indefinitely, conversation threads 90 days idle (automatic boot purge + on-demand delete).
- **Logs:** no raw embeddings, no prompts; the gateway logs trace ids, backends and errors.
- **Console:** `GET /` and its `/v1/overview|requests|evals|workflows` endpoints are unauthenticated, like `/metrics`. They expose routing metadata and step snippets, not prompts. Put a reverse proxy or firewall in front if the port is reachable beyond localhost.
- No accounts, no telemetry, no real users; this is a solo portfolio project.
