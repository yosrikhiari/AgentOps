# AgentOps Platform — Project Plan (v26, 2026-09-21 — Track J done live: presence check ships, 503 model_not_pulled, path-skip for OLLAMA_MODELS, purge interval fix; Track K done live: static advisor 104 tests)

**What you're trying to achieve, stated plainly, so every decision below serves it:** a
finished, demoable, fully-your-own-code project that proves you can do ML-systems-level work
(model serving, durable orchestration, evals, observability) — for your CV, on a solo timeline,
on an RTX 4060. Finishing something small beats half-finishing something impressive. Every
section below is labeled **MVP** (build this) or **Later** (real, worth knowing about, not
worth touching yet) so that doesn't get lost again.

> **Skills applied in this update (solo-MVP fit only):** `planning:blueprint +
> concise-planning + plan-writing` (atomic exit criteria, cold-start briefs, verification
> last), `project-management:feature-tracking + to-issues + progressive-estimation` (issue
> slices, progress log), `architecture:architecture-decision-records` (ADR-0003 DBOS→pg-native,
> ADR-0004 no-0002-spans-table-exists, ADR-0005 eval-runs-plus-pair-scores, ADR-0006 scheduler
> is a flag-not-a-service), `database:postgres-best-practices` (`eval_runs` +
> `eval_pair_scores`, `(golden_version,created_at)` index, `0002_drift.sql`),
> `backend:api-design-principles` (thin `GET /v1/drift/report` + `get_drift_report`, one JSON
> error shape; `--schedule-evals` reuses `scoreOnce`), `ai-ml:advanced-evaluation`
> (judge-bias guards, threshold alert + worst-cases). Nothing here adds scope — it makes the
> six checkboxes shippable.
>
> **v8 (same day, this session) — skill `phase-gate-reviewer`:** each slice judged done-done
> against a live run, not "code exists" — see the gate table in §1.1. Every "LIVE DB RUN
> PENDING" item in §8 was actually run against the local Postgres + Ollama box and the
> results are recorded; three defects the live runs exposed were fixed (see progress log);
> stale "DBOS for the tracker" lines corrected to ADR-0003. v7's `#10 schedule-min` +
> `docs/VRAM.md` kept as-is. Nothing here adds scope.
>
> **v9 (same day) — skill `deep-review`:** with the code phase complete, every file in
> `router/ tracker/ evals/ mcp/ main.go` was read end-to-end against the review's eight
> categories. 15 findings (1 critical, 3 high, 4 medium, 6 low, 1 parked); 14 fixed the same
> day with a binding test each, then the server, tracker and drift paths were re-run live.
> Details in the §8 progress log (6th pass). Still no new scope.
>
> **v10 (2026-09-15) — lessons pass:** `lessons/` added — 30 plain-language HTML lessons
> (`lessons/index.html`), one idea each, grounded in the real code, with a "try it" and three
> interview Q&As per lesson. Writing them against the code surfaced one more defect (histogram
> buckets double-cumulated, §8 7th pass — fixed + `TestHistogramBucketsAreCumulativeOnce`)
> and three doc/code mismatches (config defaults, k6 threshold vs plan target, `mcp/README.md`
> listing 3 of 5 tools), all corrected below. 35 tests green. No new scope.
>
> **v11 (same day) — golden v1 done:** all 32 pairs reviewed and frozen, full suite run twice
> live (faith 0.969, recall 1.000, drift `delta=0`). The runs exposed a recall-above-1 bug and
> a non-deterministic local judge — both fixed with binding tests (§8 8th pass). Model pull and
> k6/Grafana were declined for now; those two gates and the MCP recording remain. 38 tests.
>
> **v12 (same day) — "run a loop making all of this":** repo initialised and pushed to
> `github.com/yosrikhiari/AgentOps` (README, Apache-2.0, PRIVACY, .gitattributes); k6 installed
> and the 50-RPS gate **passed live** (0/2725 failed, p99 70 ms, median 1.47 ms); Prometheus +
> Grafana added as a compose profile with datasource + dashboard **provisioned from the repo**
> and verified populated; corpus v2 (11 docs rewritten to the shipped design, chunker keeps
> identifiers, ingest prunes stale chunks) → golden v2 drafted, reviewed, frozen (48 pairs)
> and scored **1.000** on the final run. Qwen2.5 3B pulled; the 7B pull hit repeated network
> resets and is retrying. 39 tests. Details: §8 9th pass.

> **Current state (v16, 2026-09-15).** Released **v1.0.0** on `github.com/yosrikhiari/AgentOps`
> (CI green, binaries attached). Five of six MVP checkboxes closed live; #1 waits on the 7B
> blob download, #3 on the MCP recording. v1.0 Tracks A (ops), B (gateway) and C (console) are
> done — see §9. Documentation set: `README.md` (front door), `CHANGELOG.md`, `docs/API.md`
> (every endpoint + env var), `docs/adr/` (8 decision records), `docs/VRAM.md`, `PRIVACY.md`,
> `mcp/README.md`, `lessons/` (33), and `corpus/` (v3, the RAG target, matches the code).
> Sections 1–8 below are the MVP record and are kept as history; §7 marks what has since
> shipped; §9 is the product track list; Tracks D/E/F are the next work.
>
> **Current state (v22, 2026-09-21).** Shipped since v16, all in the working tree
> (committed: v1.1 gateway ADR-0009 — generation params, client refs, `OLLAMA_MODELS`;
> uncommitted: Track N + cockpit + retrieval-miss + conversations shelf):
> **v1.1 gateway** (`d9040fe`, `13238e7`, ADR-0009) — `POST /v1/chat/completions`
> forwards `temperature/top_p/seed/stop/response_format/format/options/keep_alive/think`
> via `ParamBackend`, `X-Client-Ref`/`X-Agent-Role` on all three spans, extra local
> models via `OLLAMA_MODELS`. **Track N done** — `POST /mcp` (same `handle()` as
> stdio, Bearer enforced when `REQUIRE_API_KEY=true`), redacted `mcp.tool` spans
> (`tool/ok/code` only), `GET /v1/events` SSE fan-out (`events.go`, 64-deep lossy,
> DB-down still 200), Tower `#/try` + `#/live` built then retired into **`#/cockpit`**
> (send + sight on one surface, shared `mountRail`, old hashes fall back to
> Overview). **Retrieval-miss fix done** (§10.5 item 1) — `ScorePair` returns
> `retrieval_miss=true` with zero judge calls on `recall=0`, `eval_pair_scores`
> `faithfulness NULL` + `retrieval_miss` (`0004_retrieval_miss.sql`), suite average
> and drift report exclude misses (`misses_now/misses_then`), console `retrieval miss`
> chip. **Conversations shelf** (`0005_conversations.sql`, ADR-0010 proposed) —
> opt-in stored threads (`GET/POST /v1/conversations`, messages, `DELETE`), 90-day
> idle boot purge, per-thread delete; gateway chat path stays stateless
> (client-held `messages[]` resend, ~4000-token cap + truncated chip). `go test
> ./...` green, **85 test funcs** (was 69). Docs in step: `docs/API.md`
> (`POST /mcp`, `GET /v1/events`, conversations, drift `misses_*`),
> `mcp/README.md` (any client), `PRIVACY.md` (conversation exception + 90-day
> rule), `docs/adr/0010` (proposed), `CHANGELOG.md Unreleased` (4 bullets).
> Next: commit this batch, then Track J (presence check) before any other new track.
>
> **Current state (v23, 2026-09-21 — Tower dashboard).** `#/overview` is now the
> single dashboard: new *Models* panel (`modelsTable()`) ports all five Grafana
> panels from spans (approach A — last-hour counts, exact cumulative
> errors/tokens, sample p50/p99 labelled as such, error-rate pills); the
> `monitoring` compose profile and `dashboard/` are deleted; `/metrics` text and
> the k6 gate stay. `corpus/04-metrics.md` + frozen golden pairs still describe
> Grafana — deliberately untouched (frozen files never edited); follow-up is
> corpus v4 + golden v4 + a scored run.

---

## 0. The One-Sentence Version

Four small tools working together: a **traffic cop** (routes AI requests to the right model), a
**task tracker** (keeps multi-step AI tasks alive through crashes), a **fact-checker** (catches
the AI lying or degrading), and a **dashboard** (shows a human what happened, queryable in plain
language via MCP). You're not rebuilding vLLM or LangGraph — you're building the smart glue and
the parts that don't exist yet, fully in your own Go code where it matters for the CV story.

---

## 1. MVP Definition of Done (read this first) — with acceptance gates

This is the actual bar for "finished, ship it, put it on the CV." Nothing below this line is
required. Everything past Section 6 is reference material and backlog — real, useful, but not
blocking.

- [~] A Go binary that routes chat requests between two local Ollama models based on a routing
      rule you wrote (not hardcoded 50/50) — no LiteLLM, no third-party gateway.
      Gate: `go test ./router/...` green + Grafana shows per-request reason + p99 router
      overhead <5ms on k6 smoke (50 RPS, solo box).
      **Status 2026-09-15:** tests green; `qwen2.5:3b-instruct` pulled and serving the fast
      tier live (k6 chats + Grafana series confirm); k6 gate **met**: 0/2725 failures at 50
      RPS on `/health`, p99 70 ms (< 100 ms gate), median 1.47 ms; Grafana shows per-model
      series. **Remaining:** `qwen2.5:7b-instruct-q4_K_M` pull keeps dying on connection
      resets at ~3 MB/s (retrying) — until it lands, quality-tier requests 502 honestly
      (`ollama_unavailable`, visible on the error-rate panel).
- [x] Prometheus metrics (latency, tokens, which model handled each request) + one Grafana
      dashboard showing them. Gate: `dashboard/grafana/*.json` versioned, 3 panels minimum.
      **Closed 2026-09-14:** `/metrics` live-verified (`router_requests_total{model}`,
      `router_errors_total{model}`, `router_tokens_total`, `router_latency_seconds`,
      `eval_faithfulness`); `dashboard/grafana/router.json` has 5 panels. **Deep-review
      caught:** histogram buckets were emitted as `le=0.05` (unquoted) — Prometheus would
      have rejected every scrape, so this gate was *not* actually met before v9; fixed +
      `TestMetricsRecordedPerRequest` now asserts `le="0.05"`. **v10 caught a second one:** `Observe`
      filled buckets cumulatively *and* `Expose` summed them again, so one 10 ms request read
      `le="120"} 11` / `+Inf} 1` — `histogram_quantile` (the p99 panel) would have been wrong.
      Fixed; `TestHistogramBucketsAreCumulativeOnce` bind-checked. **v12:** `docker compose --profile
      monitoring up -d` brings up Prometheus (scraping the host router) and Grafana with the
      datasource and `router.json` provisioned; verified live: all 5 panels populated, RPS
      step on the k6 chats, 3B p99 ≈ 0.5 s, tokens 185, faithfulness stat 0.969→1.000, error
      panel showing the 7B 502s. Screenshot: open `localhost:3000/d/agentops-router`.
- [~] An MCP server exposing at least `list_models` and `get_stats` — provable via Claude
      Desktop or another MCP client. Gate: 2-min screen recording of a real MCP call.
      **Status 2026-09-14:** stdio `initialize` + `tools/list` live-verified → 5 tools
      (`list_models, get_stats, route_test_request, inspect_trace, get_drift_report`).
      **Blocked on you:** the recording.
- [x] A small, fresh RAG app (your own, not PFE code) with a golden set of 20-30 question/answer
      pairs and a hand-written faithfulness scorer that runs against it.
      Gate: `evals/golden/v1/` (all pairs hand-fixed) + scheduler with retry/backoff green.
      **Closed 2026-09-15:** v1 (32 pairs, `29dd81c5…`) scored 0.969 twice; then **corpus v2**
      (docs rewritten to the shipped design) → **golden v2** 48 pairs, `6ca78573…`, every
      answer a main-clause sentence, scored **1.000** (`eval_run 6`, recall 1.000, precision
      0.308), drift report `delta=+0.021` vs the pre-fix v2 run. `--freeze-golden` now
      rejects zero-claim and subordinate-clause answers. 6-pair discrimination check
      (1.00 good / 0.00 corrupted) still stands.
- [x] A durable task tracker running one toy multi-step agent (Researcher → Drafter → Reviewer)
      that survives a `kill -9` mid-task and resumes correctly.
      Gate: automated kill-resume test, not a manual demo.
      **Closed 2026-09-14:** `TestKillResume` green **and** live: workflow
      `f7850f82f56fe6b9` hard-killed while `drafter=running`, `--resume-tracker` finished it
      with `researcher attempts=1, drafter attempts=2, reviewer attempts=1`, workflow `done`.
- [x] One trace view: pick a request, see every step it went through.
      Gate: `trace_id/span_id/parent_id` present on every span (OTel shape).
      **Closed 2026-09-14:** live `GET /v1/traces/598d8f7a92bbdbc4` → 3 spans
      `route.decide → model.generate → router.respond`, each `parent_id` = previous
      `span_id`; `--trace f7850f82f56fe6b9` → 3 tracker step spans; unknown id → `404
      {error:{code:"trace_not_found",…,trace_id}}`; MCP `inspect_trace` same data.

That's the whole MVP. Six checkboxes. Everything else in this document is either detail on how
to build these six things well, or ideas to add *after* these six exist and work.

### 1.1 Phase gate (skill: `phase-gate-reviewer`, run 2026-09-14 against the live box)

Checks adapted to a solo Go repo: "journeys/ACs" = the per-slice `Verify:` lines in §8;
"tests mapped" = a Go test binds each Verify line; "builds clean" = `go build/vet/test ./...`.

| Slice | Tasks complete | Verify lines have a test | Builds clean | No regressions | No blockers | Verdict |
|---|---|---|---|---|---|---|
| #1 router-skeleton | ✓ | ✓ (`router_test.go`) | ✓ | ✓ | ✗ 7B pull retrying (network) | PASS (code) / HOLD (7B) |
| #2 router-metrics | ✓ | ✓ (`TestEvalFaithfulnessGauge`, metrics tests) + live k6 + Grafana | ✓ | ✓ | ✓ | **PASS** (2026-09-15) |
| #3 mcp-min | ✓ | ✓ (`server_test.go`, 5 tools) | ✓ | ✓ | ✗ recording owed | PASS (code) / HOLD (recording) |
| #4 corpus-min | ✓ | ✓ (`corpus_test.go`, `TestIngestPrunesStaleChunks`) + live v2: 15 docs/25 chunks | ✓ | ✓ | ✓ | **PASS** |
| #5 golden-v1 | ✓ reviewed 32/32, frozen `29dd81c5…` | ✓ `TestValidateGolden` (freeze gate) | ✓ | ✓ | ✓ | **PASS** (2026-09-15) |
| #6 scorer-v1 | ✓ | ✓ (`scorer_test.go`) + live 6-pair good/bad + full v1 run (see 8th pass) | ✓ | ✓ | ✓ | **PASS** |
| #7 tracker-min | ✓ | ✓ (`TestKillResume`, bind-checked) + live kill | ✓ | ✓ | ✓ | **PASS** |
| #8 trace-min | ✓ | ✓ (`TestChatEmitsThreeSpans`, `TestInspectTrace` not-found) + live | ✓ | ✓ | ✓ | **PASS** |
| #9 drift-min | ✓ | ✓ (`TestRecordAndDriftReport`) + live 2-run alert | ✓ | ✓ | ✓ | **PASS** |
| #10 schedule-min | ✓ | ✓ (shares `scoreOnce`, covered by #9's tests) | ✓ | ✓ | ✓ (nightly cron parked until v1) | **PASS** |
| deep-review (v9) | 14/15 fixed | ✓ one new/extended test per fix, bind-checked where the fix is logic | ✓ | ✓ 34 tests green | 1 parked (resume lease) | **PASS** |
| lessons pass (v10) | 30/30 lessons + 1 fix | ✓ `TestHistogramBucketsAreCumulativeOnce`, bind-checked | ✓ | ✓ 35 tests green | — | **PASS** |
| golden pass (v11) | v1 frozen, 2 live runs, 3 fixes | ✓ `TestValidateGolden`, `TestRecallCountsUniqueDocs`, `TestOllamaJudgeSendsTemperatureZero` | ✓ | ✓ 38 tests green | — | **PASS** |
| loop pass (v12) | repo, monitoring, k6, corpus v2, golden v2 = 1.000 | ✓ `TestIngestPrunesStaleChunks`, subordinate-clause case in `TestValidateGolden` | ✓ | ✓ 39 tests green | 7B pull (network) | **PASS** (1 hold) |

**Verdict:** five of six checkboxes closed live. #1 waits only for the 7B blob to finish
downloading (retrying automatically); #3 waits for the 2-minute MCP recording only you can do.

---

## 2. What Already Exists (study, don't rebuild)

### 2.1 Router layer
- **LiteLLM** — unified doorway to 100+ providers, ~10-20ms overhead, free. You're not using it
  (independence goal), but its routing-strategy docs are worth reading.
- **Bifrost** (Go, `maximhq/bifrost`) — the closest reference to what you're building: a
  from-scratch Go gateway with virtual keys, budgets, and a native MCP gateway. Read
  `core/bifrost.go` and `transports/bifrost-http/` for shape, don't import it.
