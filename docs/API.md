# Tower gateway API

Base URL: `http://localhost:8080`. Every error, on every endpoint, has one shape:

```json
{"error":{"code":"rate_limited","message":"key \"demo\" allows 2 requests per minute","trace_id":"aa67895ea5dd4b8e"}}
```

Every response carries `X-Trace-ID`; if you send `X-Request-ID` it is echoed back.

## Authentication

Virtual API keys. Mint one on the box (the secret is printed once; only its SHA-256 is stored):

```bash
go run . --create-key alice --key-rpm 60 --key-budget 2000000
# api key "alice" created (rpm=60 budget_tokens=2000000)
# ak_3d2649b16aa74bfff2b55b2b9eed4887
```

Send it as `Authorization: Bearer ak_…`. With `REQUIRE_API_KEY=true` anonymous requests get `401 missing_api_key`; otherwise a request without a key is served anonymously and a request *with* a key is always validated.

| Status | code | When |
|---|---|---|
| 401 | `missing_api_key` / `invalid_api_key` | no key while required / unknown key |
| 403 | `api_key_disabled` | key disabled |
| 403 | `budget_exceeded` | lifetime `tokens_used ≥ budget_tokens` |
| 429 | `rate_limited` (+ `Retry-After`) | more than `rpm` requests in the current minute |
| 400 | `bad_request` / `unsupported_field` / `option_out_of_range` / `request_too_large` | malformed body, a parameter this gateway cannot honour, an out-of-range generation option, or a body over 4 MiB |
| 404 | `not_found` | no route for that method+path (unknown API path or a typo) |
| 405 | `method_not_allowed` (+ `Allow`) | the path exists but not with that verb, e.g. `POST /v1/models` |

Every response with a non-2xx status uses this envelope, including unknown paths and wrong methods — a client can parse the body unconditionally.

Per-key counters: `router_key_requests_total{key}` and `router_key_rejected_total{key}` on `/metrics`.

## `POST /v1/chat/completions`

OpenAI-compatible. `model` is optional — `"auto"` (default) lets the router pick a tier from the prompt; a served model name (or `backend/model`) is used as-is. A **tier name** — `"fast"`, `"quality"` or `"cloud"` — picks the first registered model serving that tier (`reason: "explicit-tier"`), which is what the console's model picker sends; a model that is really named `fast` still wins over the alias.

**Unsupported OpenAI fields are refused, not dropped** — `tools`, `tool_choice`, `n` other than 1, `logprobs: true` and `top_logprobs` return `400 unsupported_field` naming every offending field. Returning 200 with plain prose would leave a tool-using client believing its `tool_calls` ran; the Anthropic ingress already refuses `tools` for the same reason. `n: 1` and `logprobs: false` ask for what the gateway already does and are accepted.

```bash
curl localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{
  "model": "auto",
  "messages": [{"role":"system","content":"Answer in one word."},{"role":"user","content":"What colour is the sky?"}],
  "max_tokens": 8
}'
```

```json
{
  "id": "chatcmpl-aa67895ea5dd4b8e", "object": "chat.completion", "created": 1789460054,
  "model": "qwen2.5:3b-instruct",
  "choices": [{"index":0,"message":{"role":"assistant","content":"Blue"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens":24,"completion_tokens":2,"total_tokens":26},
  "text": "Blue", "reason": "short-simple-prompt", "backend": "ollama", "trace_id": "aa67895ea5dd4b8e",
  "tier": "fast",
  "candidates": [{"model":"qwen2.5:3b-instruct","tier":"fast","local":true},
                 {"model":"qwen2.5:7b-instruct-q4_K_M","tier":"quality","local":true}]
}
```

`text`, `reason`, `backend`, `trace_id`, `tier`, `candidates` and (when a fallback happened) `fallback` are Tower extensions; OpenAI clients ignore them. The MVP body `{"prompt":"…","max_tokens":N}` is still accepted. `candidates` is the whole eligible chain in the order it would be tried, so a UI can show what was considered without re-deriving the decision.

**Streaming** — `"stream": true` returns `text/event-stream` in the OpenAI chunk shape; the last chunk has `finish_reason:"stop"` plus `usage`, `reason`, `backend`, `trace_id`, `tier`, `candidates`, `answered_by`, then `data: [DONE]`. A failure after the first delta is sent as a `data: {"error":…}` event.

