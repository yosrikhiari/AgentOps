# Tower — Engineering rules

The rules this codebase is built and judged by. Each rule says **what**, **why**, and **how it is enforced** — by a test in `policy/` (runs in CI), by a test elsewhere, by a CI tool, or by review. A rule with no enforcement column is not a rule; it is a wish.

Companion documents: [`README.md`](../README.md) (what it is), [`API.md`](API.md) (every endpoint and env var), [`adr/`](adr/README.md) (why the big decisions), [`VRAM.md`](VRAM.md) (the GPU budget), [`tower-design-system.html`](tower-design-system.html) (the console UI system, §12), [`../PRIVACY.md`](../PRIVACY.md).

---

## 1. Are we targeting the right problem?

**The problem.** A solo developer with one 8 GB GPU wants to put language models behind a product and needs the four things every LLM deployment needs on day two: *which model answers this request*, *what happens when a multi-step agent crashes*, *is the model still telling the truth*, and *what happened to request X*. Commercial stacks (TensorZero, Langfuse, Temporal, LiteLLM) solve these for teams with budgets and clusters; nothing small, local, and fully explainable existed for one box.

**Who it is for.** First: the author, as proof of ML-systems competence that can be explained line by line in an interview. Second: anyone running local models who wants a gateway, evals and traces without a SaaS account.

**What it is not.** Not a hosted product, not multi-tenant, not a vector-database bake-off, not a replacement for vLLM at scale. Those are explicit non-goals (plan §7, "Explicitly not doing").

**The check we re-run at every track.** Before a track starts, three questions in the plan's §9 brief: (1) which of the four day-two questions does this serve? (2) what is the smallest thing that closes it *and can be demoed*? (3) what would we cut first if it slips a week? A track that cannot answer (1) is backlog, not work. — *Enforced by review: every §9 track has a Brief line naming the question it serves.*

## 2. What we optimize for (in this order)

1. **Correctness you can prove.** A wrong metric is worse than no metric (the Prometheus histogram was double-cumulated and would have shown a fictional p99; recall exceeded 1.0). Every fix ships with a test that fails on the old code — *bind-checked*, by reverting the fix in a scratch copy.
2. **Explainability.** One `if` you can explain beats a classifier you can't demo. Every routing decision carries a `reason`; every span says why.
3. **Operability.** One binary, one Postgres, one Ollama, one `docker compose up`. No component you cannot restart with one command.
4. **Honesty of state.** Failed runs write nothing; stale data is pruned; docs say what shipped, not what was planned. The plan records every defect found, including the ones found in the docs.
5. **Router overhead, then throughput, then features.** In that order, and only after 1–4.

*Enforced by:* the bind-check rule (review, recorded per fix in the plan §8); `TestPolicyPromptsNeverReachSpans`, `TestPolicyOneErrorShape`, `TestPolicyEnvVarsDocumented`, `TestPolicyFlagsDocumented` (docs must match code).

## 3. Measures of success

