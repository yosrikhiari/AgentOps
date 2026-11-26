# Tower — enhancement research (November 2026)

Research-backed answer to "what should Tower become next", combining
(a) what comparable open-source systems (Langfuse, Bifrost, OpenObserve,
Databricks MLflow, Helicone, Promptfoo, Open WebUI) do well,
(b) the relevant research literature (judge validity, trajectory eval,
routing ceilings, tail-at-scale, MCP security, OTel GenAI semconv),
and (c) the agreed console backlog (`docs/tower-enhancements.html`, 20
examples). Every item cites a source, a file, and an effort. Items already
shipped (judge validity machinery, OTLP export, trajectory metrics,
`--judge-audit`, ceiling metrics, ML routing Phase 1, console four states)
are excluded per `docs/AUDIT-2026-11.md` §0.

Baseline: `main` at `8b02b7c`, 244 tests, policy 19/19, `go vet` clean.

---

## 1. Executive summary

Tower's position is the intersection of **one box, no SaaS, every line
explainable, with evals + traces + durability + MCP**. That intersection is
genuinely underserved and the docs are honest about where the line is. The
single biggest leverage is **not** adding providers or features; it is making
the existing surfaces faster to use (fewer clicks) and making the eval number
defensible. Concretely, the five moves that change the product most:

1. **Command palette + saved views + filters in the URL** (console) — cuts
   every "reach a trace / run an eval" path from 3–4 clicks to one keystroke.
   (`docs/tower-enhancements.html` ex04, ex06; OpenObserve #11348; MLflow
   saved views.)