**Routing** — `reason` is one of `short-simple-prompt` (≤200 chars, no reasoning word, no complexity keyword → fast tier), `long-or-complex-prompt` (→ quality tier), `reasoning-prompt` (contains `why`, `explain`, `compare`, `reason`, `analyse`, `justify`, … → quality tier even when short), `gateway-question` (a question about this gateway's models or routing → quality tier, answered by the router with no model call), `explicit-model` and `explicit-tier`. If the chosen backend fails, the router tries the other local tier, then — for non-sensitive requests only — the cloud model; the response's `fallback` field and the `model.generate` span record what happened. Backends the health prober last saw down are skipped as fallbacks.

**Questions about the gateway** — a prompt that asks which model is used, why, or how routing works is answered by the router itself, from the last model turn (model, tier, reason, latency) and the registered roster with tiers. The response carries `"answered_by": "router"` and no model is called; the trace is `route.decide → router.answer → router.respond`. A short follow-up ("why not another one") counts when the previous user turn was a gateway question — the thread is the only context a stateless gateway has.

**System prompt (server-built, every request)** — before a model is called the gateway prepends one system message: it names the registered models with tier and locality, states the routing rule, and names the model and reason this turn was routed to, with an instruction to answer from those facts. It is rebuilt per request and never stored, so a client-held thread (ADR-0010) does not accumulate copies. A client's own system message is kept after it. Sensitivity is decided on what the client sent, before the injection.

**Sensitive data (fail-closed)** — mark a request with header `X-Tower-Sensitive: true` or body `"sensitive": true`; a small keyword list (`password`, `iban`, `passport`, `confidentiel`, …; override with `SENSITIVE_KEYWORDS=a,b,c`) also flags it. A sensitive request never reaches a non-local model: every non-local candidate is removed from the chain — the classified tier included — and naming a non-local model explicitly returns `403 sensitive_cloud_blocked`. **Locality is per model, not per backend:** Ollama's hosted models (`name:cloud`) are served through the local API but count as non-local (`GET /v1/models` shows `local:false`). If no local model remains, the request is refused with the same 403 rather than sent anywhere.

Other errors: `400 bad_request` / `unknown_model` / `option_out_of_range`, `413 request_too_large`, `502 ollama_unavailable` (every candidate failed), `503 model_not_pulled` (the named or tier model is served on paper but not on disk — `run: ollama pull <model>` in the message; 403 still wins for sensitive-to-cloud), `504 timeout` (`REQUEST_TIMEOUT`, default 300s).

**Limits (both chat ingresses)** — a request body is capped at **4 MiB** (`413 request_too_large`), because the JSON is materialised before any backend is chosen. The resource knobs a client may set are bounded, since they land on the machine that hosts the model: `num_ctx` ≤ 32768, `num_gpu` ≤ 99, `num_predict` ≤ 8192, `num_thread` ≤ 256, `repeat_last_n` ≤ 4096, and `keep_alive` must be between 0 and 1h (`-1`, “never unload”, is refused). An out-of-range value is `400 option_out_of_range` with the option named, never a silent clamp. Unknown option keys pass through: Ollama ignores what it does not know.

**Generation parameters (v1.1, ADR-0009)** — beyond `max_tokens`, the request may carry `temperature`, `top_p`, `seed`, `stop` (string or list) and `response_format` (`{"type":"json_object"}` or `{"type":"json_schema","json_schema":{"schema":{…}}}`) in the OpenAI shape, plus the Ollama-native `format` (`"json"` or a schema), `options` (passthrough: `num_ctx`, `num_gpu`, `repeat_penalty`, …), `keep_alive` and `think` (`false` stops a reasoning model such as qwen3 from spending `num_predict` on chain-of-thought). All are optional; absent means model default. The Ollama backend merges the named parameters into `options` (named ones win over duplicates in the passthrough map) and sends `format` / `keep_alive` as top-level fields; the OpenAI backend forwards `temperature`/`top_p`/`seed`/`stop`/`response_format` and drops the Ollama-only knobs. The `model.generate` span records the values under `params` (kind of format, never the schema itself) and `params_forwarded: true|false` (false for a backend that only implements the v1.0 interface).

```bash
curl localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -H 'X-Client-Ref: s1/t3/critic/2' -H 'X-Agent-Role: critic' -d '{
  "model": "qwen2.5:3b-instruct", "messages": [{"role":"user","content":"Score this scene 1-10 as JSON."}],
  "temperature": 0.2, "seed": 7, "format": "json", "options": {"num_gpu": 0, "num_ctx": 8192}, "keep_alive": "30m"
}'
```

**Client reference** — a client that keeps its own record of every model call (Versatile's LangGraph writing orchestrator does: one Dexie row per Editor decision) sends `X-Client-Ref` (≤ 64 chars, opaque, e.g. `<session>/<turn>/<role>/<n>`) and `X-Agent-Role` (≤ 32 chars, e.g. `writer`, `critic`, `editor`, `director`). Both may also be body fields (`client_ref`, `agent_role`). They land as `client_ref` / `agent_role` on all three spans of the trace (`route.decide`, `model.generate`, `router.respond` — and on the failed `model.generate` span) and are echoed back as response headers, so the client can store the gateway's `X-Trace-ID` next to its own record and the Tower trace inspector can group a run's traces by agent role. They are truncated, never rejected, so a prompt can never ride along in a span attribute.

## `POST /v1/messages` (Anthropic Messages API)

The same router, spoken by Anthropic clients. The request is translated into the internal
`ChatRequest`, so routing, keys (RPM and token budgets), fail-closed sensitive handling,
fallback and spans are identical to `/v1/chat/completions` — only the wire shape differs.

```bash
curl localhost:8080/v1/messages \
  -H 'x-api-key: ak_…' -H 'Content-Type: application/json' \
  -d '{"model":"auto","max_tokens":256,"system":"be brief",
       "messages":[{"role":"user","content":"hi"}]}'
```

```json
{"id":"msg_598d…","type":"message","role":"assistant","model":"qwen2.5:3b-instruct",
 "content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","stop_sequence":null,
 "usage":{"input_tokens":12,"output_tokens":3},
 "reason":"short-simple-prompt","backend":"ollama","trace_id":"598d…",
 "tier":"fast","candidates":[{"model":"qwen2.5:3b-instruct","tier":"fast","local":true}]}
```

- **Credentials:** `x-api-key: <key>` (what Claude Code and the Anthropic SDK send) or
  `Authorization: Bearer <key>`. Both go through the same key store.
- **Content:** `system` may be a string or text blocks; `messages[].content` may be a
  string or text blocks (joined with a newline). `temp`/`top_p`/`stop_sequences` are
  forwarded; `max_tokens` defaults to the model's default when absent.
- **Streaming (`stream:true`)** emits the Anthropic event sequence:
  `message_start`, `content_block_start`, `content_block_delta` (one per token chunk),
  `content_block_stop`, `message_delta`, `message_stop`. Input tokens are reported in
  `message_delta`, not `message_start` (the backend reports usage at the end).
- **Not translated yet: `tools` / `tool_choice`**, and non-text content blocks. These are
  rejected with `400 invalid_request_error` naming the field rather than quietly dropped —
  an agentic client that receives an answer without its `tool_use` block would hang.
  Use `/v1/chat/completions` with the OpenAI tool shape until tool passthrough lands.
- **Errors use the Anthropic envelope**, not the OpenAI one:
  `{"type":"error","error":{"type":"invalid_request_error|authentication_error|permission_error|rate_limit_error|api_error","message":"…"}}`
  with `X-Trace-ID` on the header. Backend failures map to `api_error` with the same status
  codes as the OpenAI endpoint (`502`/`503`/`504`).

## `GET /v1/models`

```json
{"object":"list","data":[
  {"id":"qwen2.5:3b-instruct","object":"model","owned_by":"ollama","tier":"fast","local":true,"up":true},
  {"id":"qwen2.5:7b-instruct-q4_K_M","object":"model","owned_by":"ollama","tier":"quality","local":true,"up":true},
  {"id":"openai/gpt-oss-20b","object":"model","owned_by":"groq","tier":"cloud","local":false,"up":false}
]}
```

`up` is the last health probe (every 15 s; also `router_backend_up{backend}` on `/metrics`)
**and** presence: a served model Ollama does not have on disk reads `up:false`
with `reason:"not_pulled"` instead of failing at request time (Track J — the
prober refreshes the pulled set from `GET /api/tags` every tick; boot and every
gap log `model "X" not pulled — run: ollama pull X`). Path-like
`OLLAMA_MODELS` entries are never registered (Ollama itself uses that variable
for its models directory).

## Backends (environment)

| Variable | Default | Meaning |
|---|---|---|
| `OLLAMA_URL` | `http://localhost:11434` | local backend, always present, `local:true` |
| `FAST_MODEL` / `QUALITY_MODEL` | `qwen2.5:3b-instruct` / `qwen2.5:7b-instruct-q4_K_M` | the auto tiers on Ollama |
| `OLLAMA_MODELS` | — | comma-separated extra local models (`qwen3:8b,qwen2.5:3b-instruct`) a client may name explicitly; registered with tier `local` on the Ollama backend. A multi-agent client that places roles on different models needs every one of them registered here or the request answers `unknown_model` |
| `CLOUD_LADDER` | — | ordered list of cloud rungs, best first: `name|key|url|model` separated by commas (e.g. `groq|gsk_...|https://api.groq.com/openai/v1|openai/gpt-oss-20b,openrouter|sk-or-...|https://openrouter.ai/api/v1|meta-llama/...`). When set, it replaces the single `GROQ_API_KEY`/`CLOUD_*` backend. On exhaustion (429/402/403) a rung is benched and the next rung is tried; local Ollama is always the final rung. `CLOUD_LADDER` contains secrets — never log it |
| `EXHAUST_COOLDOWN_TRANSIENT` | `60s` | how long a rung is benched after a persistent 5xx/timeout (clamped ≥1s) |
| `EXHAUST_COOLDOWN_PERMINUTE` | `30s` | floor for the per-minute cooldown when a provider gives no `Retry-After` (clamped ≥1s); daily-quota exhaustion benches until midnight UTC regardless |
| `CLOUD_MODEL` / `CLOUD_BASE_URL` / `CLOUD_BACKEND_NAME` | `openai/gpt-oss-20b` / `https://api.groq.com/openai/v1` / `groq` | point the cloud backend at any OpenAI-style API. The old default `llama-3.1-8b-instant` was retired by Groq in 2026 and returns `model_not_found` |
| `REQUIRE_API_KEY` | `false` | reject anonymous requests |
| `REQUEST_TIMEOUT` | `300s` | per-request deadline, also the shutdown drain budget |
| `SENSITIVE_KEYWORDS` | built-in list | comma-separated override |
| `ROUTER_POLICY` | `heuristic` | `heuristic` (default) uses the built-in `ClassifyThread` (length/keywords). `learned` asks a small local model (`ROUTER_CLASSIFIER_MODEL`) to pick the tier, falling back to the heuristic on error/timeout. Phase 1 of the ML routing plan (see `docs/ML-ROUTING.md`) |
| `ROUTER_CLASSIFIER_MODEL` | `FAST_MODEL` | Ollama model used when `ROUTER_POLICY=learned` (a small model such as `qwen2.5:3b-instruct` or `phi4-mini`) |
| `ADDR` | `127.0.0.1:8080` | listen address. Loopback by default: the gateway is a local appliance, and a fresh install must not be reachable from the LAN. Set `ADDR=0.0.0.0:8080` to publish it |
| `CONSOLE_REMOTE` | `false` | the console (pages, conversations, workflows, eval runs) answers loopback clients only. `true` allows any client — required when the console is reached through a container port mapping or a reverse proxy. The console is where prompts are readable and GPU work is started, so this is an explicit act |
| `POSTGRES_DSN` | built from `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_HOST` / `POSTGRES_PORT` / `POSTGRES_DB` (defaults `tower` / `tower` / `localhost` / `5432` / `tower`, the same ones `docker-compose.yml` uses) | spans, traces, evals, workflows, api keys |
| `GPU_SMI` | `nvidia-smi` | path to the driver CLI the GPU prober shells out to. On a box without a working driver every `gpu_*` series reports `gpu_up 0` and no utilisation is published, rather than a fabricated zero |
| `GPU_POLL_INTERVAL` | `10s` | how often the driver and Ollama's `/api/ps` are read; clamped to ≥1s, parse errors fall back to the default |
| `GPU_HISTORY_KEEP` | `168h` | retention for `gpu_samples`, purged at boot; clamped to ≥1h, parse errors fall back to the default |
| `SPAN_KEEP` | `720h` | spans older than this are deleted at boot and every 24 h; clamped to at least 1 h and unparseable values fall back to the default, so a typo cannot empty the table. This is the 30-day policy `RULES.md` promises |
| `DECISION_KEEP` | `2160h` | `route_decisions` retention (90 days by default), same sweep and clamp |
| `AUDIT_KEEP` | `8760h` | `audit_log` retention (365 days by default), same sweep and clamp |
| `OTLP_ENDPOINT` | - | full OTLP/HTTP traces URL (e.g. `http://localhost:4318/v1/traces`). When set, spans are also exported with the `gen_ai.*` aliases beside the internal attributes (dual-named, because the GenAI semconv is still `Development`), batched and strictly lossy — a collector that is down never slows a request, and prompt text is never exported, even if a span carried it |

Evals and corpus (used by `--score`, `--schedule-evals`, the console's *Run eval suite*, `--ingest`, `--draft-golden`):

| Variable | Default | Meaning |
|---|---|---|
| `JUDGE_MODEL` | `qwen3:8b` | local Ollama judge (temperature 0); ignored when `JUDGE_BACKEND=groq` |
| `JUDGE_BACKEND` + `GROQ_API_KEY` | — | `groq` switches the judge to a Groq model instead of Ollama; `JUDGE_MODEL` is a local name and is ignored in this mode |
| `GROQ_JUDGE_MODEL` | `openai/gpt-oss-20b` | Groq model used by `JUDGE_BACKEND=groq`; set it to one your account can reach (`GET /openai/v1/models`). Groq retired the old default `llama-3.1-8b-instant` in 2026 |
| `EVAL_THRESHOLD` | `0.7` | faithfulness below this raises `alert` in the drift report |
| `EVAL_TIMEOUT` | `90m` | budget for one suite run (see `docs/VRAM.md` on GPU contention) |
| `EMBED_MODEL` | `nomic-embed-text` | embeddings for ingest and retrieval (768 dims) |
| `DRAFT_MODEL` | `qwen3:8b` | model that drafts golden pairs |

Ingest cache (Track Q): `--ingest` keeps SHA-256 `{hash}.json` sidecars in
`evals/corpus/cache/` (git-ignored local state, never inside the clean tree);
a re-ingest re-embeds only new/changed-model/corrupt chunks and logs
`hits/new/corrupt/model_changed`. Precedence: pgvector is rebuilt from
`clean/` + cache on every run. Recovery from any doubt: `rm -rf
evals/corpus/cache` + `--ingest` (full re-embed, always possible).

Context budget (Track R): machine-assembled prompts (judge context, researcher
context) are trimmed to `DefaultBudgetTokens` (3000) by token estimate
`chars/4` rounded up — an explicitly documented estimate (optimistic on prose,
loose on code/CJK), guardrail-only, never billing. Trimming drops
lowest-relevance chunks first (callers pass relevance order) and never empties
the context; dropped counts surface in the `--score` pair log (`trimmed=N`)
and the researcher log line. User traffic is never trimmed. The 3000 default
sits inside the 2–4K operating window (`docs/VRAM.md`) with headroom for the
estimate error.

## Conversations (cockpit shelf)

Stored threads. Prompts and answers are kept verbatim — see `PRIVACY.md`
(purpose: thread continuity; 90-day idle retention with boot purge; per-thread delete).

- `GET /v1/conversations?limit=20` — newest first: `{"conversations":[{id, title, model, messages, updated_at}]}`. `messages` is the **count** of stored turns (an integer, not an array); read `/v1/conversations/{id}/messages` for their contents. Title auto-fills from the first user message.
- `POST /v1/conversations` (`{title?, model?}`) — open a thread → `{id}`.
- `GET /v1/conversations/{id}/messages?limit=500` — `{messages:[{id, role, content, model, trace_id, prompt_tokens, completion_tokens, created_at}]}`; unknown id → `404 conversation_not_found`.
- `POST /v1/conversations/{id}/messages` (`{role: user|assistant, content, model?, trace_id?, prompt_tokens?, completion_tokens?}`) — record one turn → `{status:"stored"}`; bad role or empty content → `400 bad_request`.
- `DELETE /v1/conversations/{id}` — erase thread and messages → `204`; unknown id → `404`.

## Observability endpoints

- `GET /metrics` — Prometheus exposition (`router_requests_total`, `router_errors_total`, `router_tokens_total`, `router_latency_seconds`, `router_backend_up`, `router_key_*`, `eval_faithfulness`, `gpu_*`). The eight `gpu_*` series are `gpu_up`, `gpu_utilization_percent`, `gpu_memory_used_bytes`, `gpu_memory_total_bytes`, `gpu_power_watts`, `gpu_temperature_celsius`, `gpu_swap_events_total` and `gpu_thermal_throttled` (1 when the driver had cut clocks for heat at the last sample). With no readable driver only `gpu_up 0` and the swap counter are published; `gpu_thermal_throttled` is also absent when the driver does not answer `clocks_event_reasons.active` — "unasked" is not published as a measured 0.
- `GET /v1/traces/{trace_id}` — every span of one request or workflow (`trace_id, span_id, parent_id, name, started_at, attrs`), prompts redacted; `404 trace_not_found`. Spans are written when their work completes, so `started_at` is the end time; `attrs.latency_s` (router `model.generate`, every tracker step) gives the duration.
- `GET /v1/drift/report` — last two eval runs for the default golden version: `{score_then, score_now, delta, alert, runs, misses_now, misses_then, judge_then, judge_now, judge_changed, prompt_then, prompt_now, prompt_changed, worst_cases[{question, faithfulness|null, precision, recall, retrieval_miss}]}`. `score` averages retrieved pairs only; a pair with `recall=0` is a `retrieval_miss` (stored as `faithfulness NULL`, judge never called) and is excluded from the average. `judge_changed` (model) and `prompt_changed` (judge prompt version, `0013_eval_judge_prompt.sql`) both mean the delta is not corpus drift — the empty prompt version is "recorded before the column existed" and never equals a real one. History starts clean — runs before `0004_retrieval_miss.sql` keep their old scores.
- `POST /mcp` - MCP over HTTP for remote agents: one JSON-RPC request per POST
  (`tools/list`, `tools/call` with the same five tools as `--mcp` stdio, answered
  by the same handler). Speaks the **2026-07-28** revision (stateless, no
  handshake; `server/discover` is implemented) *and* the legacy `initialize`
  handshake back to `2024-11-05`, so old and new clients both work. The version
  comes from `params._meta["io.modelcontextprotocol/protocolVersion"]` or the
  `MCP-Protocol-Version` header; an unsupported one gets `400` with JSON-RPC error
  `-32022` and the supported list. With `REQUIRE_API_KEY=true` a valid Bearer key
  is required (`401 missing_api_key` / `invalid_api_key`, `403 api_key_disabled`,
  one error shape); the gate is the chat gate — validity, disabled, budget and RPM —
  and the tokens a model-calling tool consumed are charged to the key, so `/mcp` is
  not a way around a quota. Every call emits
  a redacted `mcp.tool` span. Bodies over 1 MB are refused with `400 bad_request`.
- `GET /v1/events` — redacted spans as server-sent events (`data: {trace_id,
  span_id, parent_id, name, attrs}` + `: ping` heartbeats) for the Tower `#/live`
  rail. In-memory fan-out, 64-deep per subscriber, slow readers drop and count —
  telemetry loss, never backpressure. Same redaction and localhost posture as the
  trace endpoint.
- `GET /health` — liveness.

## Shutdown

`SIGINT`/`SIGTERM`: the listener closes, in-flight requests finish (up to `REQUEST_TIMEOUT`), queued spans are flushed, then the process exits 0. `docker stop -t 120 tower` is the safe way to stop the container.

## Tower console (`GET /`)

The web UI is served by the same binary from embedded files — no build step, no framework. It reads the endpoints below; they are unauthenticated like `/metrics`, so put the whole thing behind a reverse proxy or firewall if it is ever exposed beyond localhost.

| Endpoint | Returns |
|---|---|
| `GET /v1/overview` | KPI header: last-hour traffic (`requests_last_hour/minute`, `p50/p99_latency_s`, `tokens_last_hour`, `errors_last_hour`, `by_model`) from spans, `models` + `backends` with health, `since_start` counters, latest `drift` for the default golden, `judge_cost_usd` (0 — local + Groq free tier), `version` |
| `GET /v1/requests?limit=30` | newest router chats first, one row per trace assembled from its `route.decide` + `model.generate` spans: model, backend, tier, reason, sensitive, latency, tokens, fallback, error |
| `GET /v1/metrics/series?window=1h\|6h\|24h` | the Metrics page's data: spans bucketed by time (1m / 5m / 15m respectively), each point carrying requests (`route.decide`), errors (`model.generate` with an error attribute), **p50/p99** latency (`percentile_cont`, never a mean) and input/output tokens. Windows are a closed set and the step is derived server-side, so a client cannot request an unbounded scan. Empty buckets are returned as zeros rather than holes; a bad window is `400 bad_request`. The query is read-only over `spans` — there is no metric store, and the measured latency is recorded in `docs/CONSOLE-REDESIGN.md` |
| `GET /v1/evals/runs?golden=v2&limit=50` | every `eval_runs` row for a golden version (`id`, `judge_model`, `judge_prompt`, `model`, `score`, `status` = `complete` / `running` / `failed`, `judged` = pairs the judge actually scored, `started_at`, `created_at`, and `gpu` when the prober sampled the run's window: sample count, mean/peak utilisation, peak and installed VRAM, swaps, `max_temp_c` and `throttled_samples` — a window where the driver had cut clocks for heat is visible as such) + its drift report. Only `complete` runs carry a score anything else may quote; an interrupted suite keeps its pair rows as evidence and the page says *incomplete* (worst cases included). A run with `judged: 0` (every pair a retrieval miss) is shown as *unmeasured* and is ignored by drift and the release gate |
| `GET /v1/benchmarks?golden=v3` | Track L comparison: latest scored run per model (same golden version), per-scenario faithfulness split by routing reason, verdict (`winner` only at n≥100 / p<0.05, else `tied — route on cost`), plus recent model-run history; differing judges invalidate |
| `GET /v1/activity?entity=&id=&limit=100` | Track S transition trail (append-only): workflow created/resumed/step_done/completed/failed and eval started/finished with ids, counts and scores — never prompts. Writers live in CLI/console call sites and never abort on failure; `0007_audit_log.sql` |
| `GET /v1/gpu?since=6h` | the prober's live snapshot plus stored history (`?since=` accepts a duration, 1m-168h, default 6h). `available:false` means the driver could not be read; it is not a zero reading. This is the one endpoint that degrades instead of 502ing: if the store is down, `now` is still returned, `history` is `[]` and `history_error` says why, because the live reading does not come from the database. Every sample carries `swaps_total`, the prober's monotonic model-swap counter (`0009_gpu_swaps.sql`), so a window's swaps are `max-min` and a finished eval run can still be explained afterwards, and `throttled` when the driver answered `clocks_event_reasons.active` (`0012_gpu_thermal.sql`; `true`/`false` from bits `0x20`/`0x40`, omitted entirely when the driver cannot be asked — unasked is not "cool") |
| `GET /v1/evals/status` | `{running, started_at, finished_at, error, golden_version}` of the console-triggered run |
| `POST /v1/evals/run` `{"golden_version":"v2"}` | starts one suite run in the background → `202`; a second while one runs → `409 eval_running` |
| `GET /v1/workflows?limit=20` | workflows newest first with their steps (`seq, name, status, attempts, output_snippet`) |
| `POST /v1/workflows` `{"input":"…"}` | starts the toy Researcher → Drafter → Reviewer workflow → `202 {id}` |
| `POST /v1/workflows/{id}/resume` | re-runs the same id (done steps skipped) → `202`; unknown id → `404 workflow_not_found`; already done → `502 workflow_failed` |

Store failures on any of these return `502 store_unavailable` in the one error shape; the UI shows an inline error with a Retry button and keeps whatever you were typing. `GET /v1/gpu` is the deliberate exception: its live half is measured in-process, so a dead store costs it the history, not the page.