- **vLLM** — the real serving engine (batching + PagedAttention memory management). Needs a real
  GPU workload to show its value; at solo/8GB scale, Ollama is the right starting point. Revisit
  vLLM only if you want to demo batching behavior specifically, later.
- **Ollama** — simple, single-request-at-a-time, right for Phase 1.

### 2.2 Eval layer
- **RAGAS** — RAG-focused: retrieval quality + answer faithfulness, no pre-written answers
  needed.
- **DeepEval** — broader, pytest-style, 50+ metrics including agent/trajectory metrics (tool
  selection accuracy, tool-call correctness) alongside RAG metrics. Both ship a **Synthesizer**
  that generates draft golden sets from your own documents — this is the standard way real teams
  build eval datasets, not writing 30-50 pairs from scratch by hand.
- **Confirmed real, useful practice from their own docs and community threads:** generate
  synthetic goldens from source docs, then manually review every one before trusting it as
  ground truth — a bad golden set silently invalidates every later score.
- **Known real pitfall** (from an actual practitioner forum thread, not hypothetical): running
  many parallel LLM-judge calls hits timeouts and flaky 500s. Build retry/backoff into your
  scheduler from day one.

### 2.3 Durable orchestration layer
- **Temporal** — industry standard, real production usage (well-funded, actively developed).
  Core concept worth internalizing: Workflow code must be deterministic (no direct I/O, clock
  reads, or randomness inside the workflow body) — side effects go through Activities.
- **DBOS** — smaller, Postgres-native durable execution. **Verified: it has an official, real Go
  SDK** (`go get github.com/dbos-inc/dbos-transact-golang/dbos`), MIT-licensed, decorates Go
  functions with workflow/step semantics and persists state to Postgres you already run.
  Studied for the durability shape; **not adopted** — ADR-0003 keeps the tracker
  Postgres-native (stdlib + pgx row status), see Section 4, Phase 4.
- **LangGraph** — defines agent step graphs, but doesn't itself guarantee crash survival; that's
  the gap Temporal/DBOS fill underneath.

### 2.4 Observability layer
- **Prometheus + Grafana** — you already know this, use as-is for the numbers-over-time view.
- **Langfuse / Arize Phoenix** — OTel-native trace models built for AI systems specifically.
  Study their `trace`/`span` schema shape, implement your own version of it so a real tool could
  ingest your traces later if you ever wanted to plug one in.

### 2.5 Honest market context
**TensorZero** already combines gateway + observability + eval + experimentation into one
actively-developed open-source stack. Don't pitch your project as novel — pitch it as *"I built
a focused version of this pattern for a specific case, informed by studying how TensorZero,
Bifrost, and Langfuse solved the same problems."* That's a stronger, more credible interview
answer than claiming originality a knowledgeable interviewer will immediately question.

Smaller, solo-scale prior art worth 10 minutes each: **local-llm-router** (tiny, single-purpose
complexity classifier — good scoping example), **llm-localfirst** (one hard rule: sensitive data
never silently falls back to cloud — good model for making your router opinionated), and
**llm-eval-router** (shadow-tests a local model against a cloud model and auto-promotes it once
proven — this is your Section 7 stretch goal, not MVP).

---

## 3. Locked Decisions

- **Language:** Go throughout — router, tracker, eval scoring, MCP server.
- **Hardware:** RTX 4060, 8GB VRAM assumed (flag me if it's the 16GB Ti variant). Caps you to
  ~3B models comfortably or a 7B at 4-bit quant with a modest (2-4K token) context window.
  One model resident at a time — document the swap rule in `docs/VRAM.md`.
- **Independence, clarified:** no wrapping a competing whole-product (TensorZero, LiteLLM,
  DeepEval-as-a-dependency, Helicone). Using a small, focused infrastructure primitive like the
  DBOS Go SDK is fine — that's normal engineering, the same way using a Postgres driver isn't
  "cheating." The line is: does this library do the *thing your CV story is about* for you? If
  yes (a whole eval framework, a whole gateway), write your own. If no (durable-execution
  primitives, an HTTP router, a Postgres driver), use the well-built free thing.
- **Repo structure:** one monorepo, single `go.mod`, packages `/router`, `/tracker`, `/evals`,
  `/mcp`, `/console` (v1.0) plus `/dashboard` assets. ADR-0001 (`docs/adr/`). `handler → service → repo` inside each package; one JSON
  error shape `{error:{code,message,trace_id}}`; `config.go` reads env with a default for
  every value (nothing is required — the trailing empty-check is dead code, kept harmless).
- **Phase 1 model pair:** Qwen2.5 3B-Instruct (fast) + Qwen2.5 7B-Instruct Q4 (quality) — same
  family, genuine speed/quality tradeoff, both fit in 8GB with a modest context window.
- **Vector store:** pgvector. Not "pgvector or Qdrant" — pick one to actually build. pgvector
  wins for solo use: zero extra containers, lives in the Postgres you already run for the
  tracker, backs up as one thing. HNSW defaults to start: `m=16, ef_construction=128`.
  Your OpenSearch/Qdrant knowledge is parked (Section 7), not lost — the hybrid weights you
  proven in GED (BM25 0.6 + vector 0.4, RRF k=60) stay portable if you ever switch.
- **RAG defaults (MVP, from GED lessons, small-corpus tuned):** embeddings `nomic-embed-text`
  via Ollama (137M, always resident) — keep `qwen3-embedding:8b` as a batch-only flag, never
  resident next to the chat models on 8GB. Chunks ~800-1000 chars / 100-150 overlap for the
  tiny 15-20-doc corpus (GED's 2000/500 was for huge French PDFs — too coarse here). Minimal
  cleaner: strip HTML/nav → normalize → chunk → dedup by hash. Add one mapping test so a
  `status=Indexed`-style filter can never silently match nothing (your GED bug lesson).
- **Postgres tables (as shipped, v22):** `docs`, `chunks(embedding vector(768), HNSW)`,
  `workflows`, `steps`, `spans(trace_id, span_id, parent_id, attrs JSONB)`, `eval_runs`,
  `eval_pair_scores` (0002 + 0004: `faithfulness` nullable, `retrieval_miss bool`,
  misses excluded from suite average and drift), `api_keys` (0003, v1.0),
  `conversations` + `messages` (0005, cockpit shelf — prompts stored verbatim,
  purpose-limited, 90-day idle boot purge, per-thread delete),
  `schema_migrations` (v1.0, tracks applied files). Migrations in
  `migrations/*.sql`, applied once each by `--migrate`. Spans retention 30d
  (policy), evals forever, conversations 90d idle.
- **Golden set:** generate a draft with an LLM from your own source documents (DeepEval/RAGAS
  Synthesizer pattern), manually review and correct every pair before use. `evals/golden/v1/`
  + hash; bump version on any doc change.
- **Judge model, corrected with real numbers:** local Qwen2.5 7B-Q4 as the everyday judge (free,
  private, runs constantly), Groq's `llama-3.1-8b-instant` as the frequent cross-check (30
  req/min, **14,400 req/day** — plenty of headroom), and Groq's `llama-3.3-70b-versatile`
  reserved for a small number of stricter weekly spot-checks only (30 req/min but just **1,000
  req/day** and a tight 12K-tokens/min ceiling that binds before the request cap does). Free
  Groq account, no card required. Build in retry-on-429 and a hard $0 cost cap (you should never
  need to pay for this). Minimal bias guards: justification-before-score, swap order on
  pairwise, log judge model + prompt version with every score.
- **Toy RAG target:** build fresh, don't reuse PFE/Neoledge-owned code.

---

## 4. Build Order — MVP Path (this is what you actually do)

### Phase 1 — Router (3-4 weeks)
- Go HTTP client calling Ollama's API directly for Qwen2.5 3B + Qwen2.5 7B-Q4.
- One real routing rule (not round-robin): heuristic first — short/simple prompts to 3B,
  longer or flagged-complex prompts to 7B. Log the decision reason.
- Prometheus metrics: latency, tokens, which model handled each request.
- **Done when:** you can hit your router, watch a Grafana panel update, and explain in one
  sentence why each request went where it went.

### Phase 2 — MCP control surface (1 week)
- MCP server wrapping the router: `list_models`, `get_stats`, `route_test_request`.
- **Done when:** you can ask Claude Desktop "which model handled the most traffic today" and get
  a real answer back through your own server.

### Phase 3 — Fact-checker (3-4 weeks)
- Build a small, fresh RAG app (your own docs — could genuinely be as small as ~15-20 short
  documents on any topic you know well).
- Generate a draft golden set (20-30 pairs) with an LLM from those docs, review and correct all
  of them by hand.
- Write your own faithfulness scorer (LLM-judge call checking claims against retrieved sources)
  and retrieval precision/recall calculation — read DeepEval/RAGAS's metric *definitions* first,
  implement the logic yourself.
- Retry/backoff on judge calls from day one (real pitfall, not hypothetical — see Section 2.2).
- Schedule the eval suite to run automatically; log scores to Prometheus; flag if faithfulness
  drops below a threshold.

### Phase 4 — Task tracker (1-2 weeks — shorter now)
- Use Postgres-native durable steps in `tracker/` (stdlib + pgx, no new dep) rather than
  vendoring the DBOS Go SDK for MVP — same tables (`workflows/steps`) from `0001_init.sql`,
  same durability shape (row status + idempotency keys). DBOS studied (Workflow/Step/Queue
  docs), parked as alternative. Wire a toy Researcher → Drafter → Reviewer agent through it.
- Prove it: `kill -9` the process mid-workflow, restart same workflow ID, confirm it resumes
  rather than restarts from scratch.
- Add OTel-style tracing so each step is a span (`trace_id=workflow_id`).

### Phase 5 — Dashboard + trace view (2 weeks)
- Grafana panels for the numbers-over-time view.
- One request-level trace view (Langfuse-shaped: `trace_id / span_id / parent_id`) — click a
  request, see every step, retrieval, and score.
- Extend MCP with `get_drift_report`, `inspect_trace`.

**Total: ~10-13 weeks part-time** — Phase 4 came in at the short end because the tracker is
~250 lines of stdlib+pgx over tables that already existed (ADR-0003), not a new dependency.
Each phase is independently demoable.

---

## 5. What To Study, In Order

0. `lessons/index.html` — 30 short lessons on this repo itself (one idea each, real code, a
   "try it", three interview Q&As). Read in order once; then use as interview prep.
1. Ollama's API docs (direct Go calls, no LiteLLM).
2. DBOS Go SDK quickstart — `go get github.com/dbos-inc/dbos-transact-golang/dbos`, read the
   Workflow/Step/Queue docs.
3. DeepEval's and RAGAS's RAG metric definitions (Faithfulness, Contextual Recall/Precision) —
   for the math, not the package.
4. Langfuse or Phoenix trace data model — for the schema shape.
5. Bifrost's Go source (`core/bifrost.go`) — for router structure ideas, study only.
6. MCP server basics (you already know this).

---

## 6. Decided vs. Open

**Decided:** language, hardware, independence definition, repo layout, model pair, vector store,
golden-set method, judge model + real rate limits, toy RAG target, Postgres-native tracker
(ADR-0003: stdlib+pgx row-status durability; DBOS Go SDK studied, not a dependency).

**Decided since (2026-09-13/15):** corpus = 15 self-written AgentOps docs (now v3, matching
the shipped code); router classifier = heuristic (length + keywords), learned classifier /
shadow-test stays Track F. Nothing in this section is open any more; the original two
questions are kept below for the record.

**Originally open:**
- Which ~15-20 documents will you use as your fresh RAG corpus? Pick a topic you can write
  confidently about, so manually reviewing the golden set is fast and accurate. Good cheap
  options: your own AgentOps docs, Tunisia travel notes, or cooking — anything with short,
  factual pages. Avoid huge PDFs for MVP (your GED 2000/500 chunking was for those — MVP
  wants 800-1000-char chunks).
- Do you want the router's complexity classifier (Phase 1) to be a simple heuristic (prompt
  length, keyword flags) or a tiny learned classifier? Heuristic is faster to ship and still a
  legitimate "routing decision" for the CV story — start there, upgrade later if you want.
  Recommendation: heuristic now. One `if` you can explain beats a classifier you can't demo.

---

## 7. Backlog — Real Ideas, Not MVP, Don't Touch Yet

Everything here is legitimate and some of it is genuinely good. None of it blocks Sections 1-6.
Pull from this list only after the six MVP checkboxes are all checked.

**Stretch feature:** shadow-test auto-promotion (inspired by `llm-eval-router`) — run the 3B and
7B side-by-side on the same requests, score both, statistically promote the cheaper one for a
task type once proven equivalent (needs a real sample-size gate, e.g. n≥100, p<0.05 — not vibes).

**Hardening, once the MVP is solid:**
- ~~A one-page threat model (STRIDE-lite)~~ — `docs/RULES.md` §9 (v17), each threat with its control and enforcing test. ~~Fail-closed rule~~ — shipped in v1.0 Track B for
  routing (ADR-0007); the eval judge is still local-by-default only, not enforced.
- SLOs per phase (e.g., router overhead budget, judge scheduler success rate). ~~Basic load
  test~~ — k6 gate shipped and passed (0/2725 at 50 RPS, p99 70 ms); formal SLOs not written.
- ~~ADRs~~ — written 2026-09-15: `docs/adr/0001…0008` (monorepo, ollama-first, pg-native
  durability, pgvector, eval rows, scheduler-as-flag, backends + fail-closed, console).
- ~~CI~~ — shipped in v1.0 Track A (gofmt/vet/race/build/docker + release on tags).
- Trajectory evals (tool-selection accuracy, ordered tool-call match) once the basic faithfulness
  scorer works — this is the natural "v2" of Phase 3, not part of v1.
- Hybrid BM25 + vector search tuning, reranking (`bge-m3`), chunking-strategy experiments —
  real improvements to retrieval quality, but premature before the basic RAG app and eval loop
  exist. Your GED hybrid numbers (0.6/0.4, RRF k=60) are parked here as the starting point
  when you get there.
- Vector-store comparison (OpenSearch 2-node vs Qdrant vs pgvector) — parked. pgvector is
  decided for MVP; your prior art with all three stays as experience, not as MVP work.
- ~~**License + privacy**~~ — Apache-2.0 `LICENSE` and `PRIVACY.md` shipped with the first
  push (2026-09-15); PRIVACY rewritten in the v16 doc pass (the gateway never stores prompts).
  Still open: an automatic 30-day sweep of `spans` (policy only today).
- ~~**Dashboard hardening**~~ — shipped in v1.0 Track C (AbortController per page, retry on
  5xx, inline error + Retry, empty/loading states).
- **Supply-chain hygiene:** ~~Dependabot~~ and ~~`go vet` in CI~~ shipped (Track A); SBOM on
  release tag still open.
- **Tracker span parentage (Later, cosmetic):** tracker step spans 1..n carry
  `parent_id = workflow_id`, which is the trace id, not a span id. Router spans already chain
  `parent_id = previous span_id`. Cheapest fix: emit one root `workflow` span whose
  `span_id = workflow_id` on first `EnsureWorkflow` (guard against re-emit on resume). 20
  minutes; only matters if you ever export to Langfuse/Phoenix.
- **Workflow terminal states (Later):** `workflows.status` is now set to `done` on completion;
  a failed step still leaves it `running` (which is honest — a resume can finish it). Add
  `failed` + an attempts cap only if a real "give up" policy is ever needed.