| Level | Measure | Target | Where measured |
|---|---|---|---|
| MVP | The six checkboxes in plan §1, each closed by a *live* run, not "code exists" | 6/6 (5 closed; #1 waits on a model download, #3 on a recording) | plan §1.1 gate table |
| Quality | Faithfulness of the frozen golden set | ≥ 0.95 on every run; alert below `EVAL_THRESHOLD` (0.7) | `eval_runs`, console Evals page, `eval_faithfulness` gauge |
| Quality | Scorer discriminates | 1.00 on correct answers, 0.00 on corrupted ones | recorded live check (plan §8 5th pass) |
| Latency | Router overhead (`/health`, no model) | p50 < 5 ms, p99 < 100 ms at 50 RPS | k6 gate `load-tests/router.js` |
| Reliability | Durable resume | a killed step resumes with no repeated side effect | `TestKillResume` + live kill |
| Privacy | Prompts persisted by the gateway | zero | `TestPolicyPromptsNeverReachSpans` |
| Delivery | CI green on `main`; release built from a tag | always | GitHub Actions |
| CV | Every line explainable | 33 lessons with interview Q&A; every decision an ADR | `lessons/`, `docs/adr/` |

A number that cannot be produced by a command or a test is not a success measure here.

## 4. Architecture

**Style: a modular monolith with a synchronous request core and asynchronous, bounded edges. Not event-driven.**

- **Synchronous core.** `POST /v1/chat/completions` does exactly what the caller must wait for: authenticate → parse → plan → call the backend → respond. Nothing else. (`router/server.go`)
- **Asynchronous edges**, every one bounded and observable:
  - spans → 1024-deep channel → one writer goroutine; full queue drops and counts; flushed on shutdown (`main.go spanWriter`);
  - API-key usage charge → fire-and-forget with a 5 s timeout, logged on failure;
  - eval runs → background, single-flight (second request gets 409), bounded by `EVAL_TIMEOUT`;
  - workflows → background goroutine per workflow, state in Postgres rows, resumable;
  - health probes → one goroutine, 15 s ticker, cancelled on shutdown.
- **Durability by row state, not by events.** Workflow progress is `pending → running → done` rows with `(workflow_id, seq)` as idempotency key (ADR-0003). Resume = re-run the same id.
- **Why not event-driven.** One process, one box, no second consumer. A broker would add a component to run, a delivery semantic to reason about, and nothing the rows don't already give us. If a second worker ever exists (Track D), the first step is a `SELECT … FOR UPDATE SKIP LOCKED` lease on the rows, still not a broker.
- **Packages talk to the database through tiny interfaces** (`Execer/Queryer/Rows`) and never import the driver; `main.go` alone adapts pgx (ADR-0001).

*Enforced by:* `TestPolicySpanSinkIsNonBlocking` (the sink must `select … default`), `TestEvalRunSingleFlight`, `TestKillResume`, `TestPolicySingleDirectDependency`.

## 5. Sync vs async — the rule

**Synchronous if and only if the caller cannot get its answer without it.** Everything else is asynchronous, and every asynchronous thing must satisfy all four:

1. **Bounded** — a buffer size, a timeout, or a single-flight guard. Never an unbounded goroutine fan-out.
2. **Non-blocking for the request** — a full buffer drops (and counts), it never waits.
3. **Observable** — a counter, a log line on first failure, or a status endpoint (`/v1/evals/status`).
4. **Safe to lose or repeat** — dropped spans lose telemetry, not correctness; a re-run workflow skips done steps; a re-sent usage charge is at most one request of budget.

Streaming is the one place the request path is long-lived: the connection stays open, but each delta is written as it arrives and the metrics are counted once at the end.

## 6. Retry rules

| Where | Policy | Why | Enforced |
|---|---|---|---|
| Judge calls (Ollama, Groq) | Exponential backoff with jitter, `attempts` 3–4; **429 waits for `Retry-After`; other 4xx never retried**; respects context cancellation | 5xx/timeouts are transient; 4xx means *our* request is wrong | `TestWithRetryNeverRetries4xx` |
| Router → backend | **No retry of the same backend.** On failure move to the next candidate in the plan (other local tier, then cloud if allowed); known-down backends skipped | Retrying a model call doubles GPU time for the same likely outcome; a fallback answers | `TestPolicyRouterDoesNotRetrySameBackend`, `TestFallsBackToCloudWhenLocalFails` |
| Span writes | None. Drop + count, first failure logged once | Telemetry must never cost the request | `TestPolicySpanSinkIsNonBlocking` |
| Usage charge | None (one attempt, 5 s timeout) | Budget precision of one request is acceptable | review |
| Console → API | One retry on 5xx after 400 ms, then inline error + Retry button | Transient store hiccups; the human decides after that | browser check (plan §9 C) |
| Ingest / migrate | None; both are idempotent and re-runnable by hand | Full sync on ingest; `schema_migrations` on migrate | `TestIngestPrunesStaleChunks`, `TestMigrateSkipsApplied` |
| Workflow steps | No automatic retry; `attempts` is counted; resume is explicit (CLI or console) | A human decides to resume; the count is the audit trail | `TestKillResume` |

Precondition for any retry: the operation is idempotent. Span inserts are `ON CONFLICT DO NOTHING`; steps are keyed; eval runs are one transaction.

## 7. Latency rules

- **Router overhead budget:** p50 < 5 ms, p99 < 100 ms on `/health` at 50 RPS (k6 gate; measured 1.47 ms / 70 ms). Anything added to the request path must not move these.
- **Per-request deadline:** `REQUEST_TIMEOUT` (default 300 s) wraps the backend call; expiry → `504 timeout`, never a hung connection. `ReadHeaderTimeout` 10 s.
- **Model time is not our latency, but we show it:** `latency_s` on every `model.generate` span; p50/p99 in the console; the histogram in Prometheus.
- **Console endpoints:** each query has a 10 s context; pages render skeletons first, data second.
- **Eval runs:** bounded by `EVAL_TIMEOUT` (90 min); on a shared GPU expect 3–4× the idle time (`VRAM.md`).

## 8. Scalability rules — and their honest limits

- **Vertical first.** One process serves one GPU; Ollama serializes model calls anyway. Concurrency inside the process is goroutine-per-request (stdlib), bounded by the single writer for spans.
- **What scales today:** requests on `/health`, `/metrics`, `/v1/models`, the console (all pool-backed, stateless); readers of spans/evals.
- **What does not, by design (documented, not hidden):** the API-key rate limiter is per-process; the eval single-flight is per-process; two resumers of one workflow id would both run the pending step. A second replica requires: a shared limiter (Postgres or Redis counter), a `FOR UPDATE SKIP LOCKED` lease on steps, and a shared eval lock. That is Track D/E work and is listed in `README.md` → *Known limits*.
- **Data growth:** `spans` (30 d), `route_decisions` (90 d) and `audit_log` (365 d) are swept at boot and daily (`SPAN_KEEP` / `DECISION_KEEP` / `AUDIT_KEEP`, each clamped to at least 1 h); `gpu_samples` keeps `GPU_HISTORY_KEEP` (168 h) and idle conversations 90 days. Indexes exist on `spans(started_at)`, `spans(trace_id)` and `spans(name, started_at)`.

## 9. Security requirements

STRIDE-lite, each threat with the control and where it is enforced.

| Threat | Control | Enforced |
|---|---|---|
| Spoofing (who is calling) | Virtual API keys, Bearer auth, `REQUIRE_API_KEY` to reject anonymous | `TestAPIKeyRequired` |
| Tampering with keys at rest | Only SHA-256 of the secret stored; secret printed once | `router/keys.go`, review |
| Repudiation | `X-Trace-ID` on every response; every request has spans; key name on per-key counters | `TestHandleChatRoutesFast` (header), spans tests |
| Information disclosure | Prompts never in spans; read paths redact `prompt/input/text/output`; **sensitive requests never reach a cloud backend** | `TestPolicyPromptsNeverReachSpans`, `TestPolicyMCPToolSpansRedacted`, `TestListSpansRedacted`, `TestSensitiveNeverLeavesBox`, `TestPolicySensitiveHasNoCloudCandidate` |
| Denial of service | Per-key RPM + token budget; `REQUEST_TIMEOUT`; `ReadHeaderTimeout`; bounded span queue; eval single-flight; 4 MiB body cap; `ReadTimeout`/`WriteTimeout`/`IdleTimeout`; client `options` bounded (`num_ctx` ≤ 32768, `num_gpu` ≤ 99, `keep_alive` ≤ 1h) | `TestAPIKeyRateLimit`, `TestAPIKeyBudget`, `TestEvalRunSingleFlight` |
| Elevation of privilege | Container runs as `nonroot` on distroless; the console — the only admin surface, and the one that returns prompts — refuses non-loopback peers unless `CONSOLE_REMOTE=true`; key creation is CLI-only on the box | `TestPolicyConsoleIsLocalOnly`, `TestConsoleGuardKeepsRemoteClientsOut`, `Dockerfile` |
| Supply chain | One direct dependency; Dependabot weekly; `govulncheck` in CI (found and fixed GO-2026-5970 in `x/text` on first run); `staticcheck` in CI; release binaries with `SHA256SUMS` | `TestPolicySingleDirectDependency`, `ci.yml` policy job |
| Secrets | Env vars only (`GROQ_API_KEY`, `POSTGRES_DSN`); never logged; `.env` git-ignored | review; `.gitignore` |

Known gaps, stated: `/metrics` is unauthenticated (routing metadata and per-key counters, never prompts). The console is loopback-only by default but returns stored prompts to that loopback client, so `CONSOLE_REMOTE=true` is a deliberate opt-out — put an authenticating proxy in front when you use it. A threat model beyond this table is backlog (A7).

## 10. Guardrails (runtime)

| Guardrail | Behaviour | Test |
|---|---|---|
| Fail-closed routing | Sensitive → every non-local candidate removed, primary included; Ollama `:cloud` models are non-local; explicit non-local model → 403 | `TestSensitiveNeverLeavesBox`, `TestCloudSuffixModelIsNotLocal`, `TestPolicySensitiveHasNoCloudCandidate` |
| Unknown model | 400 `unknown_model`, never a silent fallback to "auto" | `TestExplicitModel` |
| Not on disk | explicit or tier model missing from Ollama → 503 `model_not_pulled` with the pull hint; `/v1/models` shows `up:false` + `reason:not_pulled`; presence refreshed every probe tick, gaps logged | `TestPresenceSkipsUnpulled`, `TestPolicyModelNotPulledIsOneErrorShape` |
| Backend health | Known-down fallbacks skipped, not tried | `TestHealthProberGaugeAndModels` |
| Budgets and rates | 403 `budget_exceeded`, 429 `rate_limited` + `Retry-After`; `/mcp` uses the same gate and charges the tokens a tool consumed | `TestAPIKeyBudget`, `TestAPIKeyRateLimit`, `TestToolCallReportsUsage` |
| Body size | > 4 MiB → 413 `request_too_large` on both chat ingresses | `TestChatRejectsOversizedBody` |
| Resource options | `num_ctx`/`num_gpu`/`num_predict`/`num_thread`/`repeat_last_n` above their ceiling, or `keep_alive` negative/over 1h → 400 `option_out_of_range` | `TestParamsRejectResourceOptionsOutOfRange`, `TestChatRejectsUnboundedOption` |
| One eval at a time | 409 `eval_running` | `TestEvalRunSingleFlight` |
| Golden hygiene | Freeze rejects empty fields, unknown docs, duplicate questions, zero-claim and subordinate-clause answers | `TestValidateGolden` |
| Questions about the gateway | `which model` / `why this model` / `how does routing work` (and a short follow-up to one) is answered by the router from the last model turn and the roster, with `answered_by:"router"` and no model call; the trace is `route.decide → router.answer → router.respond` | `TestRouterGoldenMetaQuestions`, `TestGatewayQuestionTraceShape`, `TestGatewayFollowUpNeedsTheThread` |
| System prompt | Built server-side on every request: roster with tier and locality, the routing rule, and this turn's model and reason. Rebuilt, never stored, never spanned; sensitivity is decided on the client's messages before injection | `TestSystemPromptNamesRosterAndRoute`, `TestSystemPromptDoesNotChangeSensitivity` |
| Trace shape | Every chat trace has three spans; the failure path emits `router.respond` carrying the error, and `router.answer` replaces `model.generate` when no model ran | `TestFailurePathEmitsRespond`, `TestChatEmitsThreeSpans` |
| Judge determinism | Temperature 0 on every local judge call; judge model + prompt version stored per run; `judge_changed` flag in drift | `TestOllamaJudgeSendsTemperatureZero`, `TestRecordAndDriftReport` |
| Atomic eval runs | Header + pair rows in one transaction; a failed run writes nothing | `scoreOnce` (review) — observed live 2026-09-15 |
| Stale corpus | Ingest prunes chunks the cleaner no longer emits | `TestIngestPrunesStaleChunks` |
| Graceful drain | SIGTERM: stop accepting, finish in-flight chats, flush spans | observed live (`docker stop` mid-chat) |

## 11. Testing and experiments

**Test pyramid, as practised:**
1. **Unit tests with fakes at the I/O boundary** — `Backend`, `Store`, `Judge`, `KeyStore` all have in-memory fakes; the whole gateway is testable offline in ~2 s per package.
2. **Wire-format tests** — real protocol bytes replayed through `httptest` (Ollama NDJSON, OpenAI SSE with Groq usage, Prometheus exposition text).
3. **Policy tests** (`policy/`) — the rules in this document that can be checked mechanically.
4. **Live verification, recorded** — every §9 ticket has a "Verified:" line naming the command, the box and the numbers; three MVP checkboxes were closed only by live runs. A ticket without a recorded live run is "code done", not done.
5. **Load** — k6 gate on `/health`; **browser** — every console page exercised in the pane, including the Postgres-down path.

**Rules for tests.** Every fix ships with a test that fails on the old code (bind-check by reverting in a scratch copy). Tests never need Ollama or Postgres. `-race` in CI. A test that passes with the fix reverted is deleted or fixed.

**Rules for experiments (evals).**
- Change **one variable per run**: corpus, golden, judge, prompt version, or chunking — never two.
- Any corpus change bumps the golden version; the frozen hash is the identity of the experiment. Never edit a frozen file — freeze `vN+1`.
- Golden answers are standalone main-clause propositions (learned: v1 0.969 → v2 1.000 came entirely from answer form, not from the system).
- Judge at temperature 0; record judge model and prompt version with every run; treat `judge_changed: true` as "not comparable".
- Prove a metric discriminates before trusting it (correct answers 1.00 vs corrupted 0.00).
- Run evals on a quiet GPU or accept 3–4× runtime (`VRAM.md`); a run killed by the deadline writes nothing.
- Write the result into the plan the same day, including the failures.

## 12. Console UI — the Tower design system

The console (`console/static/`) is built on **Tower**: hand-written CSS, `--tower-*` tokens, a `tower-*` class per component, a 20-line `h()` DOM helper, no framework, no chart library. The living reference is [`tower-design-system.html`](tower-design-system.html) — it links the shipped `tower.css`, so what it shows is what runs. Read it before adding or changing anything the console renders. The backlog of agreed improvements, each with a demo, is [`tower-enhancements.html`](tower-enhancements.html).

- **Tokens only.** Every colour is a `--tower-*` custom property declared once in `:root` of `tower.css`. No hex literal in `app.js`, `index.html`, or anywhere else in the stylesheet. A new colour is a new token with a name and a reason. — `TestPolicyConsoleColoursAreTokens`
- **A component is a `tower-*` class in `tower.css`**, placed in the matching `/* ===== */` block, and it exists before it is used. A class used by the console that the stylesheet does not define is a build failure, not a visual bug found later. — `TestPolicyConsoleClassesExist`
- **Reuse before inventing.** Panel, metric row, table, pill, chip, chip-button, input, form-row, empty (inline and tall), error box with Retry, skeleton, toast, note, key-value grid, waterfall, span tree. Check the design-system page's spec tables; if the component is there, use it. — review
- **State is a pill, a fact is a chip.** `pill(state, label)` only for healthy / degraded / critical. `chip(text)` for tier, backend, reason, model, kind. A chip is never coloured. — review
- **One accent.** Amber marks the thing to look at: active nav, the page's one primary button, the quality-tier bar, the selected trace. Nothing decorative is amber. — review
- **Every page renders four states**: skeleton → live, plus empty (tells the operator the next action) and error (with Retry). A page that renders only the live state is not done. — review
- **DOM is built with `h()`**, children are text or nodes; there is no `html:` attribute and no `innerHTML` anywhere. Numbers that change are `.tower-mono`; ids link to `#/traces/<id>`. — review (SonarCloud flags `innerHTML`)
- **Add the example to the design-system page** with the function it is used in, in the same change. The page is the contract; a component that is not on it does not exist.

## 13. Static analysis (CI, every push)

| Tool | What it catches | Job |
|---|---|---|
| `gofmt -l` | formatting drift | `go` |
| `go vet` | suspicious constructs, struct tags, printf misuse | `go` |
| `go test -race` | data races across the goroutine edges | `go` |
| `staticcheck` (pinned v0.8.1) | dead code, misuse, simplifications | `policy` |
| `govulncheck` | known vulnerabilities reachable from our code | `policy` |
| `docker build` | the image still builds from a clean checkout | `docker` |

Local: `gofmt -l . && go vet ./... && go test -race ./... && go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./... && go run golang.org/x/vuln/cmd/govulncheck@latest ./...`.

**Pipeline hygiene (so merges are boring).**
- `concurrency: ci-${{ github.ref }}` with `cancel-in-progress` — a new push or a Dependabot rebase cancels the stale run for that ref instead of queueing behind it.
- Every job has `timeout-minutes`; `permissions: contents: read` at the top, `write` only on `release`.
- `GOTOOLCHAIN=local` — CI never silently downloads a different Go than `go.mod` + `setup-go` selected.
- Dependabot updates are **grouped per ecosystem** (one PR a week for Go modules, one for Docker images, one for Actions). Six single-dependency PRs that all edit `ci.yml` conflict with each other after the first merge; one grouped PR does not.
- A job that says *"failed to be acquired"* never ran — that is the GitHub runner pool, not the code. Re-run the job; do not "fix" anything.
- `govulncheck@latest` deliberately tracks the live vulnerability database: a new CVE in a dependency turns `main` red on the next push. That is the intended signal — bump the module (as with `golang.org/x/text`, GO-2026-5970), do not pin the scanner to hide it.
- SonarCloud runs as an app-side automatic analysis (no workflow file); it is advisory here and is not a required check.

## 14. Executable policies (`policy/policy_test.go`)

These fail the build when a rule above is broken. Names are the rules.

| Test | Rule |
|---|---|
| `TestPolicySingleDirectDependency` | `go.mod` has exactly one direct dependency (`pgx`) — the independence rule |
| `TestPolicyPromptsNeverReachSpans` | a canary in the prompt and in the model's answer appears in no span attribute; `RedactAttrs` drops the four sensitive keys |
| `TestPolicyMCPToolSpansRedacted` | an echoing model behind `route_test_request` still leaves no prompt text in any `mcp.tool` span; tool spans carry `tool`/`ok`/`code` only |
| `TestPolicyConversationsPrivate` | messages die with their thread (ON DELETE CASCADE), idle threads purge at 90 days, and the prompt-storage exception is disclosed in PRIVACY.md |
| `TestPolicySensitiveHasNoCloudCandidate` | `Plan()` never yields a non-local candidate for a sensitive request; explicit cloud is refused |
| `TestPolicyOneErrorShape` | 400/401/502 from the gateway all carry `{error:{code,message,trace_id}}` |
| `TestPolicyModelNotPulledIsOneErrorShape` | 503 `model_not_pulled` carries the one error shape; the missing model is never attempted |
| `TestPolicyAgentConfigClean` | shipped/executed config files carry no secret shapes, URL credentials, pipe-to-shell, chmod 777, or disabled host-key checks (docs/lessons/tests out of scope by construction — no allow-list) |
| `TestPolicySpanSinkIsNonBlocking` | the span sink is a `select` with a `default` branch |
| `TestPolicyEnvVarsDocumented` | every `os.Getenv("X")` in the code is documented in `docs/API.md` |
| `TestPolicyFlagsDocumented` | every CLI flag in `main.go` appears in `README.md` |
| `TestPolicyMigrationsContiguous` | `migrations/NNNN_*.sql` numbered contiguously from 0001 |
| `TestPolicyADRsIndexed` | every ADR file is in `docs/adr/README.md`; every `ADR-NNNN` cited in the README exists |
| `TestPolicyRouterDoesNotRetrySameBackend` | a failing backend is called once per candidate, never retried |
| `TestPolicyRulesCiteRealTests` | every test name cited in this document exists — an enforcement column cannot go stale |
| `TestPolicyConsoleColoursAreTokens` | no hex colour in `app.js` / `index.html`, none in `tower.css` outside `:root` — the console uses `--tower-*` tokens |
| `TestPolicyConsoleClassesExist` | every `tower-*` name the console references is defined in `tower.css` |
| `TestPolicyAgentFilesPointHere` | `AGENTS.md` cites this document and the design system; every per-tool agent file that exists (Copilot is tracked; `CLAUDE.md`, `GEMINI.md`, `opencode.json`, Cursor are local and git-ignored) points at `AGENTS.md` and carries no rules of its own |
| `TestPolicyConsoleIsLocalOnly` | the console (prompt history, eval runs, workflows) is mounted behind `consoleGuard`, its routes use the guarded handler, the browser defences are wired around the mux, and the default `ADDR` is `127.0.0.1:8080` |

Adding a rule: write the test first, watch it fail on the current code or a scratch breakage, then add the row here. A rule that cannot be tested gets the word **review** in its enforcement column above and a reviewer, not a wish.
