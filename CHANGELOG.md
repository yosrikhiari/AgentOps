# Changelog

All notable changes. Dates are 2026.

## Unreleased

- CI hardening: per-ref concurrency with cancel-in-progress, job timeouts, least-privilege permissions, `GOTOOLCHAIN=local`, Go cache; Actions bumped (checkout v7, setup-go v7, buildx v4, build-push v7, gh-release v3) and `golang:1.27-alpine` base — the six open Dependabot PRs folded into one commit; Dependabot now groups updates per ecosystem (one PR per week each) so they stop conflicting on `ci.yml`.
- `docs/RULES.md`: the engineering rule set (problem fit, what we optimize for, success measures, architecture style, sync vs async, retries, latency, scalability, security, guardrails, testing/experiments, static analysis).
- `policy/` package: 11 executable policies (single direct dependency, prompts never in spans, sensitive never cloud, one error shape, non-blocking span sink, env vars and flags documented, contiguous migrations, ADRs indexed, no same-backend retry, rules cite real tests). Found two undocumented flags on its first run.
- CI `policy` job: `staticcheck` (v0.8.1), `govulncheck`, policy tests. `govulncheck` found GO-2026-5970 in `golang.org/x/text` (indirect via pgx) — bumped to v0.39.0.
- Documentation pass: README rewrite, CHANGELOG, `docs/adr/0001–0008`, PRIVACY rewrite, `docs/API.md` env reference, MCP README, lessons reconciled, corpus v3 (17 docs → 36 chunks) with golden v3 (72 pairs).

## v1.0.0 — 2026-09-15

The MVP becomes a product: an installable gateway with a console.

### Track A — ops foundation
- `Dockerfile` (multi-stage, distroless nonroot, 21.7 MB) and a compose `app` profile that runs the router against the host's Ollama.
- GitHub Actions: gofmt · vet · `test -race` · build · docker build on every push; release job on `v*` tags builds linux/windows/darwin binaries + `SHA256SUMS`.
- `schema_migrations` tracking: `--migrate` applies each `migrations/*.sql` once and reports the count.
- Dependabot for Go modules, Docker base images and Actions. `--version` flag stamped at release.

### Track B — gateway hardening
- **OpenAI-compatible** `POST /v1/chat/completions`: `messages[]` in, `chat.completion` out, with `text/reason/backend/trace_id/fallback` extensions. The MVP `{prompt}` body still works.
- **Streaming** (`stream: true`) as SSE in the OpenAI chunk shape; usage in the final chunk.
- **Backends**: `Backend` interface; Ollama moved to `/api/chat` with NDJSON streaming; `OpenAIBackend` for Groq or any OpenAI-style API; 15 s health prober → `router_backend_up{backend}`; `GET /v1/models`.
- **Virtual API keys** (`0003_api_keys.sql`, `--create-key NAME --key-rpm N --key-budget T`): Bearer auth, per-key requests-per-minute, lifetime token budget, 401/403/429 in the one error shape, per-key counters.
- **Fail-closed rule**: sensitive requests (header, body flag, keyword) never get a cloud candidate; explicit cloud model → `403 sensitive_cloud_blocked`.
- Graceful shutdown (drain in-flight chats, flush spans), `REQUEST_TIMEOUT` → 504, `X-Trace-ID` on every response, `X-Request-ID` echo.

### Track C — Tower console
- Web UI served from the binary at `/` (embedded HTML/CSS/JS, no framework, no build step): overview (KPIs, routing traffic, backend health, recent requests), trace inspector, evals & drift (score history, worst cases, *Run eval suite*), workflows (start/resume).
- Console endpoints: `/v1/overview`, `/v1/requests`, `/v1/evals/{runs,status,run}`, `/v1/workflows` (+ `/{id}/resume`).
- Hardening: abortable fetches, one retry on 5xx, inline error + Retry on store failure, in-place polling.

### Fixes and learnings recorded along the way
- Prometheus histogram was double-cumulated (p99 would have been wrong). Fixed + test.
- Recall could exceed 1 when a doc's two chunks were both retrieved. Fixed + test.
- Local judge sampled at default temperature; pinned to 0. Fixed + test.
- Golden answers must be standalone main-clause sentences (one-word, list, and "Because…" answers score 0). `--freeze-golden` now rejects them.
- Eval run budget is `EVAL_TIMEOUT` (default 90 min); evals and live traffic contend for the 8 GB GPU (`docs/VRAM.md`).

## v0.1.0 — 2026-09-15

The MVP as shipped (six checkboxes; see the plan §1): router between two local Ollama models with a written rule and per-request reason; hand-written Prometheus metrics + Grafana dashboard (k6 gate: 0/2725 failed at 50 RPS, p99 70 ms); MCP server with 5 tools; fresh RAG corpus + reviewed golden set + hand-written faithfulness scorer (golden v2 = 1.000); durable tracker that survives `kill -9`; trace view over `trace_id/span_id/parent_id`.