2. **Two-pane waterfall + flame graph + critical path** (traces) — Langfuse's
   rebuilt Timeline (PR #14572) is the reference; Tower's current single-list
   waterfall is the weakest part of the console.
3. **Hybrid retrieval (BM25 + vector, RRF) + separate `retrieval_miss`**
   reporting — the v3 score is currently capped by two vocabulary misses
   (pairs 12, 66) that no router can rescue.
4. **Trajectory grading for workflows (τ-bench style, final-state)** — the
   largest uncovered surface and the cheapest differentiator vs. Langfuse /
   AgentOps.ai.
5. **Judge cross-check on Groq llama-3.3-70b** to lift κ from 0.64 toward 0.8.

The rest is cheap, well-scoped, and ordered in §10.

---

## 2. Where Tower stands today (short)

- **Architecture 9/10**, code quality 8.5, testing 8, security 8, perf 8,
  observability 9, DX 9, niche fit 8.5 → **8.5 overall** (`REVIEW-2026-11.md`).
- Console: 9 pages (overview, traces, cockpit, GPU, evals & drift, benchmarks,
  workflows, metrics, models), framework-free, embedded, Tower tokens.
- Known soft spots: 80 KB `main.go`, per-process limiter, no mixed-workload
  k6 gate, moderate judge agreement (κ 0.64), no workflow trajectory grading,
  no OTLP gRPC, no `/metrics` auth by default.

---

## 3. Similar solutions — what to borrow, what to ignore

| System | Best at | Borrow | Do not chase |
|---|---|---|---|
| **Langfuse** (ClickHouse) | Trace UI, saved views, waterfall two-pane, OTLP | two-pane waterfall + flame graph, saved views per table, "filters first" discovery (`langfuse.com/blog/2026-03-10-simplify-langfuse-for-scale`) | ClickHouse/Redis/S3 infra |
| **Bifrost** (Go) | 11 µs gateway overhead, 23 providers, MCP gateway | provider breadth *as "any OpenAI-style"* only | provider count race |
| **OpenObserve** | Global command palette (Cmd+K) across pages/entities | palette with pages + actions + recent traces (#11348) | — |
| **Databricks MLflow** | Custom trace views, saved views per experiment, gen-AI eval | saved views storing filters/sort/columns/density; "edit with Genie" | hosted Genie |
| **Helicone** (maintenance) | One-line proxy onboarding | `tower init` + `curl \| sh` to first trace < 5 min | — |
| **Promptfoo** (OpenAI) | Golden-set/CI gating, red-teaming CLI | import Promptfoo test files → golden set; export `eval_runs` | breadth of metrics |
| **Open WebUI** | Arena evals, OTel export, multi-Ollama | recipe: point at Tower, keys per user, arena → `eval_runs` | — |
| **kentik / Dynatrace** | Saved views, keyboard shortcuts, density switch | saved views + density toggle + shortcut sheet | — |

**Key insight from Langfuse's scale blog:** discovery starts with filters and
saved views; the waterfall is "one click away when you need the full picture".
Tower currently inverts this — the waterfall is the page. Adopting the
filter-first model is the single largest UX win available and it costs no new
backend.

---

## 4. Research literature — what it implies for code

| Paper / result | Implication for Tower | Status |
|---|---|---|
| *Reliability without Validity* (arXiv 2606.19544): Qwen3-8B has the highest measured position bias (0.192) of 21 judges | Ship a second judge via Groq and lift κ above 0.8; keep the flip probe | open |
| *All Verdicts are Not Equal* (arXiv 2610.12083): agreement ≠ validity | Trustworthy-verdict rate already reported; keep it | shipped |
| *Unsolvability Ceiling* (arXiv 2605.07395): standard routers collapse to majority class | Report `unsolvable` and `co_failure_rate` honestly (§1.2 audit) | open |
| *Co-Failure Ceiling* (HF 2606.27288) | Define β once two models have outcomes; report "undefined — one model" today | open |
| τ-bench (Yao et al. 2024): final-state grading, not transcript | Trajectory eval must grade the final state once the toy workflow has side effects | open |
| *The Tail at Scale* (Dean & Barroso, CACM 2013): hedged requests | Backend circuit breaker / cooldown (audit §3.1) + hedging for up-but-slow | open |
| MCP security: OWASP ASI01/ASI02, CVE-2025-54136 | Description pinning by hash before any MCP *client/proxy* capability (audit §4.2) | open |
| OTel GenAI semconv: still `Development`, no tagged release | Dual-name attributes already done; re-check on every semconv release before bumping `ScopeVersion` | ongoing |
| MCP 2026-07-28: stateless, `server/discover`, header negotiation | Upgrade `mcp/server.go` (declares `2024-11-05`) — four revisions behind | open |

---

## 5. UI/UX inspiration — reduce clicks, increase trust

Each maps to an enhancement example in `docs/tower-enhancements.html` (exNN).

### 5.1 Wayfinding (ex04, ex05, ex06)
- **Command palette (Ctrl+K)** with three groups: *Go to* (pages, Metrics, Models),
  *Actions* (run eval suite, start workflow, copy curl), *Recent traces*.
  Anything that looks like a trace/workflow id opens it directly. Replaces no
  existing UI — it is a second door. (OpenObserve #11348.)
- **Breadcrumbs + share link**: `Traces › chat 8f2a1c3d`; replace "Copy id" with
  split "Copy link" (full URL) + "Copy id".
- **Filters in the URL** (`#/traces/<id>?f=error`) with counts on chips; a
  zero-count chip is disabled, not hidden, so the operator learns the vocabulary.

### 5.2 Live data that feels live (ex07, ex08, ex09)
- **Live tail**: key rows by `trace_id`; prepend only unseen rows with a 1.6 s
  amber-dim fade; pause (indicator turns amber "paused") while the pointer is
  over the table. Metric cards update in place. (No full-page rebuild.)
- **Sparklines + count-up** in metric cards: return `traffic.series_1h` and
  `latency.series_1h` from `/v1/overview`; 28 px SVG polyline, last point amber;
  tween the number over 400 ms. Faithfulness card gets last 10 run scores.
- **Relative time everywhere**, absolute on hover, ticking on a 30 s interval;
  past 24 h switches to date + time. One helper `when(iso)`.

### 5.3 Trace inspector (ex11, ex13, ex14, ex15)
- **Styled chart tooltips** (not SVG `<title>`): positioned `.tower-tip`,
  time · model · latency · reason, hovered bar brightens, crosshair line.
- **Two-pane waterfall + ruler + hover sync + critical path** (Langfuse PR
  #14572): fixed name-gutter (never scrolls horizontally) + scrollable chart;
  ruler with 4–5 ticks; hovering a waterfall row or a span-tree step highlights
  both via `data-span`; compute the critical path (longest chain from root) and
  outline those bars in amber with their share of total.
- **Typed attribute viewer**: colour by type (string green, number amber,
  boolean red, null faint), arrays as chips, truncate > 120 chars with toggle,
  per-row copy on hover, important keys (`error`, `model`, `latency_s`) pinned
  first.
- **Search within a trace**: text filter over span names + attribute values,
  "errors only" chip, expand/collapse all; failed spans open by default.

### 5.4 Foundations (ex01, ex02, ex03, ex10, ex16, ex17, ex18, ex19, ex20)
- **Visible focus rings** (`:focus-visible` only, mouse clicks stay clean) + make
  trace items real buttons + j/k/Enter navigation.
- **Contrast-safe `ink-faint`**: split into `--tower-ink-ghost` (decorative) and
  `--tower-ink-faint:#8B838B` (smallest text). Today's `#635C63` fails WCAG AA
  on `surface #201D22`.
- **Reduced motion + light theme**: `prefers-reduced-motion` guard; light token
  set on `[data-theme="light"]` with manual override persisted in localStorage;
  amber darkens to `#B96F05`.
- **Shaped skeletons** (`skelMetrics`, `skelTable`, `skelPanel`) so layout is
  stable before data lands.
- **Toast queue** (max 3, oldest drops) with actions (View/Undo/Retry), timer
  bar, pause on hover; errors stay until dismissed.
- **Button states**: spinner + disabled + confirm-in-place (destructive).
- **Eval progress**: bar + ETA + cancel; needs API progress field.
- **Tables**: sortable/sticky headers, density switch (compact/comfortable),
  zebra off by default.
- **Glossary tooltips + shortcut sheet** (`?`).

### 5.5 New visual surfaces (from `tower-enhancements.html` ex21 + research)
- **Router answers** card: when a question about the gateway is answered by the
  router itself (no model call), show it as a chip ("answered by: router") with
  the source trace — already in the API, surface it in the console.
- **GPU → eval correlation**: per-run GPU summary and a "model swaps during this
  run" warning in `--score` and `--release-gate` (Tier 0 #4).
- **Flame graph view** for traces (Langfuse #16219): same data re-projected
  with y = nesting depth instead of row index — reveals where time goes at a
  glance.

---

## 6. Code-level enhancements (from audit §5, ordered)

| # | Item | File | Effort | Policy/test |
|---|---|---|---|---|
| 1 | Split `main.go` (80 KB) into `internal/bootstrap` | `main.go` | 1 d | coverage |
| 2 | `chargeKey` → single worker draining 1024-deep channel (drop + count) | `router/keys.go` | 0.5 d | pattern test |
| 3 | `HealthProber.ProbeOnce` parallelize with WaitGroup | `router/backend.go` | 0.5 d | — |
| 4 | Shared `tunedTransport` injected into backends | `router/ollama.go`, `openai_backend.go`, `main.go` | 0.5 d | — |
| 5 | Backend circuit breaker / cooldown (up-but-slow) | `router/backend.go`, `router/router.go` | 1 d | `TestPolicyCoolingBackendSkipped` |
| 6 | Bounded streaming concurrency (`MAX_CONCURRENT_STREAMS`, 503 + Retry-After) | `router/server.go`, `metrics.go` | 0.5 d | — |
| 7 | Span compaction (`SPAN_COMPACT_AFTER`) | `main.go` sweep, `tracker/spans.go` | 0.5 d | — |
| 8 | Fuzz `parseVerdict`, request decoders, SSE emitters | `evals/*`, `router/*` | 1 d | fuzz targets |
| 9 | Property test for fail-closed routing rule | `router/*_test.go` | 0.5 d | property |
| 10 | Console Postgres-down path on every endpoint | `console/*_test.go` | 1 d | — |
| 11 | Mixed-workload k6 gate (short fast + long quality + streaming) | `load-tests/` | 0.5 d | gate |
| 12 | `sort.Strings` in `tracker/otlp.go` | `tracker/otlp.go` | 5 min | — |
| 13 | Injectable retry jitter | `router/retry.go` | 0.5 d | — |
| 14 | `golangci-lint` advisory job | `.github/workflows/ci.yml` | 0.5 d | — |
| 15 | `params.go` ceilings → struct with `validate()` | `router/params.go` | 0.5 d | — |

---

## 7. Feature additions (from MARKET.md §6 + audit §2–4)

### P0 credibility (≈ 6 days)
1. **`:cloud`-aware locality** on `ModelRef` + policy test — **done**.
2. **Hybrid retrieval**: Postgres FTS (BM25-ish `ts_rank`) + vector, RRF k=60,
   weights 0.6/0.4; `--search` shows both scores. Fixes pair-12/66 misses.
3. **Scorer reports `retrieval_miss` separately** from unfaithful; console shows it.
4. **OTLP polish**: `--otlp-endpoint` flag, `/v1/otlp/status`, retry/backoff on 5xx.
5. **Anthropic Messages API** `/v1/messages` (request/response, streaming) — **done**;
   tool passthrough still open (audit §2.3).

### P1 differentiators (≈ 12 days)
6. **Shadow-test auto-promotion** (Track F): sample N% of quality traffic to the
   fast model too, judge both, promote per task type at n ≥ 100, p < 0.05, with
   a console page showing the evidence. The local, explainable answer to
   RouteLLM/Not Diamond.
7. **Sovereignty policy engine**: per-key `data_policy` (`local_only`/`cloud_ok`),
   PII detectors beyond keywords (IBAN/CIN/phone/email regex, French + Arabic
   keyword sets), audit line per blocked request, `router_sensitive_blocked_total`.
8. **Spend in currency** per key/model (price table, Ollama = optional electricity
   estimate), budget alerts, key management in the console.
9. **Opt-in capture** per key: prompts/answers encrypted at rest (AES-GCM, key
   from env), retention days, redaction on read stays default.
10. **MCP tool-call spans + per-key tool allow-list** (proxy MCP servers, record
    `execute_tool` spans) — only after description pinning (§4.2).

### Tier 0 (November revision, §9.3)
11. **Release trust pack**: cosign keyless, SBOM, provenance, `--version --json`,
    verify recipe — **mostly done** (README has it).
12. **Anthropic ingress** — **done**; tool passthrough open.
13. **MCP 2026-07-28**: `server/discover`, stateless HTTP, protocol headers,
    version negotiation, registry listing.
14. **GPU → eval correlation**: per-run GPU summary + "model swaps during this run"
    warning in `--score` and `--release-gate`.

### P2 distribution
15. `curl -fsSL … | sh`, Homebrew tap, `winget`, GHCR image, `tower init`.
16. Open WebUI recipe (native MCP, auto tool permissions, arena → `eval_runs`).
17. Import Promptfoo test files → golden set; export `eval_runs` as Promptfoo/JSONL.
18. "Migrating from LiteLLM / TensorZero / Helicone" guides + 2-min demo + docs site.
19. Multi-replica correctness: shared key counters in Postgres, `FOR UPDATE SKIP
    LOCKED` resume lease, eval lock row.

### P3 security
20. Authenticate `/metrics` (`METRICS_TOKEN`, bearer or loopback peer).
21. MCP tool description pinning by hash + provenance record before any proxy.
22. Console remote mode: per-route `CONSOLE_TOKEN` bearer.

---

## 8. Optimization (performance, VRAM, evals)

- **Shared HTTP transport** (item 4 above) removes per-request DNS/TLS churn
  for cloud backends.
- **Circuit breaker / cooldown** (item 5) is the biggest latency-tail risk fix.
- **Streaming concurrency cap** (item 6) prevents goroutine exhaustion on a
  small box under a slow model.
- **Eval concurrency**: the suite is serial; with an 8 GB card, one chat model
  resident at a time means eval + live traffic contend. Document `EVAL_TIMEOUT`
  and add a "quiet GPU" check to `--release-gate` (Tier 0 #4).
- **Span compaction** (item 7) keeps the `spans` table bounded on a long-running
  gateway.
- **Mixed-workload k6 gate** (item 11) measures router overhead where it
  actually matters (today's gate is `/health` only).

---

## 9. Remodeling (console information architecture)

Adopt Langfuse's "filter-first, waterfall one click away" model:

1. **Overview** stays the landing: metric row + traffic band chart + live tail +
   backend health + recent traces. Add sparklines and count-up.
2. **Traces list** becomes filter-first: saved views, URL filters with counts,
   density switch, command-palette jump. The trace detail (waterfall + span tree)
   opens in a side panel or full page on click, not as the page itself.
3. **Trace detail** becomes a two-pane waterfall (Langfuse PR #14572) with ruler,
   hover sync, critical path, flame-graph toggle, typed attributes, in-trace
   search.
4. **Evals & drift** adds: eval progress with ETA/cancel, sparkline of last 10
   run scores, retrieval-miss count, worst pairs, judge health (κ, trustworthy
   rate).
5. **Workflows** adds: trajectory chip per workflow (steps, done, attempts,
   retries, clean), pass^k, `--workflow-grade` rubric result, open trace link.
6. **Models** adds: router weights hash, per-model spend, locality (`:cloud`
   aware), VRAM fit from `--advise`.
7. **GPU** stays as-is (already best-in-class: no competitor reports GPU
   util/VRAM/power/temp/resident models correlated with a trace or eval run).
8. **Cockpit** stays operator-focused; add command palette and shortcut sheet.

---

## 10. Prioritized backlog (effort × impact)

Effort in solo days. Dependencies noted.

| Priority | Item | Effort | Depends on |
|---|---|---|---|
| P0 | Hybrid retrieval + `retrieval_miss` | 2 d | corpus ingest |
| P0 | Judge cross-check on Groq (κ ≥ 0.8) | 0.5 d | `GROQ_API_KEY` |
| P0 | Judge prompt v3 variant + audit | 0.5 d | — |
| P0 | OTLP polish (flag + status + retry) | 0.5 d | — |
| P0 | Anthropic tool passthrough | 1.5 d | — |
| P1 | Trajectory grading for workflows | 3 d | toy workflow side effects |
| P1 | MCP 2026-07-28 upgrade | 1.5 d | — |
| P1 | GPU → eval correlation | 1 d | GPU telemetry (shipped) |
| P1 | Shadow-test auto-promotion | 4 d | ceiling metrics |
| P1 | Sovereignty policy engine | 2 d | — |
| P1 | Spend in currency | 2 d | price table |
| P1 | Opt-in capture (encrypted) | 2 d | — |
| P1 | MCP tool-call spans + allow-list | 2 d | description pinning |
| P1 | Command palette | 0.5 d | — |
| P1 | Two-pane waterfall + ruler + sync + critical path | 1 d | — |
| P1 | Flame graph view | 1 d | waterfall |
| P1 | Live tail + sparklines + relative time | 1 d | API series field |
| P1 | Saved views + URL filters with counts | 1 d | — |
| P1 | Contrast-safe token + focus rings + reduced motion + light theme | 0.5 d | — |
| P2 | Code-level items §6 (1–15) | 7 d | — |
| P2 | `tower init` + distribution (brew/winget/GHCR) | 1.5 d | — |
| P2 | Open WebUI recipe | 1 d | — |
| P2 | Promptfoo import/export | 1 d | — |
| P2 | Multi-replica correctness | 1 d | — |
| P2 | Migration guides + docs site | 1.5 d | — |
| P3 | `/metrics` auth, console token, MCP pinning | 2 d | — |

**Total to close the credibility + UX gap: ~12 days (P0+P1 highest-impact).**

---

## 11. Non-goals (do not propose)

Carried over from `docs/RULES.md` §1 and `AUDIT-2026-11.md` §8:
hosted/multi-tenant product, vector-DB bake-off, provider-count race, deep
learning in Go / ML platform, online learning from user feedback, semantic
entropy on a 4 GB card, MCP proxying without description pinning, learned
router before ceiling metrics exist, semantic cache, prompt registry UI,
RBAC/SSO, Kubernetes operator.

---

## 12. Sources

- `docs/MARKET.md` (Sep + Nov 2026 revisions), `docs/AUDIT-2026-11.md`,
  `docs/REVIEW-2026-11.md`, `docs/RESEARCH-2026-11-next-steps.md`,
  `docs/tower-enhancements.html`, `docs/tower-design-system.html`.
- Langfuse: PR #14572 (two-pane waterfall), issue #16219 (flame graph),
  blog "simplify-langfuse-for-scale" (2026-03-10), best-practices docs.
- OpenObserve issue #11348 (global command palette).
- Databricks MLflow docs: custom trace views, saved views.
- Masfazulfiqar: "Why most SaaS dashboards stop working past the demo".
- Papers: *Reliability without Validity* (arXiv 2606.19544), *All Verdicts
  are Not Equal* (arXiv 2610.12083), *Unsolvability Ceiling* (arXiv 2605.07395),
  *Co-Failure Ceiling* (HF 2606.27288), τ-bench (Yao et al. 2024), *The Tail
  at Scale* (Dean & Barroso, CACM 2013).
- Security: OWASP ASI01/ASI02, CVE-2025-54136, MCP 2026-07-28 spec.
- OTel GenAI semantic conventions (Development, 2026).
- kentik saved-views drawer, Dynatrace keyboard-shortcut planning.

*Research written 2026-11-26. Run `go test -count=1 ./policy/` after any
change; add a policy test per `docs/RULES.md` §14.*