- **Resume lease (Later, from deep-review #15):** two processes resuming the same workflow ID
  at once would both run the pending step — there is no `running`-row lease/heartbeat. Solo
  demo never does this; add `SELECT … FOR UPDATE SKIP LOCKED` + `locked_until` only if a
  second worker ever exists.
- ~~**Corpus v2**~~ — done 2026-09-15 (9th pass): golden v2 = 1.000. Still optional: pass
  Ollama `seed` alongside `temperature: 0` and check whether HNSW tie order changes the
  retrieved CONTEXT between runs.
- **Golden authoring rule (learned, now enforced):** an answer must be a standalone
  main-clause proposition — no one-word answers, no bare lists, no "Because…" clauses. The
  judge never sees the question. `ValidateGolden` enforces the mechanical part.
  Until then, remember in interviews that the corpus is a *RAG target*, not documentation.
- **Thinking models + `max_tokens` (Note):** `qwen3:8b` spends `num_predict` on its `<think>`
  block, so `max_tokens: 8` returns empty text. Not a router bug; disappears with the Qwen2.5
  pair (non-thinking). If qwen3 stays, pass Ollama `think:false` per model — not done because
  Ollama 400s on models that don't support the flag.

**Explicitly not doing, and why:** a formal vector-database bake-off (pgvector is decided), a
Kubernetes deployment (Docker Compose is enough for a solo demo), a full production runbook
(write one if this ever gets real users, not before).

---

## 8. MVP Execution Slices (from skills — start here, in this order)

Built with `planning:blueprint` (cold-start briefs — a fresh agent can run any slice without
reading the others) + `planning:plan-writing` (each task verifiable, verification last).
Tracer-bullet issues, each ≤3 days except scorer/tracker (5d), each demoable. Create these as
GitHub issues when you start; close in order. Stop rule: if any slice slips >1 week, cut
Section 7 first. Never cut tests.

Progress log (skills: `project-management:feature-tracking`, `phase-gate-reviewer`, `deep-review`):
**2026-09-22 (22nd pass, Tracks O–T drafted).** ECC research → six portable
mechanisms → brainstormed (all six as §9 tracks, NFRs locked, approach A:
six small dependency-ordered tracks) → drafted above. No code yet; each track
gets grilled (multi-agent-brainstorming) pre-code, then TDD + gate + live proof
where applicable.
**2026-09-22 (21st pass, post-push E2E sweep).** Server 21:41 binary (all pushed
code): health ok; both tiers up, no junk entries; fresh short→3B / long→7B
chats with chained 3-span traces; overview `by_model` + `since_start` live
(Tower Models panel inputs); requests, evals/status idle, workflows done,
conversations shelf; MCP stdio (5 tools) + HTTP `inspect_trace` real spans;
sensitive→local 3B; explicit unknown→400 `unknown_model`; tracker happy path;
SSE rail streaming redacted spans; `--drift/--migrate/--version` ok; console
serves index + benchmarks nav + tower.css. v3 full L runs still queued
(need quiet GPU + foreground hours).
**2026-09-21 (20th pass, commit + L endpoint proven live).** Committed c70e724
(53 files: dashboard removal, J/K/L code, purge fix, docs, ADRs) + merged
origin README commits, pushed (remote == local). Live on the restarted
server: `GET /v1/benchmarks?golden=e2e-bench` over two committed model runs
(ids 7/8, same judge) → comparable, per-model faith/latency/tokens, two
reason scenarios, verdict + scenario verdicts all `tied — route on cost
(n<100)`; `#/benchmarks` nav served. Duplicate 3B rows (ids 5–7, from
relaunched attempts) are harmless — latest-per-model wins. Still queued: full
v3 A+B runs (hours each, need quiet GPU + reliable foreground launch —
background `Start-Process` proved flaky on this box).
**2026-09-21 (19th pass, full local gate).** `gofmt` clean, `go vet` clean,
`go test ./...` 8 pkgs green, `policy/` green, `staticcheck@v0.8.1` clean,
`govulncheck` no vulnerabilities. Environment notes: `GOTOOLCHAIN=auto` fails
re-execing into a newer toolchain on this box (pin `GOTOOLCHAIN=go1.26.8` for
the two `go run` scanners); `-race` needs cgo (CI covers it); k6 + browser
driver absent (curl + `node --check` stand in).
**2026-09-21 (18th pass, Track J done + purge fix, all live).** TDD
(red-green: router build-fail, policy 200-vs-503, main path-registered).
Shipped: `OllamaClient.PulledModels` (`:latest`-strip), `PresenceReporter` +
per-backend pulled sets refreshed every probe tick, `presentRefs` (unknown
never filters), `ErrModelNotPulled` → 503 + pull hint, `/v1/models`
`up:false` + `reason:not_pulled`, gap log once-per-model at boot + ticks,
`OLLAMA_MODELS` path-skip, ADR-0011, API + RULES + policy row. Live on this
box: boot logs the skip + gap lines; fake fast tier → auto AND explicit both
503 in the one error shape; pulled 7B serves 200; junk path entry gone from
`/v1/models`. E2E also exposed a dead retention path: the 90-day purge never
ran (`($1 || ' days')` types the param text vs Go int) — fixed to
`make_interval(days => $1)` (RED test `TestPurgeConversationsBuildsTypedInterval`),
live proof: backdated thread purged at boot (`dropped 1`). 98 test funcs,
then Track K the same day (advisor package, `--advise`, ADR-0012 — 104).
`gofmt/vet/test/policy` green. Next: Track K.
**2026-09-21 (17th pass, full E2E sweep on this box).** Postgres `agentops-e2e-pg`
(:15432) already up; both chat models pulled (3B + 7B-Q4 — the v16 7B hold is
over here) + `nomic-embed-text`. `go build/vet`, `go test ./...` (7 pkgs),
`policy/` all green; `gofmt` clean. Live, one by one: `--migrate` → applied 0
(0001–0005 already recorded, 11 tables); `--ingest` → 36 chunks; short chat →
3B `short-simple-prompt`, long chat → **7B `long-or-complex-prompt`** (first
two-model proof — MVP #1's remaining hold closes wherever both blobs are on
disk); both traces 3 chained spans; `/metrics` quoted `le` + per-model
counters (3B 59 tok / 7B 1139 tok); `/v1/overview` `by_model` + `since_start`
(the Tower Models panel's exact inputs) + drift shape with `misses_*`;
`/v1/requests`, `/` 200; `POST /mcp` 5 tools + real `get_stats`; `mcp.tool`
span redacted over `/v1/events` SSE; `--run-tracker` done + CLI `--trace` 3
step spans + workflows API; scorer subset 3 good → 1.000, 3 corrupted → 0.000
+ `alert=true` (judge `qwen3:8b`); `--drift v3` shape OK, empty history honest.
Defects found (recorded, not hidden): (a) `--trace` without `POSTGRES_PORT`
fails on :5432 defaults — operator error, not code; (b) `/v1/models` advertises
`C:\Users\yosri\.ollama` as a tier-`local` model — root cause: Ollama's own
`OLLAMA_MODELS` (models *directory*) collides with our extra-models env var;
Track J's presence check will surface it as `not_pulled`, root fix (skip
path-like entries) rides with J. k6 + browser checks not run on this box
(no k6, no browser driver); `-race` needs cgo (CI covers it).
**2026-09-21 (16th pass, Tower dashboard replaces Grafana).** Decision A from
the dashboard brainstorm (keep `/metrics`, delete the stack, Tower is the
dashboard; corpus/golden/lesson history untouched by golden discipline).
`console/static/app.js`: `modelsTable(ov, reqs)` + one panel line in
`pageOverview` — per-model last-hour (`by_model`), since-start requests/errors/
tokens, sample p50/p99 (labelled "last 30"), error-rate pills; `tower-empty`
with curl next-action when no traffic; existing page-level skeleton/error kept.
No new Tower classes, no hex, `h()` only; example added to
`docs/tower-design-system.html` (Data display, cites `modelsTable()`).
Deleted: `dashboard/` (router.json, provisioning, prometheus.yml) + compose
`monitoring` profile + `.dockerignore` line. Updated: `README.md` (3 spots),
`CHANGELOG.md Unreleased`. `go test ./...` + policy gate re-run (see below).
Follow-up recorded: corpus v4 (`04-metrics.md`, `14-monorepo`) + golden v4
(Grafana-questions) + scored run — needs a quiet-GPU window, not this change.
**2026-09-21 (15th pass, v1.1 + Track N + cockpit + retrieval-miss — this update).**
v1.1 gateway (committed `d9040fe`/`13238e7`, ADR-0009): generation params end to
end (`temperature/top_p/seed/stop/response_format/format/options/keep_alive/think`),
`ParamBackend` on both backends, `X-Client-Ref`/`X-Agent-Role` (64/32 chars) on all
three spans + echo, `OLLAMA_MODELS` extra locals, `params`/`params_forwarded` on
`model.generate` (schema kind only), `agent_role` chips in console.
Track N (working tree): `POST /mcp` via `HandleOne` (same `handle()`, 1 MB cap,
Bearer when `REQUIRE_API_KEY=true`), `mcp.tool` spans redacted
(`TestPolicyMCPToolSpansRedacted`), `spanWriter.Hub` fan-out + `GET /v1/events`
SSE (`events.go`, 64-deep per subscriber, drop+count, DB-down still 200,
`TestEventsDropNotBlock`), `#/try` + `#/live` built then retired into `#/cockpit`
(shared `mountRail`, own ticket pinned amber, old hashes → Overview).
Cockpit "Run as workflow" posts last input to `POST /v1/workflows` (no backend
change); conversation client-held per ADR-0010 (resend `messages[]`, ~4000-token
cap + truncated chip) with opt-in shelf (`0005`, 5 endpoints, boot purge 90d,
`TestPolicyConversationsPrivate`). Retrieval-miss fix (§10.5 item 1 done):
`ScorePair` short-circuits on `recall=0` (zero judge calls), `faithfulness NULL`
(`0004`), averages/drift exclude misses, worst-cases sort misses first, console
`retrieval miss` chip + `–`. NaN-guard: `writeJSON` marshals first, unencodable →
loud 502 (console + `/v1/drift/report`). `go test ./...` green, 85 test funcs;
`gofmt/vet/policy` gate run before commit. Still yours: 7B pull, MCP recording.
**2026-09-15 (14th pass, console shell).** User feedback on a 1900 px screen: the 64 px icon rail
looked bare, the empty detail panel was a void, the OS scrollbar glared, the strip scrolled away.
New shell: labelled sidebar with sections and a status footer, sticky header with page title,
themed scrollbars, 1480 px content cap, hover states; Traces auto-opens the latest trace and
orders spans parent-first (a reconstructed start had sorted `model.generate` above
`route.decide`). Verified at 1900×1060 and at the pane's default width.
**2026-09-15 (13th pass, Traces page UX).** `#/traces` was an input box with no data. Now: list of
recent traces (router chats + workflows, filters, relative time, chips) → waterfall + indented
span tree with expandable attrs, Copy id / JSON, deep links keep the list, empty state with a
next action; strip refreshes on every route. Backend: `Span.StartedAt` in the trace JSON;
tracker steps emit `latency_s` + `attempts` (spans are emitted at completion, so the timeline
places a bar at `started_at − latency`). Verified in the browser on a fresh workflow (5.17 s /
5.94 s / 14 ms, 11.21 s end to end) and a sensitive chat that fell back to the 3B.
**2026-09-15 (12th pass, market study → security fix).** `docs/MARKET.md` written from
verified sources: TensorZero archived 2026-06-12 (founders returned most of a $7.3M seed),
Langfuse → ClickHouse (Jan 16), Helicone → Mintlify (Mar 3, maintenance mode), Promptfoo →
OpenAI (Mar 9); survivors are enterprise gateways (LiteLLM, Portkey, Bifrost, AISIX) and
cloud-plane eval platforms. Ollama itself now serves hosted `name:cloud` models through the
same local API. **That last fact was a hole here:** `OllamaClient.Local()` was true for every
model, and `Plan()` only filtered non-local *fallbacks* — a sensitive prompt classified to a
quality tier configured as `deepseek-v4-pro:cloud` would have left the machine as its
*primary* route. Fixed: `ModelRef.Local()` (backend local AND model not `:cloud`/`Cloud`),
`Plan()` drops every non-local candidate for sensitive requests including the primary and
refuses with 403 if none remain; `TestCloudSuffixModelIsNotLocal` + policy case. Study
conclusions: beachhead = individuals/small teams/regulated shops in front of Ollama; P0 =
`:cloud` fix (done), hybrid retrieval (golden v3 pair 12 was the first vector-only miss),
retrieval-miss reported separately from unfaithful, OTLP `gen_ai.*` export, Anthropic
Messages API. **Golden v3 result** (`eval_run 7`, quiet GPU, 8 min): faith **0.972**, recall
0.972, precision 0.325; the two zeros (pairs 12 and 66) are both recall-0 retrieval misses
— every pair whose chunk was retrieved scored 1.000. Default golden version → v3; P1 = shadow-test promotion, sovereignty policy engine, spend in currency, opt-in
encrypted capture, MCP tool-call governance; P2 = installers, Open WebUI recipe, Promptfoo
import, migration guides, multi-replica correctness. 69 tests.
**2026-09-15 (11th pass, rules + executable policies).** `docs/RULES.md` written: problem fit,
optimization order, success measures, architecture (modular monolith, sync core + bounded
async edges, explicitly not event-driven and why), the sync/async rule (bounded, non-blocking,
observable, safe to lose), a retry matrix per call site, latency and scalability budgets with
their honest limits, STRIDE-lite security table, runtime guardrails, testing + experiment rules,
static analysis. `policy/` package: 11 tests that fail the build on a broken rule; the very
first run caught `--drift-golden` and `--tracker-input` missing from README. CI gained a
`policy` job (staticcheck v0.8.1, govulncheck, policy tests); govulncheck found GO-2026-5970
in `golang.org/x/text@v0.29.0` (indirect via pgx) — bumped to v0.39.0, clean. Also: the v3
score run was killed with the previous session at pair 15 (nothing written) and re-run.
68 tests.
**2026-09-15 (10th pass, v1.0 Tracks A/B/C — see §9 for per-ticket proof).** Track A: Docker
(21.7 MB distroless), CI (gofmt/vet/race/build/docker), release job → `v0.1.0` with 3 binaries,
`schema_migrations`, Dependabot. Track B: `Backend` interface, Ollama on `/api/chat` + NDJSON
stream, OpenAI-style cloud backend, OpenAI request/response shape + SSE, health prober +
`router_backend_up`, virtual API keys (`0003_api_keys.sql`, `--create-key`), fail-closed
`Plan()`, graceful drain — all live-verified (401→200→SSE→429; docker stop mid-chat completed
the chat). Track C: `console/` package + embedded Tower UI (overview, traces, evals, workflows)
verified page by page in the browser, including the Postgres-down error path. Findings on
the way: (1) console re-render race ate a click and wiped input — fixed; (2) two pre-v9
workflows sat at `status=running` with all steps done (they predate `SetWorkflowStatus`);
Resume on one of them re-ran the id, skipped all three done steps and marked the workflow
done in under a second — the durable-resume path self-heals stale rows. (3) The eval run
started from the console during the browser checks **failed at pair 38 after 30 min**
(`context deadline exceeded`): `scoreOnce` had a hard-coded 1800 s budget and the judge was
swapping against the 3B chat model on the 8 GB GPU for every request I sent meanwhile. No
row was written (atomic), the console showed `failed` + the error. Fix: `EVAL_TIMEOUT` env
(default 90 min) and the contention rule in `docs/VRAM.md`; Track E should add a "pause
routing during eval" or a smaller judge for shared boxes. 57 tests.
**2026-09-15 (9th pass, "run a loop making all of this").**
1. **Repo.** `git init -b main`, `.gitignore`, `.gitattributes` (LF), `README.md` (pitch,
   status table, quickstart, flags, layout, decisions), `LICENSE` Apache-2.0 (fetched via
   `gh api licenses/apache-2.0`), `PRIVACY.md`. Pushed to `github.com/yosrikhiari/AgentOps`.
   `tower-design-system.html` moved to `docs/`.
2. **k6 gate closed live.** `winget install GrafanaLabs.k6` (v2.2.0). `k6 run
   load-tests/router.js` against the router on the real 3B: overhead scenario 2725 req, 0
   failed, p99 70.47 ms (gate <100), median 1.47 ms, max 3.4 s (a 4.7 GB download + a chat
   shared the box); chat scenario 2/2 = 200. 281 dropped iterations = the 10-VU pool could
   not hold 50 RPS through the 3 s stall — the constant-arrival executor reports that
   honestly instead of slowing down.
3. **Monitoring as code.** `docker-compose.yml` gained a `monitoring` profile: Prometheus
   v2.53 (scrape `host.docker.internal:8080` every 5 s) + Grafana 11.1 with
   `dashboard/grafana/provisioning/{datasources,dashboards}` and `router.json` mounted.
   Two provisioning defects in the dashboard JSON: `${DS_PROMETHEUS}` placeholders are only
   resolved by manual import → fixed datasource `uid: prometheus`; targets lacked `refId` so
   panels rendered empty → added; `gridPos` added; faithfulness panel → `stat` with a 0.7
   threshold colour. Verified in the browser: both model series in every legend, stat 0.969.
4. **Corpus v2.** 11 docs rewritten to describe shipped behaviour (5 metrics/5 panels, 5 MCP
   tools + error codes, pg-native tracker + stored-input resume, chained router spans +
   redaction + 404, vector-only search, temperature-0 judges, env defaults, full-sentence
   golden rule). `clean_docs.py`: keeps `_`/`-` inside identifiers, strips list bullets
   only, rebuilds the output dir (it used to append). `evals.Ingest` now deletes chunks the
   cleaner no longer emits (`TestIngestPrunesStaleChunks`) — without it, v1 text kept
   answering v2 questions. Live: 15 docs → 25 chunks; `--search "router_errors_total"` hits
   `04-metrics` first.
5. **Golden v2.** `--draft-golden`/`--freeze-golden`/`--score` now take `--golden-version`
   for file names (v1 re-freeze reproduces `29dd81c5…`). Drafted 48 pairs from the 25 chunks
   with `qwen3:8b`; reviewed all 48 against their docs: 39 fragment answers rewritten as
   full sentences, 1 garbled question fixed, 1 duplicate-listing answer fixed. First score
   (`eval_run 5`): 0.979 — the one miss was *"Because the scorer splits…"*: a subordinate
   clause has no main-clause proposition for the judge. Rewrote the three "Because…"
   answers as main clauses, re-froze (`6ca78573…`), re-scored (`eval_run 6`): **1.000**,
   recall 1.000, precision 0.308. `ValidateGolden` now also rejects answers opening with
   because/since/so that/to/in order to (test case added); v1 predates that rule and is
   rejected at line 14 on re-freeze by design — its frozen hash stands as history.
   Defaults for `--golden-version` and `--drift-golden` moved to `v2`.
6. **Model pull.** `qwen2.5:3b-instruct` (1.9 GB) pulled; `qwen2.5:7b-instruct-q4_K_M`
   died twice on `wsarecv: connection forcibly closed` at ~3 MB/s (Ollama resumes blobs);
   retry loop running. Until it lands, quality-tier chats return the honest 502.
7. **Not done:** the MCP recording (human), and the shadow-test auto-promotion stretch
   feature — it needs both models resident, ≥100 scored paired requests and a real
   significance gate; that is a separate ticket, not a loop iteration.
`go build/vet` clean, `go test ./... -count=1` green (39 tests).
**2026-09-15 (8th pass, golden v1 review → freeze → full score).** Read all 32 draft pairs
next to their source doc. Every answer was supported by its doc, but:
1. HIGH scorer/golden trap — `SplitClaims` keeps only fragments ≥10 runes, so the five
   one-word answers (`Three`, `8GB`, `20 to 30`, `HTTP 429`, `Postgres`) yield **zero
   claims and faithfulness 0.00** no matter how correct they are; the judge also never sees
   the question, so a bare "Three" is not a checkable claim anyway. Fix on both sides: the
   9 short/garbled answers rewritten as full standalone sentences (pair 20 also had "SDK …
   *and* a library" garble corrected), and validation moved from `main.go` into
   `evals.ValidateGolden` (+ zero-claim check) with `TestValidateGolden` — `--freeze-golden`
   now rejects the old draft at line 7 with `answer "Three" yields no scorable claim`.
2. NOTE corpus vs code — several corpus docs describe the *planned* design, not what shipped:
   `09-tracker` (DBOS SDK; shipped pg-native, ADR-0003), `05-mcp`/`04-metrics` (3 tools/3
   panels; shipped 5/5), `07-faithfulness` (results cached by prompt hash; not built),
   `12-pgvector` (BM25 + RRF hybrid; vector-only shipped), `13-vram` (7B judge; `qwen3:8b`
   on this box), `14-monorepo` (fails loudly on missing env; defaults). Faithfulness is
   measured against the *docs*, so v1 stays valid; parked as **corpus v2** in §7 — rewrite
   the docs to match the code, re-chunk, re-draft, bump golden to v2.
3. LOW `scripts/clean_docs.py` `normalize` strips `_` and `-`, so identifiers like
   `router_requests_total` and `nomic-embed-text` become `router requests total` in every
   chunk (and therefore in the golden answers). Consistent, so scores are unaffected, but it
   hurts retrieval on exact identifiers. Parked with corpus v2.
Frozen: `v1.jsonl` 32 pairs, sha256 `29dd81c50ef88b043262feaf4f123e6d77cb945997ba9e708801ac40abaa1cfe`.
Full run 1 (`eval_run id=3`, judge `qwen3:8b`, `faithfulness-v1`): **faith=0.969** (31/32 at
1.00; pair 31 at 0.00), precision=0.206, **recall=1.031** → two more defects:
4. MEDIUM `evals.PrecisionRecall` — recall counted retrieved *chunks* from relevant docs, so
   `08-judges` (the one doc that split into two chunks, both retrieved) scored recall 2.0.
   Recall now counts unique relevant docs found; precision keeps counting chunks (both
   chunks are useful context). `TestRecallCountsUniqueDocs`, bind-checked.
5. MEDIUM `evals.NewOllamaJudge` sent no `options`, so the local judge sampled at Ollama's
   default temperature while the Groq judge already used 0. Pair 31 flipped REFUTED→SUPPORTED
   when re-asked. Now `temperature: 0` on every local judge call;
   `TestOllamaJudgeSendsTemperatureZero`, bind-checked.
Full run 2 after both fixes (`eval_run id=4`): **faith=0.969**, precision=0.206,
**recall=1.000**, `alert=false runs=2 delta=0.000`; `--drift` shows `judge_changed:false`
and names the worst case. This time pair 3 failed and pair 31 passed — both are bare
noun-phrase answers with no verb ("Qwen2.5 3B Instruct and Qwen2.5 7B Instruct…",
"TensorZero, LiteLLM, DeepEval, and Helicone"). A fragment is not a proposition, so the
judge has to infer the claim and does not do so stably even at temperature 0. Lesson for
v2: **every golden answer is a full declarative sentence** (subject + verb); the 8th-pass
rewrite fixed the one-word cases but left these two list-fragments. v1 stays frozen and
honest at 0.969 with the cause recorded; the rewrite goes into corpus v2 (§7).
**2026-09-15 (7th pass, lessons → one more fix).** Wrote `lessons/` (30 HTML lessons +
index + stylesheet, generated from one script so navigation and template stay consistent),
reading every package end-to-end again to ground each lesson in real code. Findings:
1. MEDIUM `router/metrics.go` — histogram double-cumulated: `Observe` already increments every
   bucket ≥ latency (cumulative by construction), then `Expose` ran a second cumulative sum
   over those values. One 10 ms request emitted `le="0.05"} 1 … le="120"} 11, +Inf} 1` —
   monotonic-looking but wrong, and `+Inf < le="120"` breaks `histogram_quantile`. v9's
   `TestMetricsRecordedPerRequest` only asserted the quoted label, not the values. Fixed
   (print `st.buckets[b]` directly); `TestHistogramBucketsAreCumulativeOnce` asserts every
   bucket, `+Inf` and `_count` all equal 3 after three ~0 s observations; bind-checked (test
   fails with the sum reintroduced).
2. DOC `mcp/README.md` listed 3 tools; server exposes 5 — README updated.
3. DOC plan §3 claimed `config.go` "fails loudly on missing env"; every value has a default —
   corrected.
4. DOC plan target "p99 overhead <5 ms" vs `load-tests/router.js` gate `p(99)<100` ms — both
   are intentional (target vs pass/fail gate); §8 #2 now says so.
5. DOC §2.3 still read as if DBOS "changes the recommendation"; §7 ADR stub list still said
   `0003-dbos-go-sdk` — both aligned with ADR-0003.
After: `go build/vet` clean, `go test ./... -count=1` green (35 tests).
**2026-09-14 (6th pass, deep-review → fix → re-verify).** Findings and what changed:
1. CRITICAL `router/metrics.go` — histogram `le` label unquoted → whole scrape invalid.
   Fixed; test asserts quoted form and that `le=0.05` never leaks.
2. HIGH `main.go` span sink — opened a fresh Postgres connection per span, synchronously,
   3× per chat, 2s timeout each (DB down = +6s per chat). Now one `pgxpool` shared by
   spans/traces/drift + an ordered single-goroutine `spanWriter` (1024-deep queue, drops
   counted, first failure logged once). Request path cost is a channel send.
3. HIGH `router/server.go` — `ServeHTTP` built a new `ServeMux` per request. Built once in
   `NewServer`.
4. HIGH `evals/judge.go` — `WithRetry` retried 4xx (bad key/model) against the plan's
   "never retry 4xx". Added typed `StatusError` + `Permanent()`; 429 keeps retrying and now
   honours `Retry-After`. `TestWithRetryNeverRetries4xx` covers 401/503/429.
5. MEDIUM `tracker` — resume fed the *resuming process's* `--tracker-input` to a not-yet-done
   Researcher instead of the workflow's original input. `EnsureWorkflow` now returns the
   stored input and `RunToy` prefers it. `TestResumeUsesStoredInput`, bind-checked; live:
   resume with `"SOMETHING ELSE"` left `workflows.input` untouched.
6. MEDIUM `evals/drift.go` — run header + pair rows weren't one transaction. `scoreOnce`
   wraps `RecordRun` in `pgx.Tx`.
7. MEDIUM drift report hid judge identity — added `judge_then/judge_now/judge_changed` so a
   judge swap isn't read as corpus drift. Test extended; live report shows the fields.
8. MEDIUM `max_tokens` accepted and ignored — now forwarded as Ollama `options.num_predict`;
   `Generator` signature gained `maxTokens`; `TestMaxTokensForwarded`.
9. LOW `go.mod` had `pgx` as `// indirect` — `go mod tidy` (adds `puddle` for the pool).
10. LOW `parseVerdict` read "NOT SUPPORTED"/"UNSUPPORTED" as supported — fixed + tests.
11. LOW span attrs by string concat — `json.Marshal`.
12. LOW blank line in a golden `.jsonl` aborted `--score`/`--freeze-golden` — skipped;
    freeze also rejects duplicate questions (they'd collide on `eval_pair_scores` PK).
13. LOW failed chats invisible — `router_errors_total{model}` + `ObserveError`, 5th Grafana
    panel (error rate), `TestOllamaDownCountsError`.
14. LOW MCP swallowed malformed lines — replies JSON-RPC `-32700`; live-verified.
15. Parked: concurrent-resume lease (see §7).
Also: four copy-pasted pgx adapter structs in `main.go` collapsed into one `pgAdapter`
(+ a 6-line `trackerAdapter` because `tracker.Rows` ≠ `evals.Rows`). After all of it:
`go build/vet` clean, `go test ./... -count=1` green (34 tests), live chat → 3 spans via the
pool worker, `/metrics` shows quoted `le` + `router_errors_total`, tracker run + resume OK.
**2026-09-14 (5th pass, live):** applied `0002_drift.sql` to the running `agentops-postgres`;
ran tracker happy path (47s) + hard kill mid-Drafter + resume (38s); chatted twice through
the HTTP router and read both traces back; ran `--score` twice on a 6-pair draft subset
(good → faith=1.00, corrupted answers → faith=0.00, `alert=true delta=-1.000`, 3 worst cases);
`GET /v1/drift/report` + `eval_faithfulness` gauge on restart + MCP `tools/list` all live.
Defects found by the live runs and fixed the same day: (a) `main.go` did not compile
(missing `fmt` import after the scheduler edit — two sessions fixed it at once, duplicate
removed); (b) `RunToy` never marked `workflows.status=done` (a leftover no-op `ListSteps`
call) — added `Store.SetWorkflowStatus`, asserted in `TestHappyPath`/`TestKillResume`,
bind-checked; (c) unknown trace ids returned `200 {"spans":null}` — now `404 {error:{code:
"trace_not_found",message,trace_id}}` over HTTP and JSON-RPC `-32004` over MCP
(`tracker.ErrTraceNotFound`, test added). `go build/vet/test ./...` green after all three.
Verified 2026-09-14 (4th pass) from disk: `router/`, `mcp/` (5 tools), `evals/`
(+`drift.go`), `migrations/0001_init.sql` + `0002_drift.sql`, `tracker/`,
`docs/VRAM.md` (new — swap rule), `dashboard/grafana/router.json` (4 panels),
`load-tests/router.js`. `go vet` + `go build` + `go test ./...` green.
- [x] `#1 router-skeleton` — DONE 2026-09-13. `main.go`, `config.go`, `router/` stdlib-only,
      6/6 tests green, `go vet` clean. Box already has `nomic-embed-text` (plan default);
      Phase 1 chat pair Qwen2.5 3B/7B still to pull (`qwen3:8b` present as fallback).
- [x] `#2 router-metrics` — DONE 2026-09-13 (code). Hand-rolled stdlib Prometheus
      exposition (`router_requests_total`, `router_tokens_total`, `router_latency_seconds`
      histogram) wired into every chat request + `GET /metrics`; Ollama token counts
      (`prompt_eval_count` + `eval_count`) plumbed through; `dashboard/grafana/router.json`
      (RPS / p99 / tokens); `load-tests/router.js` (50-RPS `/health` overhead + chat smoke).
      7/7 tests green. Live run pending: pull Qwen2.5 pair + `k6 run` + Grafana import.
- [x] `#3 mcp-min` — DONE 2026-09-13 (code). JSON-RPC stdio server in `/mcp`
      (`initialize`/`tools/list`/`tools/call`: `list_models`, `get_stats`,
      `route_test_request`), one binary two modes (`--mcp` flag), router `Chat` shared
      path so stats stay honest, `mcp/README.md` with Claude Desktop config. Live stdio
      handshake verified; MCP tests green. Recording with real Ollama pending model pull.
- [x] `#4 corpus-min` — DONE 2026-09-13. 15 self-written AgentOps docs →
      `scripts/clean_docs.py` (900/120 chunks, SHA dedup, idempotent: 16 chunks) →
      Postgres pgvector (`docker-compose.yml`, `migrations/0001_init.sql`: docs, chunks
      HNSW `m=16,ef_construction=128`, workflows, steps, eval_runs, spans) →
      `--migrate`/`--ingest` via pgx + `nomic-embed-text`; `--search` recall smoke
      returns the right docs. All-package tests green.
- [x] `#5 golden-v1` — DONE 2026-09-15. 32 pairs drafted (`v1_draft.jsonl`, local
      `qwen3:8b`, 2/chunk) → all 32 reviewed against their source doc (agent first pass, your
      approval) → 9 answers rewritten as full sentences, 1 question reworded → `v1.jsonl`
      frozen, `v1.sha256 = 29dd81c50ef88b04…abaa1cfe`. Review found a scorer/golden trap
      (one-word answers score 0 — see 8th pass); `--freeze-golden` now rejects them.
- [x] `#6 scorer-v1` — CODE DONE 2026-09-13. Claim-split faithfulness scorer
      (justification-before-score, `faithfulness-v1`), retrieval P/R, retry/backoff,
      local + Groq judges. Live 2-pair smoke: faith=1.00. Full run waits for your v1.
- [x] `#7 tracker-min` — DONE 2026-09-14 (code + live kill-resume, see 5th-pass log). `tracker/tracker.go`
      (`Store` + `SQLStore` + `MemStore`, `RunToy` Researcher→Drafter→Reviewer, spans per step,
      idempotent resume skips `done`); `tracker/tracker_test.go` (`TestHappyPath`,
      `TestKillResume` — researcher runs once across kill+resume) green; `go build/vet` clean;
      CLI `go run . --run-tracker` / `--resume-tracker <id>` wired (researcher=pgvector top-3,
      drafter=Ollama fast model, reviewer=`approved:` gate). Live run pending: `--migrate` +
      `--run-tracker` against local Postgres, then `kill -9` mid-Drafter + `--resume-tracker`.
      Decision v4 (ADR-0003): Postgres-native stdlib+pgx, no new DBOS dep for MVP (keeps
      `go.mod` stdlib+pgx-only, same `workflows/steps/spans` tables, kill-resume via row
      status). DBOS Go SDK stays a studied alternative, not a dependency.
- [x] `#8 trace-min` — DONE 2026-09-14 (code + live HTTP/CLI/MCP reads, 404 shape fixed). Router `Chat` emits 3 spans
      (`route.decide→model.generate→router.respond`, shared `trace_id`, OTel
      `trace/span/parent` shape, `TestChatEmitsThreeSpans` green); tracker `RunToy` already
      emits 3 spans/workflow (`trace_id=workflow_id`, `TestListSpansRedacted` green);
      query path `GET /v1/traces/{id}` + `go run . --trace <id>` + MCP `inspect_trace`
      (prompts redacted via `RedactAttrs`, same `{error:{code,message}}` shape). Live run
      pending: chat once + `--trace <trace_id>` shows ≥3 spans; tracker run + `--trace
      <workflow_id>` shows 3 steps.
- [x] `#9 drift-min` — DONE 2026-09-14 (code + live 2-run alert on `v1_draft`; full `v1` run waits on #5). `--score` now persists
      (`eval_runs` + `eval_pair_scores` via `0002_drift.sql`, `TestRecordAndDriftReport`
      green) and logs `alert=runs/delta` vs `EVAL_THRESHOLD` (default 0.7); `eval_faithfulness`
      gauge in `/metrics` (`TestEvalFaithfulnessGauge` green, 4th Grafana panel) loaded at
      server startup from latest run; `GET /v1/drift/report` + `go run . --drift` + MCP
      `get_drift_report` (`TestDriftReport` green, 5 tools) return
      `{score_then,score_now,delta,alert,worst_cases[]}`. Live run pending: `--migrate` +
      two `--score` runs (2nd after a bad corpus edit) → 2nd log shows `alert=true`, report
      shows negative delta. → Ran 2026-09-14: `eval_run id=1 faith=1.000 alert=false`, `id=2
      faith=0.000 alert=true delta=-1.000`; report lists 3 worst cases. Note the runs are
      tagged `golden_version=v1_draft` so the future `v1` history starts clean.
- [x] `#10 schedule-min` — DONE 2026-09-14. `--schedule-evals 24h` loops the exact `--score`
      path (`scoreOnce` shared, golden file re-read each tick so a frozen v1 is picked up,
      per-tick failure logged loudly + scheduler survives). ADR-0006: a CLI flag, not a
      service — same binary runs under any timer/cron, zero new deps. Nightly cron parked
      until v1 is frozen. `docs/VRAM.md` written (swap rule from Locked Decisions).

**Your remaining tasks (nothing else is blocking):**
1. ~~Golden review~~ — done (v1 0.969, v2 1.000, v3 0.972 with misses separated).
2. ~~k6 + Grafana~~ — done live; grab the PNG from `localhost:3000/d/agentops-router` if you want it in the README.
3. Record the 2-minute MCP call with any client (`mcp/README.md` — no longer Claude-only; `POST /mcp` works for remote agents).
4. If the 7B pull is still failing: `ollama pull qwen2.5:7b-instruct-q4_K_M` on a better connection, then the short/long curl pair lands on two models.
5. (v22) Commit this batch: `0004_retrieval_miss.sql` + `0005_conversations.sql` + `events.go` + cockpit + ADR-0010 (proposed) + this plan update; then start Track J.
6. (v23) Corpus v4 + golden v4: rewrite `corpus/04-metrics.md` (Tower dashboard, no Grafana) + `corpus/14-monorepo.md` (no `/dashboard` package), re-clean, re-ingest, draft → review → freeze v4, score a full run on a quiet GPU. Frozen v1–v3 and `lessons/11-*` stay as history.
7. ~~(v24) Track J~~ — done live (see 18th pass). ~~Track K~~ — done live the
same day (`advisor/`, `--advise`, veto list, ADR-0012). Next: Track L (local
benchmarking — needs two quiet-GPU scoring runs over frozen v3).
Original list kept for reference:
1. `ollama pull qwen2.5:3b-instruct` and `ollama pull qwen2.5:7b-instruct-q4_K_M` (~2 GB +
   ~4.7 GB), then `go run .` with defaults and re-run the short/long curl pair — the two
   `reason` values should now land on two different `model` values.
3. Install k6 (`winget install k6`), run Grafana (`docker run -d -p 3000:3000 grafana/grafana`),
   import `dashboard/grafana/router.json`, `k6 run load-tests/router.js`, screenshot; then
   record the 2-minute Claude Desktop MCP call using `mcp/README.md`.

### #1 router-skeleton (2d) — Phase 1
Brief: Go monorepo `agentops`, single `go.mod`. Package `/router` calls Ollama
(`http://localhost:11434`) for `qwen2.5:3b-instruct`. No DB, no auth yet.
- [ ] `go mod init agentops`, dirs `/router /tracker /evals /dashboard /mcp`, `config.go` reads `OLLAMA_URL` → Verify: `go build ./...` passes
- [ ] `POST /v1/chat/completions` forwards `{prompt, max_tokens}` to Ollama, returns `{text, model, reason}` with heuristic reason (`len<200 → 3B else 7B-stub`) → Verify: `curl localhost:8080/v1/chat/completions -d '{"prompt":"hi"}'` returns 200 + `reason`
- [ ] Unit test: short prompt → 3B, long prompt → 7B-stub, Ollama-down → `503 {error,trace_id}` → Verify: `go test ./router/...` green
Done: curl demo recorded, reason logged per request.

### #2 router-metrics (2d) — Phase 1
Brief: same repo. Add Prometheus counters + Grafana JSON + k6 smoke. Needs #1.
- [ ] Counters `router_requests_total{model}`, `router_latency_seconds`, `router_tokens_total` on `/metrics` → Verify: `curl localhost:8080/metrics` shows them
- [ ] `dashboard/grafana/router.json`: 3 panels (RPS by model, p99 latency, tokens) → Verify: import into Grafana, panels populate on traffic
- [ ] `load-tests/router.js` (k6, 50 RPS, 1 min), assert p99 overhead <5ms vs direct Ollama → Verify: `k6 run` passes
      (script gate is `p(99)<100` ms so a cold laptop doesn't fail CI; the <5 ms is the target to read off the report)
Done: screenshot of Grafana after k6 run in PR.

### #3 mcp-min (3d) — Phase 2 — DONE 2026-09-13 (code, live handshake verified)
Brief: JSON-RPC stdio MCP server in `/mcp` (stdlib only, MCP spec
`initialize`/`tools/list`/`tools/call`). One binary, two modes (`--mcp` flag). Reuses the
router `Chat` path so stats stay honest.
- [ ] Tools `list_models`, `get_stats` (reads Prometheus counters), `route_test_request` → Verify: MCP inspector lists 3 tools
- [ ] Claude Desktop config snippet in `mcp/README.md` → Verify: ask "which model handled the most traffic today", 2-min recording shows real answer
Done: recording linked in issue.

### #4 corpus-min (3d) — Phase 3 setup — DONE 2026-09-13 (16 chunks, recall verified)
Brief: 15 self-written AgentOps docs + `scripts/clean_docs.py` + pgvector HNSW upsert.
Env confirmed: Docker 29.4 live, Python 3.12, Go network OK, `nomic-embed-text` on box.
Skills: `data-ai:rag-engineer` (chunking), `database:postgresql` (schema/HNSW).
Brief: 15 self-written `.md` docs + minimal cleaner + pgvector HNSW upsert. Independent of #1-3.
- [ ] Write 15 short factual docs (`corpus/*.md`, any topic you know) → Verify: `ls corpus/*.md | wc -l` = 15
- [ ] `scripts/clean_docs.py`: strip HTML/nav → normalize → chunk 800-1000 chars/100-150 overlap → dedup by SHA → `evals/corpus/clean/*.jsonl {doc_id,hash,text,source}` → Verify: rerun is idempotent, `dedup_dropped` logged
- [ ] Postgres + pgvector: `migrations/0001_init.sql` (docs table + `embedding vector(768)` + HNSW `m=16,ef_construction=128`), Go upsert via `nomic-embed-text` (Ollama) → Verify: 15 docs → 40-80 chunks queryable with `recall@5` smoke test
Done: `SELECT count(*) FROM chunks` > 40.

### #5 golden-v1 (3d) — Phase 3 — DONE 2026-09-15 (reviewed, frozen)
Draft: 32 pairs in `evals/golden/v1_draft.jsonl` via local `qwen3:8b` (2/chunk, quality
spot-checked GOOD). Your job: read all 32, fix/delete any wrong pair, save the survivors
as `evals/golden/v1.jsonl` (same one-object-per-line shape). Then tell me and I run
`go run . --freeze-golden` (schema + doc-id validation, writes `v1.sha256`).
Brief: 20-30 QA pairs from clean corpus. Needs #4.
- [ ] LLM drafts pairs from `clean/*.jsonl` (Groq `llama-3.1-8b-instant`, free) → `evals/golden/v1_draft.jsonl` → Verify: 20-30 rows present
- [ ] Hand-fix 100%: every answer checked against source text, bad rows deleted/fixed, hash frozen → `evals/golden/v1.jsonl` → Verify: `sha256sum v1.jsonl` recorded, zero unreviewed rows
Done: v1 hash posted in issue; any doc change bumps to v2 + rerun.

### #6 scorer-v1 (5d) — Phase 3 — CODE DONE, full run pending your v1 freeze
Live-verified on 2 draft pairs: `faith=1.00 precision=0.20 recall=1.00` (honest numbers —
single-doc goldens cap precision@5). `--score` runs the whole file; Groq cross-check
wired behind `GROQ_API_KEY`.
Skills: `ai-ml:advanced-evaluation` (justification-before-score, bias guards),
`backend` (retry/backoff). Judge: local `qwen3:8b` default (`JUDGE_MODEL` env;
Qwen2.5 7B-Q4 when pulled), Groq `llama-3.1-8b-instant` cross-check client wired
behind `GROQ_API_KEY`. Runs against golden file (`--golden`, draft doubles as
dev fixture until you freeze v1).
Brief: faithfulness + retrieval P/R scorer with bias guards. Needs #4-5.
- [ ] `evals/faithfulness.go`: claim-split answer, judge each claim vs retrieved chunks (local Qwen 7B-Q4 everyday), output `{score, evidence[], judge_model, prompt_version}` with justification-before-score → Verify: golden subset scores >0.7 on known-good answers
- [ ] Retrieval `precision@k/recall@k` from golden relevance → Verify: numbers logged per run
- [ ] Scheduler: timer + on-demand, retry/backoff (never retry 4xx), retry-on-429 for Groq `llama-3.1-8b-instant` cross-check (10% sample) + `llama-3.3-70b` weekly spot (small N, 1k/day cap), `$0` cap enforced → Verify: kill network mid-run → suite retries then fails loudly with `trace_id`
Done: `go test ./evals/...` + one scheduled run writing to Prometheus.

### #7 tracker-min (5d) — Phase 4 — DONE 2026-09-14 (cold-start brief kept for reference)
Brief: Postgres-native Researcher→Drafter→Reviewer toy on existing `workflows/steps/spans`
tables. Needs Postgres from #4 only. No new deps (stdlib + pgx, same as router/evals).
ADR-0003 v4: DBOS Go SDK studied, not vendored for MVP — same durability shape via row
status (`pending→running→done`), idempotency keys (`workflow_id,seq`), resume = re-run same
workflow ID skips `done` steps. Keeps `go.mod` stdlib+pgx-only and independence story clean.
- [ ] `tracker/tracker.go`: `Store` iface (`EnsureWorkflow/ListSteps/GetStep/SetRunning/SetDone/EmitSpan`) + `SQLStore` (pgx Execer/Queryer) + `MemStore` (maps, for unit test) → Verify: `go build ./...` passes
- [ ] Toy agent: Researcher (retrieve fn) → Drafter (Ollama 3B fn) → Reviewer (judge fn); each step a span (`trace_id=workflow_id`) → Verify: happy-path run completes, 3 steps `done` + ≥3 spans
- [ ] Kill test: `kill -9` mid-Drafter == failing Drafter fn once, restart same workflow ID, assert resume (no duplicate Researcher side effect, attempts counted) → Verify: automated test `TestKillResume` green
Done: kill-resume log posted in issue. CLI: `go run . --run-tracker` (new ID) / `go run . --resume-tracker <id>`.
Live log 2026-09-14 (`FAST_MODEL=qwen3:8b`): started `f7850f82f56fe6b9`; at t=1s
`researcher=done drafter=running reviewer=pending`; `taskkill /F` the Go process; DB unchanged
(`drafter running attempts=1`); `--resume-tracker f7850f82f56fe6b9` → 38s →
`researcher done/1, drafter done/2, reviewer done/1`, `workflows.status=done`, 3 spans.

### #8 trace-min (4d) — Phase 5 — DONE 2026-09-14 (code + live)
Brief: spans live in `0001_init.sql` already (ADR-0004: no `0002_spans.sql` — table +
`spans_trace_id`/`spans_started_at` indexes exist). Router + tracker emit, HTTP + MCP read.
Needs #1, #6-7 (code meets this; live DB pending).
- [x] Router `Chat` emits 3 spans (`route.decide→model.generate→router.respond`, shared
  `trace_id`) via optional `SpanSink` (nil in tests, best-effort pg insert in prod, never fails
  the request) → Verify: `TestChatEmitsThreeSpans` green (OTel ids + parent chain)
- [x] Tracker `RunToy` emits 1 span/step (`trace_id=workflow_id`) + `ListSpans(trace_id)` with
  `RedactAttrs` (drops `prompt/input/text/output`, keeps `output_snippet`) → Verify:
  `TestListSpansRedacted` green
- [x] `GET /v1/traces/{id}` JSON + `go run . --trace <id>` + MCP `inspect_trace`
  (`TestInspectTrace` green, 5 tools in `tools/list` after drift) → Verify: chat once, `curl
  localhost:8080/v1/traces/<trace_id>` shows ≥3 spans; `inspect_trace` in Claude Desktop
  returns same
Done: trace JSON for one chat + one tracker workflow posted in issue.
Live 2026-09-14: chat `598d8f7a92bbdbc4` → `route.decide(parent=-) → model.generate(parent=
decide) → router.respond(parent=generate)`, attrs carry `reason/tier/model`, prompts redacted;
`--trace f7850f82f56fe6b9` → `researcher/drafter/reviewer` with `output_snippet` only.

### #9 drift-min (2d) — Phase 5 — DONE 2026-09-14 (code + live on `v1_draft` subset)
Brief: on-demand eval + Prometheus gauge + report (nightly cron parked — same `--score`
binary runs on any timer). Needs #6, #8 (code meets this; live DB pending).
ADR-0005: `eval_runs` stays the run header, per-question scores go to `eval_pair_scores`
(`0002_drift.sql`), so the report can name worst cases without re-running judges.
- [x] `--score --golden-version v1` persists run + pair scores (`RecordRun`), logs
  `faithfulness/threshold/alert/runs/delta` vs `EVAL_THRESHOLD` (default 0.7) → Verify:
  `TestRecordAndDriftReport` green (delta math, alert, worst-cases sorted)
- [x] `eval_faithfulness` gauge in `/metrics` (startup load from latest run, best-effort) +
  4th Grafana panel + `GET /v1/drift/report` + `go run . --drift` + MCP `get_drift_report`
  (5 tools in `tools/list`, `TestDriftReport` + `TestEvalFaithfulnessGauge` green) → Verify:
  force a bad corpus change → 2nd `--score` logs `alert=true`, `curl
  localhost:8080/v1/drift/report` shows negative delta + 3 worst cases
Done: six MVP checkboxes closable after live runs + your `#5 golden-v1` review.
Live 2026-09-14: `--score --golden <6 good pairs> --golden-version v1_draft` → faith=1.000;
same 6 with answers replaced by false claims → faith=0.000, log `alert=true runs=2
delta=-1.000`; `--drift --drift-golden v1_draft` and `GET /v1/drift/report` (server started
with `--drift-golden v1_draft`) return `{score_then:1, score_now:0, delta:-1, alert:true,
worst_cases:[3]}`; `eval_faithfulness 0.000000` present in `/metrics` after restart.

---

*Update this as you build. The six MVP checkboxes are the contract with yourself — everything
else is allowed to change.*

---

## 9. v1.0 — beyond the MVP (started 2026-09-15)

The MVP proved the four tools work. v1.0 turns them into something a person can install and
use: the **Tower** console from `docs/tower-design-system.html`, on top of a gateway that
real clients can talk to. Scope agreed 2026-09-15: tracks **A, B, C** now; D (tracker v2),
E (evals v2), F (shadow-test promotion) stay in §7 until these ship. One track at a time,
each shipped as tested commits on `main`, check-in between tracks.

### Track A — ops foundation (½ day) — DONE 2026-09-15
Brief: make every later change safe to ship. No product behaviour changes.
- [x] `Dockerfile` (multi-stage, static binary, distroless nonroot, 21.7 MB) + `.dockerignore`; `docker compose --profile app` runs the router against the host's Ollama → Verified: image builds, `--version` runs, containerised chat + `/v1/drift/report` answered live
- [x] `.github/workflows/ci.yml`: gofmt, `go vet`, `go test -race`, `go build`, docker build on push/PR; on `v*` tags builds linux/windows/darwin binaries + `SHA256SUMS` and creates a release → Verified: run 34944183047 green (go ✓ docker ✓ release skipped)
- [x] `schema_migrations(filename, applied_at)`: `Migrate` records + skips applied files, returns the count → Verified: `TestMigrateSkipsApplied`; live `--migrate` → "applied 2", then "applied 0"
- [x] `.github/dependabot.yml` (gomod, docker, github-actions weekly) → Verified: Dependabot opened its first actions PR within a minute
- [x] tag `v0.1.0` → Verified: run 34944393352 release ✓; assets `agentops-v0.1.0-{linux,windows,darwin}-amd64` + `SHA256SUMS`
Done: CI + release badges in README; `--version` flag stamped from the tag. 40 tests.

### Track B — gateway hardening (2–3 days) — DONE 2026-09-15
Brief: the router becomes a gateway an OpenAI-style client can point at.
- [x] OpenAI-compatible `POST /v1/chat/completions` (`messages[]`, `model` = "auto"|name|backend/name, `choices[0].message`, `usage`, `id chatcmpl-…`) with `text/reason/backend/trace_id/fallback` extensions; legacy `{prompt}` kept → `TestOpenAIShape`, `TestExplicitModel`; live: `"Blue"` with usage 24/2/26
- [x] Streaming: `stream:true` → SSE chunks in the OpenAI shape, final chunk carries usage + reason + trace_id, `[DONE]`; error-after-first-delta becomes an error event; metrics count once → `TestStreamSSE`; live `curl -N` shows digit deltas
- [x] `Backend` interface (`Generate/Stream/Health`, `Local()`), `OllamaClient` moved to `/api/chat` with NDJSON streaming, `OpenAIBackend` for Groq/any OpenAI-style API with SSE + `stream_options.include_usage` + Groq `x_groq.usage`; `HealthProber` every 15 s → `router_backend_up{backend}`, `GET /v1/models` with `up`, MCP `list_models` with backends → `TestOllamaChatAndStream`, `TestOpenAIBackendStreamAndAuth`, `TestHealthProberGaugeAndModels`; live gauge = 1
- [x] Virtual API keys: `0003_api_keys.sql`, `--create-key NAME --key-rpm --key-budget` prints the secret once (SHA-256 stored), `Authorization: Bearer ak_…`, 401/403/429 in the one error shape, fixed-window per-key limiter, async usage charge, `router_key_requests_total/rejected_total{key}` → `TestAPIKeyRequired/RateLimit/Budget`; live: 401 without key, 200/429/429/429 on a 2-rpm key, `tokens_used=97` in the table
- [x] Fail-closed: `Plan()` builds the candidate chain once — other local tier as fallback, cloud only when not sensitive; header `X-AgentOps-Sensitive`, body flag or keyword list; explicit cloud model on sensitive data → `403 sensitive_cloud_blocked` → `TestSensitiveNeverLeavesBox` (header + keyword + explicit), `TestFallsBackToCloudWhenLocalFails`, `TestPlanCandidateOrder`; live span shows `sensitive:true` and local-only candidates
- [x] Graceful shutdown (`http.Server.Shutdown` with the request timeout, health prober stopped, span queue drained via `spanWriter.Close`), `ReadHeaderTimeout`, per-request `REQUEST_TIMEOUT` → 504, `X-Trace-ID` + `X-Request-ID` echo → live: `docker stop` 3 s into a 150-token chat, chat returned 200 (171 tokens), log `draining … bye` 48 s later
Done: `docs/API.md` written; README gateway section; 52 tests.
Deferred to Track D/E: nothing. Noted for later: the limiter is per-process (fine for one gateway; a second replica needs a shared counter), and usage is charged asynchronously so a budget can overshoot by one request.

### What's next after v1.0.0
The market study (`docs/MARKET.md` §6) re-orders the backlog: **P0** hybrid retrieval + retrieval-miss reporting, OTLP export, Anthropic API; **P1** shadow-test promotion (= Track F), sovereignty policy engine, spend, opt-in capture, MCP tool governance; **P2** distribution. The original track list stays valid as containers:
Tracks **D** (tracker v2: generic workflows via API, resume lease, `failed` state, root span),
**E** (evals v2: hybrid retrieval, judge disagreement metric, trajectory evals, "pause routing
during eval" for shared GPUs) and **F** (shadow-test auto-promotion) — in that order, each
one a §9 track with tickets and exit criteria before code. Two box-level items stay yours:
the 7B model pull and the MCP recording.
**Shipped since (v22, 2026-09-21):** v1.1 gateway (ADR-0009, committed) — generation
params, client refs, `OLLAMA_MODELS`; Track N (universal MCP + live rail, `#/try`/`#/live`
→ `#/cockpit`) + retrieval-miss reporting (P0 item 1 of 2 — misses now reported
separately via `misses_now/misses_then` + chip; hybrid retrieval itself still open) +
opt-in conversation capture (P1 "opt-in capture" partially — thread shelf only, no
prompt capture on the chat path). Remaining P0: hybrid retrieval, OTLP export,
Anthropic API.

**§10 (v2 enhancement roadmap)** adds three new tracks: **G** (standardized model
benchmarking), **H** (ML-based outage/degradation prediction), **I** (ML-powered drift
detection). These require a Python sidecar for the ML libraries; the Go binary stays
unchanged. See §10 for the full plan, repo survey (41 tools evaluated), build order, and
architecture.

**§11 (model-intelligence pipeline)** adds four Go-only tracks: **J** (startup presence
check — the correctness floor: never advertise an unpulled model), **K** (static advisor),
**L** (local benchmarking), **M** (adaptive routing) plus **M1** (remote judgment
pool: Groq + one second provider for non-sensitive review). Build J before any other
new track; K→L→M in order after. Track L shares its comparison view with G; Track M
is the local, explainable execution of F's promotion rule.

### Track C — Tower console (3–4 days) — DONE 2026-09-15
Brief: the mockup, real. Served by the same binary from `embed.FS` at `/`, hand-written
HTML/CSS/JS on the Tower tokens (no framework, no build step), reading the JSON API.
- [x] `console/` package: `GET /v1/overview` (last-hour traffic from `model.generate` spans: requests, p50/p99, tokens, errors, by-model; models + backends with health; drift; version), `GET /v1/requests?limit` (one row per trace from `route.decide` + `model.generate`), `GET /v1/evals/runs`, `GET/POST /v1/evals/run|status` (single-flight: 202 then 409), `GET/POST /v1/workflows`, `POST /v1/workflows/{id}/resume` → `TestStaticAndOverview`, `TestStoreDownIsOneErrorShape`, `TestEvalRunSingleFlight`, `TestWorkflowActions`, `TestRecentRequestsGroupsSpansPerTrace`
- [x] Overview page: status strip (router live / backend down, models, faithfulness, p50, version), KPI row, routing-traffic SVG bars (height = latency, amber = quality/cloud, red = error, click → trace), backend health, recent requests with `sensitive`/`fallback`/`error` chips → verified in the browser against the live box (6 req/h, p50 19 s, faithfulness 1.000, 17 bars)
- [x] Trace inspector: id input, one step per span with dot/name/meta, expandable k/v attrs, router chats and workflows → verified on chat `07da498d…` (sensitive chip, 3 chained spans) and workflow `615181b4…` (3 steps, `output_snippet` only)
- [x] Evals page: latest/delta/runs/status metrics, score-history SVG with the 0.7 threshold line, worst cases, runs table, **Run eval suite** button → verified: runs 5→6 chart; `POST /v1/evals/run` 202 then 409 live, run 7 started from the console
- [x] Workflows page: start input + button, table with per-step pills (`drafter ×2` shows the recorded kill-resume), Resume on non-done rows → verified: started `615181b4288a13f4` from the UI, 3 steps done in ~90 s, opened in the inspector
- [x] Dashboard hardening (§7 item struck): AbortController per page, one retry on 5xx, `502 store_unavailable` → inline error + Retry, polling refreshes in place and never replaces a focused input → verified: Postgres stopped → error box; chat still 200; Postgres started → Retry → page back
Done: `docs/API.md` console section; README; lessons 31–33 (Part 8 · v1.0). 57 tests.
Browser check found one real bug before shipping: the workflows page re-rendered every 4 s while a workflow was running, so the Start button was replaced under the click and the input was wiped — fixed (form built once, list refreshes in place). Screenshots: not saved as files (pane cannot export); every page verified live, see the §8 10th pass.
Not in this track (deliberate): key management in the UI (CLI only), auth on console endpoints (localhost tool; put a proxy in front), light theme.

---

## 10. What's Next — Making AgentOps Smarter with ML (v2 plan, 2026-09-17)

> **Plain-English version.** The technical details (repo tables, SQL schemas, Prometheus
> metrics, architecture diagrams) are in §10-technical below. Read this section first to
> understand *what* and *why*; read §10-technical when you're ready to *build*.

### 10.0 Where we are and where we're going

**What v1.0 already does:**
- Routes AI requests to the right local model (fast vs quality)
- Tracks multi-step tasks and survives crashes
- Checks if the AI is telling the truth (faithfulness scoring against 72 known answers)
- Shows everything on a dashboard

**What v1.0 can NOT do (the three gaps):**
1. It only checks one thing — faithfulness. It doesn't check for toxicity, hallucination,
   relevance, bias, or how good the model is at coding, reasoning, etc.
2. It waits for things to break. The health check is binary: model is up, or model is down.
   It can't see a crash coming.
3. The drift detector is dumb. It compares the last two scores and says "it went up" or
   "it went down." It can't tell you if that's a real trend or just noise. It doesn't
   watch operational numbers (latency, errors) for drift at all.

**The plan adds three upgrades to fix these gaps:**

### 10.1 Track G — Better Testing: "Which model is actually best?"

**The problem:** Right now you only measure faithfulness (is the AI telling the truth?).
But a model can be truthful and still be toxic, slow, expensive, or bad at coding. You
have no way to compare models side-by-side on multiple dimensions.

**What we're building:**

1. **A benchmark runner** — a small Python program that tests your models on many different
   things: faithfulness (you already have this), plus hallucination (does it make stuff up?),
   relevance (does it answer the actual question?), toxicity (is it saying harmful things?),
   and more. It uses two well-known open-source tools:
   - **DeepEval** (~17k GitHub stars) — like a test suite for AI. You write tests, it scores
     the AI on 50+ dimensions. Works like pytest (if you know Python testing).
   - **lm-evaluation-harness** (~14k stars) — the standard tool that powers the Hugging Face
     leaderboard. Runs 60+ academic benchmarks.

2. **A prompt regression gate** — before you swap to a new model, automatically run a test
   suite. If the new model is worse on any dimension, block the swap. Uses **promptfoo**
   (~22k stars), which also tests for security vulnerabilities (prompt injection, jailbreaks).

3. **A leaderboard page** — a new page on your dashboard that shows all your models in a
   table: model A scores 0.97 on faithfulness, 0.02 on toxicity, 0.85 on relevance; model B
   scores 0.91, 0.01, 0.92. You can sort by any column and see which model is best for what.

4. **Quality-vs-cost analysis** (later) — once you track how much each model costs per token,
   you can answer: "Model B is 5% less accurate but 80% cheaper — is it worth it for simple
   questions?"

**In restaurant terms:** Instead of just checking "did the chef use the right ingredients?",
you're now also checking "does the food taste good?", "is it safe to eat?", "did the chef
answer the customer's actual order?", "how long did it take?", and "how much did the
ingredients cost?"

### 10.2 Track H — Predicting Problems: "Something is about to break"

**The problem:** Right now, the health check pings each model every 15 seconds and reports
UP or DOWN. That's like checking if a car engine is running — it tells you nothing until the
engine already died. You want to see the engine overheating *before* it fails.

**What we're building:**

1. **A metrics pipeline** — a Python program that reads all the numbers your gateway already
   collects (how fast is each model responding? how many errors? how many requests?) and
   organises them into a timeline. Think of it as a spreadsheet where each row is one minute
   and each column is a measurement.

2. **Anomaly detection (simple first)** — rules that flag unusual patterns:
   - "Error rate jumped from 1% to 15% in the last 5 minutes" → warning
   - "Latency has been getting worse every hour for the last 6 hours" → warning
   - "Throughput dropped to zero" → critical
   Uses **adtk** (~1k stars) — a simple, easy-to-understand anomaly detection library.
   No fancy ML yet, just sensible rules.

3. **Forecasting (then smarter)** — a model that learns what "normal" looks like for each
   backend over a week, then predicts what the next hour should look like. If reality
   diverges from the prediction, something is wrong. Uses **Darts** (~9.5k stars) — a
   time-series forecasting library that includes models from simple (Prophet) to advanced
   (N-BEATS neural networks). The key idea: if the model predicts latency should be 200ms
   and it's actually 800ms, that backend is in trouble even though it hasn't crashed yet.

4. **Pre-emptive re-routing** — when the anomaly score for a backend crosses a threshold,
   don't wait for it to crash. Mark it "degraded" (a new state between healthy and down)
   and start sending traffic to other backends first. The degraded backend still works — it's
   just tried last, like moving a struggling chef to backup duty instead of firing them.

5. **Smarter detection** (later) — combine multiple detectors and use post-processing to
   reduce false alarms (the system crying wolf). Uses **PyOD** (~9k stars, 60+ detectors)
   and **Merlion** (~4.5k stars, built by Salesforce to reduce false positives).

**In restaurant terms:** Instead of waiting for a chef to collapse, you watch the signs —
they're sweating more, plating slower, making small mistakes. When enough signs add up, you
quietly move their orders to another chef before any customer notices.

### 10.3 Track I — Smarter Drift Detection: "Is the AI getting worse, and is it real?"

**The problem:** The current drift detector compares two scores and says the number went up
or down. That's like looking at the temperature at noon today vs noon yesterday — it tells
you almost nothing. Is it a trend? Is it random? Which specific questions got worse? Is the
model drifting or is the data changing?

**What we're building:**

1. **Trend analysis** — instead of comparing 2 runs, look at ALL runs over time. Draw a trend
   line. Compute a p-value (a statistical way to say "there's a 95% chance this decline is
   real, not random noise"). Show a confidence band on the chart — a grey zone that says
   "scores in this range are normal; anything outside is suspicious."

2. **Per-question tracking** — track each of your 72 questions individually across every run.
   If question #15 scored 1.0, 1.0, 1.0, 0.0, 0.0 over the last 5 runs, that specific
   question is regressing. Show a table of "problem questions" with mini-charts (sparklines)
   so you can see exactly where things are getting worse.

3. **Operational drift** — watch the gateway's own metrics (latency, error rate, tokens per
   request) for drift, not just the eval scores. If Model A's latency is slowly creeping up
   over days, that's operational drift. Uses **River** (~5.9k stars) — a streaming ML library
   that processes one data point at a time with no memory buildup. It watches the metric
   stream and raises a flag the moment the pattern statistically changes.

4. **Semantic drift** — take a sample of the model's actual outputs, convert them to
   embeddings (number representations of meaning), and compare the distributions over time.
   If the model starts answering in a fundamentally different way — even if the faithfulness
   score hasn't moved yet — the embedding distribution will shift. Uses **Evidently** (~7.6k
   stars) or **Frouros** (~227 stars) for the statistical comparison.

5. **Fix a real bug first** — right now, when the system can't find the right document to
   answer a question (a retrieval miss), it reports it as "unfaithful" (the model lied). But
   the model didn't lie — the search just failed. Separate these two cases so you know
   whether to fix the model or fix the search.

6. **Deterministic tests** — a set of prompts with known exact answers (not judged by another
   AI, just checked by string matching: "the answer must contain X"). Run daily. If a model
   silently changes and breaks these, auto-create a GitHub Issue. Inspired by **model-drift**
   (a project that found 58.8% of AI model+prompt combinations lost accuracy on silent
   provider updates).

7. **Automatic response** (later) — when drift is confirmed and sustained, automatically
   alert you (webhook, email) and optionally pause routing to the drifting model.

**In restaurant terms:** Instead of "today's food was worse than yesterday's", you now know:
"the soup has been getting saltier every day for a week (that's a trend, not a fluke), and
specifically it's the tomato soup (not the chicken), and the chef is also plating slower
(operational drift), and the flavour profile has fundamentally changed (semantic drift), and
by the way last week's low score was because we ran out of tomatoes (retrieval miss), not
because the chef forgot the recipe (unfaithfulness)."

### 10.4 How It All Fits Together

Your main program (the Go binary) stays exactly the same. Nothing changes in the gateway's
speed or behaviour. All the new ML stuff runs in a **separate Python program** (called a
"sidecar") that sits next to the main program:

```
  Your Go program (fast, handles traffic)
       │
       │ sends data to
       ▼
  Python sidecar (slow, does the math offline)
       │
       │ writes results back to
       ▼
  Postgres database + Prometheus metrics + Dashboard
```

- The sidecar runs **only when asked** (on a schedule or when you click a button). It never
  slows down the gateway.
- All its ML models run on **CPU** (not your GPU). Your GPU stays free for the chat models.
- One command starts it: `docker compose --profile ml up`
- It reads from the same Prometheus metrics and database your gateway already writes to.
- It writes results to new database tables and new Prometheus metrics.
- The dashboard gets three new pages: Benchmarks, Anomalies, and an upgraded Drift view.

### 10.5 Build Order — What to Do First

Do these in order. Each one works on its own — you can stop at any point and still have
something useful and demoable.

| # | What | Why first | How long |
|---|---|---|---|
| 1 | ~~Fix the retrieval-miss vs unfaithful bug~~ — DONE 2026-09-21 (`0004`, `ScorePair` miss short-circuit, NULL faith, misses excluded, chip) | It's a bug, not a feature. 1 day. | 1 day |
| 2 | Add trend line + p-value to drift report | Makes the existing drift report 10x more useful with no new dependencies. | 2 days |
| 3 | Track which questions are regressing | Once you have trend data, this is a simple query. | 2 days |
| 4 | Build the metrics pipeline (Python sidecar) | This is the foundation for all ML work. Every anomaly detector and forecaster needs this data. | 2 days |
| 5 | Simple anomaly detection | The first "ML" feature. Easy to understand, easy to demo, immediately useful. | 2 days |
| 6 | Benchmark runner | Start testing models on multiple dimensions. Big demo value. | 3 days |
| 7 | Streaming operational drift | First real-time ML feature. Watches your metrics and flags changes as they happen. | 3 days |
| 8 | Prompt regression gate | Block bad model swaps automatically. Great CI/CD story. | 2 days |
| 9 | Forecasting model | Predict what latency *should* be, flag when reality diverges. | 5 days |
| 10 | Multi-metric eval (toxicity, hallucination, etc.) | Extend your scorer beyond faithfulness. | 3 days |
| 11 | Semantic output drift | Detect meaning-level changes in model outputs. | 3 days |
| 12 | Deterministic regression suite | Catch silent model changes without an AI judge. | 2 days |
| 13 | Pre-emptive re-routing | Route traffic away from struggling backends. | 3 days |
| 14 | Model leaderboard page | Show the comparison table on the dashboard. | 2 days |

**Total: about 30 days part-time.** Items 15–18 (ensemble detectors, transformer models,
cost analysis, automatic responses) are parked for later — only build them after the above
proves itself over a few weeks of real use.

### 10.6 Key Open-Source Tools We'll Use

**For benchmarking (Track G):**
| Tool | What it does | Stars |
|---|---|---|
| [DeepEval](https://github.com/confident-ai/deepeval) | Test suite for AI — 50+ metrics, works like pytest | ~17k |
| [lm-evaluation-harness](https://github.com/EleutherAI/lm-evaluation-harness) | The standard LLM benchmark tool (powers HF leaderboard) | ~14k |
| [promptfoo](https://github.com/promptfoo/promptfoo) | Prompt regression testing + security red-teaming | ~22k |
| [RAGAS](https://github.com/explodinggradients/ragas) | RAG-specific evaluation (retrieval quality + answer quality) | ~14k |

**For outage prediction (Track H):**
| Tool | What it does | Stars |
|---|---|---|
| [Darts](https://github.com/unit8co/darts) | Time-series forecasting + anomaly detection | ~9.5k |
| [PyOD](https://github.com/yzhao062/pyod) | 60+ anomaly detection algorithms, auto-selects the best one | ~9k |
| [adtk](https://github.com/arundo/adtk) | Simple rule-based anomaly detection (good starting point) | ~1k |
| [Merlion](https://github.com/salesforce/Merlion) | Salesforce's time-series ML — great at reducing false alarms | ~4.5k |

**For drift detection (Track I):**
| Tool | What it does | Stars |
|---|---|---|
| [Evidently](https://github.com/evidentlyai/evidently) | 100+ drift metrics, includes LLM-specific ones | ~7.6k |
| [River](https://github.com/online-ml/river) | Real-time streaming drift detection, constant memory | ~5.9k |
| [Frouros](https://github.com/IFCA-Advanced-Computing/frouros) | Clean statistical drift tests (KS, MMD, etc.) | ~227 |
| [whylogs](https://github.com/whylabs/whylogs) | Lightweight data profiling — compact snapshots of your data | ~2.7k |

**For reference (study these, don't install):**
| Tool | What it does | Stars |
|---|---|---|
| [Langfuse](https://github.com/langfuse/langfuse) | Most complete open-source LLM observability platform | ~30k |
| [LiteLLM](https://github.com/BerriAI/litellm) | The most popular AI gateway — study its cost tracking | ~53k |
| [OpenLLMetry](https://github.com/traceloop/openllmetry) | Auto-generates traces for LLM calls (OpenTelemetry) | ~7.4k |

### 10.7 Rules (same as the MVP)

1. **Finish before you add.** Each item above works alone. Don't start #9 before #5 works.
2. **You write the glue, libraries do the math.** Don't write your own anomaly detector from
   scratch. Use PyOD/Darts/River. Write the code that connects them to your system.
3. **The Python sidecar never slows down the gateway.** It runs in the background, on CPU.
   The gateway stays fast.
4. **Watch the licenses.** `alibi-detect` (BSL) and `deepchecks` (AGPL) have restrictive
   licenses. Stick with Apache-2.0 / MIT / BSD libraries.
5. **Don't train ML models on your GPU.** Your GPU is for the chat models. The sidecar's
   models (Prophet, N-BEATS, ADWIN) are small and run fine on CPU.

---

### 10-technical: Technical Reference (schemas, metrics, architecture)

The detail below supports §10. Skip it until you're building.

#### Python sidecar architecture

```
┌──────────────────────────────────────────────────────┐
│  Go binary (gateway + console + tracker + evals)     │
│  ┌─────────┐ ┌──────────┐ ┌────────┐ ┌───────────┐  │
│  │ router/ │ │ tracker/ │ │ evals/ │ │ console/  │  │
│  └────┬────┘ └────┬─────┘ └───┬────┘ └─────┬─────┘  │
│       │           │           │             │        │
│       └───────────┴───────┬───┴─────────────┘        │
│                    spans table + /metrics             │
└───────────────────────────┬──────────────────────────┘
                            │ HTTP / subprocess
┌───────────────────────────▼──────────────────────────┐
│  Python sidecar (ml/)                                │
│  ┌────────────┐ ┌──────────┐ ┌───────────────────┐   │
│  │ benchmark/ │ │ anomaly/ │ │ drift/            │   │
│  │ deepeval   │ │ adtk     │ │ river, evidently  │   │
│  │ lm-eval    │ │ darts    │ │ frouros, whylogs  │   │
│  │ promptfoo  │ │ pyod     │ │                   │   │
│  └────────────┘ └──────────┘ └───────────────────┘   │
│  Flask/FastAPI on localhost:8081 (not exposed)        │
└──────────────────────────────────────────────────────┘
```

- **One `docker compose --profile ml` command** adds the sidecar.
- Go calls the sidecar via HTTP (`POST /benchmark/run`, `POST /anomaly/detect`,
  `POST /drift/check`) or subprocess (`python ml/benchmark/run.py --suite faithfulness`).
- Results always land in Postgres tables + Prometheus gauges.
- The sidecar never touches the gateway's request path — it is offline/batch only.
- VRAM note: the sidecar's ML models (Prophet, N-BEATS) run on CPU. Only `nomic-embed-text`
  (already resident, 137M) uses the GPU. No VRAM contention with the chat models.

#### New Postgres tables

```sql
-- Track G: benchmarking
CREATE TABLE benchmark_runs (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    model       TEXT NOT NULL,
    suite       TEXT NOT NULL,
    scores      JSONB NOT NULL,       -- {faithfulness: 0.97, toxicity: 0.02, ...}
    params      JSONB,                -- {temperature: 0, max_tokens: 512, ...}
    duration_s  REAL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Track H: anomaly detection
CREATE TABLE anomaly_events (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    backend     TEXT NOT NULL,
    detector    TEXT NOT NULL,         -- 'threshold', 'volatility_shift', 'forecast_residual'
    metric      TEXT NOT NULL,         -- 'latency_p99', 'error_rate', 'tokens_per_min'
    score       REAL NOT NULL,
    severity    TEXT NOT NULL,         -- 'info', 'warning', 'critical'
    ts          TIMESTAMPTZ NOT NULL
);

CREATE TABLE forecasts (
    backend     TEXT NOT NULL,
    ts          TIMESTAMPTZ NOT NULL,
    metric      TEXT NOT NULL,
    predicted   REAL NOT NULL,
    actual      REAL,
    residual    REAL,
    PRIMARY KEY (backend, metric, ts)
);

-- Track I: drift detection
CREATE TABLE drift_events (
    id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    metric      TEXT NOT NULL,         -- 'latency_p99', 'error_rate', 'semantic_similarity'
    backend     TEXT,
    detector    TEXT NOT NULL,         -- 'adwin', 'ks_test', 'mmd', 'page_hinkley'
    p_value     REAL,
    window_before JSONB,              -- {mean: 0.12, std: 0.03, n: 100}
    window_after  JSONB,
    ts          TIMESTAMPTZ NOT NULL
);
```

Migration: `migrations/0004_ml_tables.sql`, applied by `--migrate` like the others.

#### New console pages (Tower design system)

All pages follow `docs/tower-design-system.html`. Four states each (skeleton, live, empty,
error+Retry). Colours are `--tower-*` tokens. No framework.

1. **Benchmarks** (`#/benchmarks`) — model × metric matrix (sortable), run history per model,
   "Run benchmark" button (single-flight like evals), score-over-time SVG per metric.
2. **Anomalies** (`#/anomalies`) — timeline of `anomaly_events`, filterable by backend and
   severity. Forecast overlay chart (predicted vs actual latency). Degraded-backend pills
   on the Overview page.
3. **Drift** (extend existing `#/evals`) — trend line + confidence band on the score history
   chart, per-question regression table with sparklines, operational drift indicators per
   backend, semantic drift chart.

#### New Prometheus metrics

```
# Track G
benchmark_score{model,suite,metric}      gauge   — latest score per dimension
benchmark_runs_total{model,suite}         counter — completed benchmark runs

# Track H
router_anomaly_score{backend,metric}      gauge   — current anomaly score (0–1)
router_backend_degraded{backend}          gauge   — 1 when pre-emptively deprioritised
router_forecast_residual{backend,metric}  gauge   — |actual - predicted| / predicted

# Track I
router_drift_detected{backend,metric}     gauge   — 1 when drift confirmed
eval_drift_p_value{golden_version}        gauge   — p-value of score trend test
eval_regressing_questions{golden_version}  gauge   — count of consistently-declining questions
```

#### All repos surveyed (full reference)

**Benchmarking:**

| Repo | Stars | License | Why |
|---|---|---|---|
| [EleutherAI/lm-evaluation-harness](https://github.com/EleutherAI/lm-evaluation-harness) | ~14k | MIT | Standard few-shot LLM eval; 60+ tasks; HF leaderboard backend |
| [confident-ai/deepeval](https://github.com/confident-ai/deepeval) | ~17k | Apache-2.0 | pytest-style LLM eval with 50+ metrics |
| [promptfoo/promptfoo](https://github.com/promptfoo/promptfoo) | ~22k | MIT | Prompt regression testing + red-teaming |
| [explodinggradients/ragas](https://github.com/explodinggradients/ragas) | ~14k | Apache-2.0 | RAG-specific evaluation without ground truth |
| [stanford-crfm/helm](https://github.com/stanford-crfm/helm) | ~4k | Apache-2.0 | Multi-dimensional scoring (accuracy, fairness, toxicity, efficiency) |
| [open-compass/opencompass](https://github.com/open-compass/opencompass) | ~5k | Apache-2.0 | 100+ datasets, self-hosted leaderboard |
| [huggingface/lighteval](https://github.com/huggingface/lighteval) | ~2k | MIT | Lightweight eval, 1000+ tasks |
| [THUDM/AgentBench](https://github.com/THUDM/AgentBench) | ~3.7k | Apache-2.0 | Agent benchmarking across 8 environments |
| [princeton-nlp/SWE-bench](https://github.com/princeton-nlp/SWE-bench) | ~4.5k | MIT | Code-generation regression suite |
| [LiveBench/LiveBench](https://github.com/LiveBench/LiveBench) | ~1k | Apache-2.0 | Monthly-refreshed contamination-resistant benchmark |
| [openai/evals](https://github.com/openai/evals) | ~19k | MIT | OpenAI's official eval framework and registry |

**Outage prediction:**

| Repo | Stars | License | Why |
|---|---|---|---|
| [yzhao062/pyod](https://github.com/yzhao062/pyod) | ~9k | BSD-2 | 60+ anomaly detectors with auto-selection |
| [unit8co/darts](https://github.com/unit8co/darts) | ~9.5k | Apache-2.0 | Time-series forecasting + anomaly detection |
| [salesforce/Merlion](https://github.com/salesforce/Merlion) | ~4.5k | BSD-3 | False-positive reduction for alerts |
| [thuml/Time-Series-Library](https://github.com/thuml/Time-Series-Library) | ~12.8k | MIT | State-of-the-art transformer models for time-series |
| [sktime/sktime](https://github.com/sktime/sktime) | ~10k | BSD-3 | Composable time-series pipelines |
| [open-edge-platform/anomalib](https://github.com/open-edge-platform/anomalib) | ~6.1k | Apache-2.0 | Intel's anomaly detection with experiment management |
| [arundo/adtk](https://github.com/arundo/adtk) | ~1k | MPL-2.0 | Simple rule-based anomaly detection |
| [linkedin/greykite](https://github.com/linkedin/greykite) | ~1.8k | BSD-2 | Changepoint detection with auto-tuned thresholds |
| [zillow/luminaire](https://github.com/zillow/luminaire) | ~750 | Apache-2.0 | Minimal-config anomaly detection |
| [AICoE/prometheus-anomaly-detector](https://github.com/AICoE/prometheus-anomaly-detector) | ~610 | OSS | ML predictions directly against Prometheus metrics |

**Drift detection:**

| Repo | Stars | License | Why |
|---|---|---|---|
| [evidentlyai/evidently](https://github.com/evidentlyai/evidently) | ~7.6k | Apache-2.0 | 100+ drift metrics including LLM-specific ones |
| [SeldonIO/alibi-detect](https://github.com/SeldonIO/alibi-detect) | ~2.5k | BSL (caution) | Text drift via embeddings — powerful but restrictive license |
| [NannyML/nannyml](https://github.com/NannyML/nannyml) | ~2.1k | Apache-2.0 | Estimates performance without ground truth |
| [online-ml/river](https://github.com/online-ml/river) | ~5.9k | BSD-3 | Streaming drift detection, constant memory |
| [whylabs/whylogs](https://github.com/whylabs/whylogs) | ~2.7k | Apache-2.0 | Lightweight data profiling and comparison |
| [deepchecks/deepchecks](https://github.com/deepchecks/deepchecks) | ~3.9k | AGPL-3.0 (caution) | Pre-built drift checks — powerful but AGPL |
| [IFCA-Advanced-Computing/frouros](https://github.com/IFCA-Advanced-Computing/frouros) | ~227 | BSD-3 | Clean statistical drift tests |
| [GenesisClawbot/llm-drift](https://github.com/GenesisClawbot/llm-drift) | <100 | MIT | LLM-specific behavioural drift detection |
| [egnaro9/model-drift](https://github.com/egnaro9/model-drift) | <50 | — | Deterministic daily regression tracker for 16 models |

**Observability (study, don't install):**

| Repo | Stars | License | Role |
|---|---|---|---|
| [langfuse/langfuse](https://github.com/langfuse/langfuse) | ~30k | MIT | Reference architecture for LLM observability |
| [comet-ml/opik](https://github.com/comet-ml/opik) | ~20k | Apache-2.0 | Automated eval hooks pattern |
| [Arize-ai/phoenix](https://github.com/Arize-ai/phoenix) | ~10.2k | ELv2 | Embedding drift analysis |
| [traceloop/openllmetry](https://github.com/traceloop/openllmetry) | ~7.4k | Apache-2.0 | OTel auto-instrumentation for LLM calls |
| [openlit/openlit](https://github.com/openlit/openlit) | ~6.6k | Apache-2.0 | 50+ provider instrumentation + cost tracking |
| [Arize-ai/openinference](https://github.com/Arize-ai/openinference) | ~1.1k | Apache-2.0 | OTel semantic conventions for AI |
| [whylabs/langkit](https://github.com/whylabs/langkit) | ~960 | Apache-2.0 | Quality/toxicity signal extraction |
| [BerriAI/litellm](https://github.com/BerriAI/litellm) | ~53k | MIT | Reference for routing + cost tracking |
| [Portkey-AI/gateway](https://github.com/Portkey-AI/gateway) | ~12.8k | MIT | Reference for edge gateway patterns |
| [AgentOps-AI/agentops](https://github.com/AgentOps-AI/agentops) | ~5.6k | MIT | Agent monitoring SDK (same problem space) |

**Reading lists:**

- [awesome-LLM-AIOps](https://github.com/Jun-jie-Huang/awesome-LLM-AIOps) — 93+ papers on AI + operations
- [awesome-TS-anomaly-detection](https://github.com/rob-med/awesome-TS-anomaly-detection) (~3.2k stars) — time-series anomaly tools and datasets
- [ai-agent-benchmark-compendium](https://github.com/philschmid/ai-agent-benchmark-compendium) — 50+ agent benchmarks

#### What NOT to do

- **Don't build a general-purpose ML platform.** The sidecar runs specific jobs, not an
  experiment tracker or feature store.
- **Don't replace the Go eval pipeline with Python.** The sidecar adds capabilities; it
  doesn't replace what works.
- **Don't train on the GPU.** CPU-only ML models are fine for solo scale.
- **Don't adopt BSL/AGPL libraries without evaluation.** Stick with Apache-2.0/MIT/BSD.
- **Don't put ML in the request path.** Every ML job is offline. The gateway stays fast.

---

## 11. Model-intelligence pipeline (v2 plan addition, 2026-09-20)

> **Plain-English version.** §10 teaches the gateway to spot sick models. This section
> teaches it to *know its own models*: first that they exist (J), then what they cost
> and claim (K), then which one actually answers best (L), and finally to route by
> that evidence (M). Availability before selection — a correctness fix (J) ahead of
> three optimization tracks (K→L→M), per RULES.md §2.

**Why this order.** An advisor that recommends an unpulled model prescribes a 404.
`J` is the correctness floor for everything below and for §10 alike; `K` stops you
benchmarking models that can never fit 8 GB; `L` produces the evidence `M` acts on.
Each track is independently demoable — stop after any of them with something finished.

**Scenario labels (shared).** Routing scenarios are the existing `reason` values
(`short-simple-prompt`, `long-or-complex-prompt`, `explicit-model`) plus a
`code-vs-prose` tag on golden pairs when present. No new taxonomy until the data
demands one. Significance gate everywhere: n≥100, p<0.05 (Track F's rule, reused).

### Track J — startup presence check (½ day) — DONE 2026-09-21 (code + live)

Brief: the gateway stops advertising models it cannot serve. Serves day-two
question 1 (which model answers this request) by answering the prior question:
is the model even on disk?
- [x] `OllamaClient` decodes `GET /api/tags` into a pulled-set (normalize `name` vs
  `name:tag`) → Verified: `TestOllamaPulledModelsDecodesTags` (fake `/api/tags`
  without the model reads as missing; `:latest`-stripped names match)
- [x] Boot + every probe tick refresh the set; each gap logs
  `model "X" not pulled — run: ollama pull X` → Verified live: fake fast tier
  logs the gap at boot; `TestRefreshPulledLogsGapOnce` (once-per-model),
  `TestAfterProbeRunsEachTick`
- [x] `GET /v1/models`: per-model `up = backendUp && pulled` + `reason:"not_pulled"`;
  `Plan()` filters unpulled refs; explicit request for one → `503 model_not_pulled`
  naming the model (one error shape; 403 sensitive-cloud refusal still wins on
  explicit cloud) → Verified: `TestPresenceSkipsUnpulled` + `TestModelsShowsNotPulledReason`
  + policy `TestPolicyModelNotPulledIsOneErrorShape`; live: auto + explicit both
  503, pulled 7B serves 200, junk path entry gone
- [x] `docs/API.md` documents the code + reason; README flags section unchanged
  (no new flags) → Verified: policy tests green
Done: one chat to a pulled tier + one explicit request for an unpulled name shows
`503 model_not_pulled`; `/v1/models` shows the gap; no `502` surprise at request time.
Plus, found live: `OLLAMA_MODELS` collides with Ollama's own models-directory
variable (path advertised as a model) — path-like entries now skipped with a log
line (`TestExtraModelsSkipsPaths`, ADR-0011). E2E also exposed the dead 90-day
purge (`($1 || ' days')` int/text mismatch — retention never ran) — fixed to
`make_interval(days => $1)` (`TestPurgeConversationsBuildsTypedInterval`), proven
live (`dropped 1`).

### Track K — static advisor (½–1 day) — DONE 2026-09-21 (code + live)

Brief: a startup report from metadata + cached public data. Informs the operator;
changes nothing at runtime. Needs J (only present models are advised on).
- [x] Per configured model: size, quant, context window, license, VRAM estimate vs
  the 8 GB budget (`docs/VRAM.md` numbers) → Verified: `go run . --advise` lists
  both tiers fits-alone + swap co-residency (`TestAdviseKnownTiersFit`,
  `TestReportPrintsTable`)
- [x] Cached public scores (LMArena category ranks, Artificial Analysis quality +
  $/speed) marked *rumor, not measurement* with source + date → Verified: table
  shipped empty (same-day search found nothing citable — honest, printed aloud)
  + `TestCachedRumorsCarryProvenance` binds future adds
- [x] "Cannot-fit" veto: any model that cannot fit the box is flagged before it can
  enter Track L → Verified: `TestAdviseVetoesCannotFit` (40 GB → VETO);
  unknown names get "no static data" (`TestAdviseUnknownModel`), unpulled get
  "pull first" (`TestAdviseSkipsUnpulled`)
Done: `go run . --advise` (or startup block) prints the table; zero behaviour change.
New package `advisor/` (stdlib only — single-dep rule safe); one flag + README
row (policy); ADR-0012; live: pulled lineup + unknown-presence path with Ollama down.

### Track L — local benchmarking (2–3 days) — CODE DONE 2026-09-21, runs in flight

Brief: score your pulled models against your golden set on your GPU; the
classification table from §11's premise. Needs J + K. Extends `evals/`, never
replaces it.
- [x] Run the suite per model over the same frozen golden version (interleaved, same
  judge, `judge_changed` invalidates the comparison) → Verify: two `eval_runs`
  rows, same golden hash, different `judge_model`/`model` under test
  (`--score-model`, `0006_benchmark.sql`; run A on 3B started 20:13, run B on 7B queued)
- [x] Per-scenario split by `reason` (+ `code-vs-prose` where tagged): faithfulness,
  p50 latency, tokens per answer → Verify: `BuildComparison` scenarios from
  `router.Classify` (`TestBuildComparison*`); no golden pair carries a
  code-vs-prose tag, so that split is noted-absent, not built
- [x] Verdicts only at significance (n≥100, p<0.05); smaller gaps reported as
  "tied — route on cost" → Verify: `evals.WelchPValue` (A-S 7.1.26) + `decideWinner`
  (`TestWelchPValue*`, winner test at n=120; v3's 72 pairs land tied by design)
- [x] Console `#/benchmarks` (read-only matrix + run history), Tower rules per §12 →
  Verify: `pageBenchmarks()` (four states, catalogue-only, chip-not-pill for the
  winner), `TestBenchmarksEndpoint`, design-system example, `node --check` clean
Done: the table names a winner per scenario with evidence, or "tied" with honesty.
Faithfulness path shared with golden scoring (`judgeAnswer` extract,
`TestScorePair` green); generation failures fail loudly (atomic run writes
nothing); tokens = completion tokens from the generator (honest, not estimated).

### Track M — adaptive routing (3–5 days)

Brief: the gateway acts on L's evidence. Touches the request path, so it carries
the heaviest guardrails in this plan. Needs L at significance.
- [ ] Promotion rule: per-tier winner promoted only at n≥100, p<0.05; every moved
  request spans its `reason` + evidence run id → Verify: promotion + span audit
- [ ] Guardrails: never route sensitive traffic to an unproven model; no flapping
  (minimum hold + hysteresis); instant rollback flag → Verify: dedicated tests per
  guardrail, bind-checked
- [ ] Sampling tax bounded (N% shadow traffic, documented) → Verify: overhead
  unchanged on the k6 gate
Done: traffic moves per evidence with an audit trail; rollback demonstrated live.

### Track M1 — remote judgment pool (2–3 days, after M's guardrails)

Brief: non-sensitive *judgment* bursts to free-tier remotes; generation, routing and
all sensitive work stay local, always. Story becomes "sovereign local with burst
judgment", fail-closed intact. Needs M's guardrail shape + Groq path exercised first.
- [ ] Reviewer role on Groq 70B for non-sensitive workflows via existing
  `OpenAIBackend` (config only: base URL + key env, documented in `docs/API.md`) →
  Verify: remote-reviewed workflow completes; disagreement vs local reviewer recorded
- [ ] Second vote from exactly one second provider (Cloudflare `@cf/` via
  `.../accounts/{ID}/ai/v1`, or Gemini via `.../v1beta/openai/`), chosen by ADR on
  measured pain (outages hurt → Cloudflare; third-lineage vote/big-context review →
  Gemini) → Verify: disagreement metric across three lineages, no 1–1 deadlocks
- [ ] Per-provider coded caps (Groq req/day, Cloudflare Neurons/day, Gemini RPM/RPD);
  70% alert, 100% → local fallback, never user-visible failure → Verify: cap hit in
  a test reads as fallback spans, not errors
- [ ] Provider error dialects → one table: `429` retries with `Retry-After`;
  Cloudflare `403`/`5035`/`3040` and any outage mean unavailable → next provider →
  local, never retried → Verify: new retry-table cases, bind-checked
- [ ] Fence with three rows: sensitive-span filter before every remote call (policy
  test, RULES.md §14), PRIVACY.md documents per-provider scope → Verify: policy
  green; sensitive canary appears in no remote payload
- [ ] Counters `remote_calls_total{provider,role}` +
  `remote_fallback_total{provider,reason}`; monthly calibration review reads three
  judges' rows → Verify: dashboards populate; review log entry written same day
Done: reviewer bursts remote with local fallback proven by killing the primary
mid-workflow; disagreement data flowing; $0/Neuron/RPD pools unbreached.
Never: drafter/generator remote (burns scarcest quotas to save owned GPU); router
classification remote (sees every prompt including sensitive ones — architectural,
not budgetary); cost-routing arbitrage between pools (pools are headroom, not income).

### Track N -- universal MCP + Try playground + live mission view (2-3 days, Go-only, after M1) — DONE 2026-09-21 (working tree; evolved into `#/cockpit`)

Brief: the gateway stops looking Claude-exclusive and the console gains the one
missing mirror. Serves day-two question 4 (what happened to request X) for both
humans and any LLM agent: HTTP stays the source of truth, MCP stays a thin typed
wrapper, Tower gains `#/try` + `#/live`. Needs C (console) + #3 mcp-min only.
Verified baseline 2026-09-21: `rg "mcp\.tool|/v1/events|#/live|EventSource"`
matched nothing; `go test -count=1 ./policy/` green -- the track started from zero.
- [x] Universal MCP docs: `mcp/README.md` retitled "any MCP client" (Cursor,
  Windsurf, VSCode, generic python `mcp`) + `POST /mcp` Streamable-HTTP reusing
  `handle()` behind `REQUIRE_API_KEY=true`; stdio stays for local, HTTP for
  remote LLMs; `route_test_request` stays on the same `Router.Chat()` path -->
  Verified: stdio `tools/list` still 5 tools; HTTP MCP call returns same shape;
  policy tests green
- [x] Redacted `mcp.tool` span per MCP tool call (tool/trace_id/latency only,
  never prompt) so the live view is never empty --> Verified:
  `TestPolicyMCPToolSpansRedacted` green (bind-checked)
- [x] `spanWriter` fan-out (DB + SSE hub, 64-deep per subscriber, drop+count) +
  `GET /v1/events` SSE --> Verified: Postgres stopped, chat still 200;
  `curl -N /v1/events` receives `mcp.tool`; `TestEventsDropNotBlock` green.
  (Plan said 128 buffer / 10s ctx; shipped 64-deep per subscriber — same
  lossy-by-design shape, see `events.go`.)
- [x] Console `#/try` playground (single prompt --> model/reason/trace_id/text +
  link to `#/traces/<id>`, no history/streaming v1) + `#/live` mission view
  (ticket rail `route.decide --> model.generate --> router.respond` and
  `researcher --> drafter --> reviewer`, `EventSource` append, single amber
  accent) reusing `panel/waterfall/pill/chip`, `h()` only, four states each -->
  Verified: Tower policy tests green; DB-down shows error + Retry; no new hex.
  **Evolved same day:** `#/try` + `#/live` retired into **`#/cockpit`** — send +
  sight on one surface (sticky send/answer column beside shared `mountRail`
  live rail, own ticket pinned amber, "Run as workflow" → `POST /v1/workflows`
  with last input, client-held conversation per ADR-0010); old hashes fall back
  to Overview. See `docs/try-live-cockpit.html`, `docs/cockpit-playbook.html`,
  `docs/live-rail-playbook.html`.
- [x] Docs: `docs/API.md` (`POST /mcp`, `GET /v1/events`), design-system
  example for `#/live`, CHANGELOG `Unreleased` line --> Verified: policy
  `EnvVarsDocumented/AgentFilesPointHere` green. Plus `PRIVACY.md` conversation
  exception, `docs/adr/0010` (proposed), conversations endpoints.
Done: any MCP client lists 5 tools; `#/cockpit` sends once and deep-links the
trace; live rail slides tickets in real time with prompts redacted; full gate
`gofmt -l . && go vet ./... && go test -race ./... && go test -count=1 ./policy/`
green + browser check at 1900px and default width.
Never: prompt text in spans/events (policy fence), JSON-RPC in the browser,
chat history, new colors/fonts, multi-replica lease (still Track D).
Follow-up shelf (same batch, opt-in): `0005_conversations.sql` + 5 conversation
endpoints + 90-day boot purge + per-thread delete — gateway chat path stays
stateless; storage only when the client asks.

### Track O — pre-commit verify gate (½ day, ECC verification-loop port) — DONE 2026-09-22

Brief: one script that proves the tree is green before a commit exists. Serves
operability (RULES §2). ECC runs this as hooks; here a script + CI job (no daemon).
- [x] `scripts/verify.sh` (gofmt, vet, tests, build, policy, diff stat) →
  Verified red-green: green on this tree, FAIL gofmt on a scratch breakage,
  green again on removal (Git Bash; system bash is a broken WSL stub)
- [x] CI job calls the script (`VERIFY_RACE=1`; SHA pins untouched) → Verified:
  `go` job is one verify step; local runs the same file bare
- [x] README documents the script + manual PowerShell equivalent →
  Verified: secret grep deliberately deferred to Track P (allow-listed policy
  test beats a wolf-crying grep)
Done: `scripts/verify.sh` is the single definition of green.

### Track P — agent-config self-scan (½ day, ECC AgentShield port, config-only) — DONE 2026-09-22

Brief: audit the agent surface, not the app. Serves the fail-closed posture:
a leaked key or wildcard permission in our own config is a hole no gateway
rule can close. Scans config; never executes anything.
- [x] `TestPolicyAgentConfigClean` over an explicit shipped/executed file list
  (agent files, workflows, compose, Dockerfile, console assets, MCP readme);
  docs/lessons/tests out of scope by construction, no allow-list → Verified:
  green on clean tree, FAIL on scratch breakage (watched, then reverted);
  grilled pre-code (10 objections accepted, 2 rejected with rationale)
- [x] RULES §9 row for the scan → Verify: `TestPolicyRulesCiteRealTests` green
Done: caught a real one on arrival — `postgres://USER:PASSWORD@…` DSN in
`mcp/README.md`, now env parts like the compose fix.

### Track Q — embedding hash cache (½–1 day, ECC content-hash-cache port) — DONE 2026-09-22

Brief: stop re-embedding unchanged chunks. Ingest writes a SHA-256 `{hash}.json`
sidecar per chunk; re-ingest embeds only what changed; corruption reads as a
miss, never an error. Ingest-local: no request path, no schema.
- [x] `evals/hashcache.go` read-through cache (`TextEmbedder` interface;
  `Ingest` takes the interface, `*Embedder` implements it) → Verified:
  5 unit tests (hits, new-text miss, truncated/corrupt miss + rewrite,
  model-change miss, dims-mismatch miss) with counting fake + tempdir
- [x] Sidecars beside the clean tree (`evals/corpus/cache/`, git-ignored),
  payload carries model+dims+vector with a validity predicate (parse, model,
  dims==len, finite) → Verified live: 36 new first ingest, 36 hits second
  (seconds), retrieval still correct; grilled pre-code (10 accepted, 2 rejected:
  file-I/O cost needs no estimate at N=36, daily-loop payoff)
- [x] Miss-reason counters in the ingest log + recovery doc (`docs/API.md`) →
  Verified: `hits=36 new=0 corrupt=0 model_changed=0` live
Done: re-ingest of an untouched corpus costs zero embedding calls.

### Track R — router context budget (1 day, ECC context-budget port)

Brief: a counted token ledger per request (chars/4 estimate, 500 tokens per
tool schema): retrieval topK + generation params are trimmed to fit a
configured budget. Counting only — no extra inference on the request path.
Over-budget trims chunks, never fails the request.
- [ ] Budget assembly in the chat path with trim-oldest-chunks-first →
  Verify: oversized-context test trims to budget; small contexts pass through
  byte-identical
- [ ] `route.decide` span records `budget_trimmed` (count, never text) →
  Verify: span test asserts the attr, prompts-never-in-spans still green
- [ ] `docs/API.md` documents the budget, the estimate, and trim behavior →
  Verify: policy tests green
Done: no prompt assembly exceeds the budget, whatever the corpus grows to.

### Track S — create-only history (1–2 days, ECC memory-vault port)

Brief: history is appended, never overwritten. `eval_runs` and
`workflows/steps` gain a `supersedes` link; readers resolve the latest, and a
history query returns the full chain. The vault idea without the daemon.
- [ ] Migration `0007_supersedes.sql` + store writers append-only (UPDATEs
  removed from these paths) → Verify: re-score/resume appends rows; row
  counts grow, nothing mutates
- [ ] Chain query (latest + full history) for evals and workflows → Verify:
  history endpoint returns the chain in order; existing tests updated, green
- [ ] No console change (API-only; Tower renders the chain when a page needs
  it) → Verify: full gate green, no new Tower classes
Done: every eval run and workflow step is forever auditable.

### Track T — eval release gates (1 day, ECC eval-harness port)

Brief: a release gate with teeth. A version tag requires k-of-k consecutive
golden passes on the frozen set; candidate execution stays local-only (their
containment refusal, ported as policy, not infrastructure). Gates releases —
never routes (Track F's n≥100/p<0.05 remains the only promotion rule).
- [ ] Gate check (script + test): last-k runs for the golden version all green
  → Verify: gate test fails a tag with one flaky run in the window
- [ ] CHANGELOG release checklist cites the gate → Verify: checklist present;
  first tagged release notes the gate result
Done: no tag ships on a flaky suite.


**What NOT to do (this section).** No cloud leaderboard lookups at runtime. No new
ML model to "predict the best model" — the classifier is a query over eval rows
plus a promotion rule. No auto-selecting the lineup: the advisor compares, the
operator (or Track F's gate) promotes. No fine-grained claims ("best for legal
summarization") without golden pairs in that scenario — no data, no claim.

**Decision log.** Presence-before-advisor (correctness before optimization,
RULES.md §2); four phased tracks over one big build (each demoable, per §1
contract style); scenarios reuse `reason` values (no new taxonomy prematurely);
Track F's significance gate reused for L/M (one statistical standard, not two);
remote judgment pool (M1): reviewer-first, one-second-provider-later by ADR on
measured pain, local generation/routing permanently (fence + quotas, not preference).
**v22 exception:** Track N shipped before J — deliberately out of §11 order.
N is Go-only, needs only C + #3, and unblocks every demo (any MCP client, live
sight, cockpit); J/K/L/M stay ordered after it. No ML ordering (§10) affected.
**v26 (Tracks O–T, ECC ports):** six small tracks, dependency-ordered —
tooling first (O gates everything after), config scan while small (P),
ingest-local (Q), request-path counting (R), schema change (S), release gate
last (T, needs L/F context to mean anything). Each independently demoable and
stop-anywhere; multi-agent-brainstorming grills each brief pre-code. Rejected:
two batched tracks (breaks ≤3-day sizing), folding into shipped tracks
(reopens done-done criteria). ECC's daemon, multi-harness weight, runtime
leaderboard lookups, and auto-promotion explicitly not ported.


