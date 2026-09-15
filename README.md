# AgentOps

A small, from-scratch **AI operations toolkit** in one Go binary: a model **router**, a **durable task tracker**, a **faithfulness eval + drift alert** loop, **OTel-shaped traces**, and an **MCP server** so an AI assistant can query all of it.

Built solo, on an RTX 4060 (8 GB VRAM), against local [Ollama](https://ollama.com) models — informed by how TensorZero, Bifrost, Temporal and Langfuse solve the same problems, but with every core piece written here so it can be explained line by line. Standard library + one dependency (`pgx`).

```
                     ┌──────────────────────────── agentops (one binary) ───────────────────────────┐
  POST /v1/chat ───► │ router: Classify(prompt) → fast 3B | quality 7B ──► Ollama /api/generate     │
                     │         ├─ Prometheus /metrics (hand-written exposition)                      │
                     │         └─ 3 spans per chat ──► async writer ──► Postgres spans               │
  --run-tracker ───► │ tracker: Researcher → Drafter → Reviewer, row-status durability, kill -9 safe │
  --score ─────────► │ evals: pgvector top-k → split answer into claims → LLM judge → faithfulness   │
                     │        eval_runs + eval_pair_scores → drift report + alert                    │
  Claude Desktop ──► │ mcp (--mcp): list_models · get_stats · route_test_request · inspect_trace ·   │
                     │             get_drift_report  (JSON-RPC 2.0 over stdio)                      │
                     └──────────────────────────────────────────────────────────────────────────────┘
```

## Status — MVP definition of done

| # | Checkbox | Status |
|---|---|---|
| 1 | Router between two local Ollama models on a rule I wrote (not 50/50), reason logged per request | ✓ code + live on `qwen2.5:3b` · 7B pull pending (network) |
| 2 | Prometheus metrics + Grafana dashboard (5 panels) | **✓ k6: 0/2725 failed @50 RPS, p99 70 ms; Grafana provisioned + populated** |
| 3 | MCP server with `list_models`, `get_stats` (+3 more) | ✓ live handshake · recording pending |
| 4 | Fresh RAG app + reviewed golden set + hand-written faithfulness scorer | **✓ golden v2 (48 pairs) faith 1.000; v1 0.969** |
| 5 | Durable tracker surviving `kill -9` mid-task | **✓ automated `TestKillResume` + live kill** |
| 6 | One trace view: pick a request, see every step | **✓ `GET /v1/traces/{id}`, CLI, MCP** |

39 tests, `go vet` clean. Full history, decisions (ADR-0001…0006) and every defect found by the review passes are in [`AgentOps_Project_Plan.md`](AgentOps_Project_Plan.md).

## Quickstart

Requirements: Go 1.22+, Docker, [Ollama](https://ollama.com) with `nomic-embed-text` and one chat model (`qwen2.5:3b-instruct` / `qwen2.5:7b-instruct-q4_K_M` by default, or set `FAST_MODEL`/`QUALITY_MODEL`), Python 3 for the corpus cleaner.

```bash
docker compose up -d                      # Postgres 16 + pgvector on :5432
docker compose --profile monitoring up -d # optional: Prometheus :9090 + Grafana :3000 (dashboard provisioned)
go run . --migrate                        # migrations/*.sql
python scripts/clean_docs.py              # corpus/*.md → evals/corpus/clean/*.jsonl
go run . --ingest                         # embed chunks with nomic-embed-text → pgvector
go run .                                  # HTTP router on :8080
```

Then:

```bash
curl -s localhost:8080/v1/chat/completions -d '{"prompt":"hi"}'
# {"text":"…","model":"qwen2.5:3b-instruct","reason":"short-simple-prompt","trace_id":"598d…"}
curl -s localhost:8080/v1/traces/598d…     # 3 spans: route.decide → model.generate → router.respond
curl -s localhost:8080/metrics             # router_requests_total, …_latency_seconds histogram, eval_faithfulness
```

Every mode is a flag on the same binary (`go run . --help`):

| Flag | What it does |
|---|---|
| `--mcp` | MCP stdio server for Claude Desktop (config in [`mcp/README.md`](mcp/README.md)) |
| `--run-tracker` / `--resume-tracker <id>` | run / resume the 3-step toy agent; kill it mid-step and resume |
| `--trace <id>` | print redacted spans for a chat or workflow |
| `--draft-golden` → review → `--freeze-golden` | build a golden set (`--golden-version vN`): LLM drafts, human reviews, validator rejects unscorable answers, hash frozen |
| `--score` / `--drift` / `--schedule-evals 24h` | run the faithfulness suite, print the drift report, or loop it |

## Layout

```
router/      classify → Ollama → metrics → spans          (stdlib net/http)
tracker/     workflows/steps row-status durability, spans   (Store iface: SQLStore + MemStore)
evals/       corpus ingest, pgvector search, golden, judge, retry/backoff, drift
mcp/         JSON-RPC 2.0 over stdio, 5 tools
migrations/  0001_init.sql, 0002_drift.sql
corpus/      15 short docs describing this system — the RAG target (v2 matches the shipped code)
dashboard/   grafana/router.json + provisioning, prometheus/prometheus.yml
load-tests/  router.js (k6: 50 RPS on /health, p99 gate)
lessons/     30 plain-language HTML lessons on this repo — open lessons/index.html
docs/        VRAM.md (swap rule), tower-design-system.html (UI mockup)
```

## Decisions worth knowing

- **pgvector, not a vector DB** — one Postgres for workflows, spans, evals *and* vectors; HNSW `m=16, ef_construction=128`. (ADR-0004)
- **Postgres-native durability, not DBOS/Temporal** — `(workflow_id, seq)` idempotency keys + `pending→running→done` rows; resume = re-run the same id and skip `done`. DBOS studied, parked. (ADR-0003)
- **Scheduler is a flag, not a service** — `--schedule-evals 24h` loops the same `scoreOnce` that `--score` calls. (ADR-0006)
- **Per-question eval rows** — `eval_pair_scores` lets the drift report name the worst cases without re-running judges. (ADR-0005)
- **Heuristic router first** — `len > 200` or a complexity keyword → quality tier. One `if` you can explain beats a classifier you can't demo.
- **Judge bias guards** — justification before verdict, negative verdicts parsed first, temperature 0, judge model + prompt version logged, `judge_changed` flag in the drift report.
- **Golden answers are standalone propositions** — the judge never sees the question, so one-word, list-fragment and "Because…" answers score 0 regardless of truth; `--freeze-golden` rejects them. Learned from v1 (0.969) → v2 (1.000).
- **Telemetry never on the request path** — spans go through a 1024-deep channel to one writer goroutine; a dead DB costs a chat nothing.

## Privacy & license

Prompts are redacted on every trace read path; see [`PRIVACY.md`](PRIVACY.md). Licensed under [Apache-2.0](LICENSE).
