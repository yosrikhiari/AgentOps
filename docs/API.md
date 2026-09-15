# AgentOps gateway API

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

Per-key counters: `router_key_requests_total{key}` and `router_key_rejected_total{key}` on `/metrics`.

## `POST /v1/chat/completions`

OpenAI-compatible. `model` is optional — `"auto"` (default) lets the router pick a tier from the prompt; a served model name (or `backend/model`) is used as-is.

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
  "text": "Blue", "reason": "short-simple-prompt", "backend": "ollama", "trace_id": "aa67895ea5dd4b8e"
}
```

`text`, `reason`, `backend`, `trace_id` and (when a fallback happened) `fallback` are AgentOps extensions; OpenAI clients ignore them. The MVP body `{"prompt":"…","max_tokens":N}` is still accepted.

**Streaming** — `"stream": true` returns `text/event-stream` in the OpenAI chunk shape; the last chunk has `finish_reason:"stop"` plus `usage`, `reason`, `backend`, `trace_id`, then `data: [DONE]`. A failure after the first delta is sent as a `data: {"error":…}` event.

**Routing** — `reason` is one of `short-simple-prompt` (≤200 chars, no complexity keyword → fast tier), `long-or-complex-prompt` (→ quality tier), `explicit-model`. If the chosen backend fails, the router tries the other local tier, then — for non-sensitive requests only — the cloud model; the response's `fallback` field and the `model.generate` span record what happened. Backends the health prober last saw down are skipped as fallbacks.

**Sensitive data (fail-closed)** — mark a request with header `X-AgentOps-Sensitive: true` or body `"sensitive": true`; a small keyword list (`password`, `iban`, `passport`, `confidentiel`, …; override with `SENSITIVE_KEYWORDS=a,b,c`) also flags it. A sensitive request never reaches a non-local backend: cloud is removed from the fallback chain, and naming a cloud model explicitly returns `403 sensitive_cloud_blocked`.

Other errors: `400 bad_request` / `unknown_model`, `502 ollama_unavailable` (every candidate failed), `504 timeout` (`REQUEST_TIMEOUT`, default 300s).

## `GET /v1/models`

```json
{"object":"list","data":[
  {"id":"qwen2.5:3b-instruct","object":"model","owned_by":"ollama","tier":"fast","local":true,"up":true},
  {"id":"qwen2.5:7b-instruct-q4_K_M","object":"model","owned_by":"ollama","tier":"quality","local":true,"up":true},
  {"id":"llama-3.1-8b-instant","object":"model","owned_by":"groq","tier":"cloud","local":false,"up":false}
]}
```

`up` is the last health probe (every 15 s; also `router_backend_up{backend}` on `/metrics`).

## Backends (environment)

| Variable | Default | Meaning |
|---|---|---|
| `OLLAMA_URL` | `http://localhost:11434` | local backend, always present, `local:true` |
| `FAST_MODEL` / `QUALITY_MODEL` | `qwen2.5:3b-instruct` / `qwen2.5:7b-instruct-q4_K_M` | the auto tiers on Ollama |
| `GROQ_API_KEY` | — | when set, registers an OpenAI-compatible cloud backend |
| `CLOUD_MODEL` / `CLOUD_BASE_URL` / `CLOUD_BACKEND_NAME` | `llama-3.1-8b-instant` / `https://api.groq.com/openai/v1` / `groq` | point the cloud backend at any OpenAI-style API |
| `REQUIRE_API_KEY` | `false` | reject anonymous requests |
| `REQUEST_TIMEOUT` | `300s` | per-request deadline, also the shutdown drain budget |
| `SENSITIVE_KEYWORDS` | built-in list | comma-separated override |
| `ADDR` | `:8080` | listen address |
| `POSTGRES_DSN` | `postgres://agentops:agentops@localhost:5432/agentops` | spans, traces, evals, workflows, api keys |

Evals and corpus (used by `--score`, `--schedule-evals`, the console's *Run eval suite*, `--ingest`, `--draft-golden`):

| Variable | Default | Meaning |
|---|---|---|
| `JUDGE_MODEL` | `qwen3:8b` | local Ollama judge (temperature 0) |
| `JUDGE_BACKEND` + `GROQ_API_KEY` | — | `groq` switches the judge to `llama-3.1-8b-instant` |
| `EVAL_THRESHOLD` | `0.7` | faithfulness below this raises `alert` in the drift report |
| `EVAL_TIMEOUT` | `90m` | budget for one suite run (see `docs/VRAM.md` on GPU contention) |
| `EMBED_MODEL` | `nomic-embed-text` | embeddings for ingest and retrieval (768 dims) |
| `DRAFT_MODEL` | `qwen3:8b` | model that drafts golden pairs |

## Observability endpoints

- `GET /metrics` — Prometheus exposition (`router_requests_total`, `router_errors_total`, `router_tokens_total`, `router_latency_seconds`, `router_backend_up`, `router_key_*`, `eval_faithfulness`).
- `GET /v1/traces/{trace_id}` — every span of one request or workflow, prompts redacted; `404 trace_not_found`.
- `GET /v1/drift/report` — last two eval runs for the default golden version.
- `GET /health` — liveness.

## Shutdown

`SIGINT`/`SIGTERM`: the listener closes, in-flight requests finish (up to `REQUEST_TIMEOUT`), queued spans are flushed, then the process exits 0. `docker stop -t 120 agentops` is the safe way to stop the container.

## Tower console (`GET /`)

The web UI is served by the same binary from embedded files — no build step, no framework. It reads the endpoints below; they are unauthenticated like `/metrics`, so put the whole thing behind a reverse proxy or firewall if it is ever exposed beyond localhost.

| Endpoint | Returns |
|---|---|
| `GET /v1/overview` | KPI header: last-hour traffic (`requests_last_hour/minute`, `p50/p99_latency_s`, `tokens_last_hour`, `errors_last_hour`, `by_model`) from spans, `models` + `backends` with health, `since_start` counters, latest `drift` for the default golden, `judge_cost_usd` (0 — local + Groq free tier), `version` |
| `GET /v1/requests?limit=30` | newest router chats first, one row per trace assembled from its `route.decide` + `model.generate` spans: model, backend, tier, reason, sensitive, latency, tokens, fallback, error |
| `GET /v1/evals/runs?golden=v2&limit=50` | every `eval_runs` row for a golden version + its drift report (worst cases included) |
| `GET /v1/evals/status` | `{running, started_at, finished_at, error, golden_version}` of the console-triggered run |
| `POST /v1/evals/run` `{"golden_version":"v2"}` | starts one suite run in the background → `202`; a second while one runs → `409 eval_running` |
| `GET /v1/workflows?limit=20` | workflows newest first with their steps (`seq, name, status, attempts, output_snippet`) |
| `POST /v1/workflows` `{"input":"…"}` | starts the toy Researcher → Drafter → Reviewer workflow → `202 {id}` |
| `POST /v1/workflows/{id}/resume` | re-runs the same id (done steps skipped) → `202`; unknown id → `404 workflow_not_found`; already done → `502 workflow_failed` |

Store failures on any of these return `502 store_unavailable` in the one error shape; the UI shows an inline error with a Retry button and keeps whatever you were typing.
