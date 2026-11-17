# What to build next: the evidence from the November 2026 sweep

*Research pass, 2026-11-17. Companion to `docs/RESEARCH-intent-routing.md` (routing literature)
and `docs/MARKET.md` §9 (market). That pass asked "which router should we build"; this one asks
a narrower question after an end-to-end test: **what does the evidence say to build next?**
Every claim below is either a reading from this machine or a source in §6.*

## 0. The short version

The machinery passes end to end. The **number** does not exist, and the **judge** is the reason.

- 27/27 API checks, the auth matrix, MCP dual-era over HTTP and stdio, twelve migrations on an
  empty database, both release-gate branches, seven console routes with no page error, and four
  GPU states all pass (§1).
- The only v3 runs in the database are **72/72 unjudged** — the corpus was empty when they ran —
  so the project has **no valid v3 measurement** at all. The only judged runs are 2-pair probes.
- In one of those probes a **faithful paraphrase scored 0.00**. The detector is doing what it was
  built to do (fail closed when it cannot confirm), but with one weak judge we cannot tell
  "unsupported" from "judge could not follow it" (§2.4).
- Routing is **blocked on the roster, not the classifier**: this box has a 4 GB card and one chat
  model, so there is nothing to route. The 2026 literature says the headroom everyone reports is
  inflated anyway (§2.1–2.3).

So the order that follows from evidence is: **make the measurement trustworthy → make the traces
portable → grade what agents actually do → route only when a roster exists.**

## 1. What the sweep proved, with the evidence

| Area | Check | Evidence |
|---|---|---|
| HTTP | 27/27 API checks | OpenAI chat 200 on `phi4-mini:3.8b`, Anthropic `message` + streaming event order, tools rejected 400, unknown route 404 JSON |
| Auth | 5 states | no key 401 `missing_api_key`, bad key 401 `invalid_api_key`, good key 200, third request in a minute 429 with `Retry-After: 52`, `x-api-key` 200 |
| MCP | dual-era | `server/discover` lists 5 versions, legacy `initialize` echoes 2024-11-05, unknown version `-32022`, stdio tools/list identical |
| Schema | from scratch | 12 migrations, 14 tables, `gpu_samples.throttled` nullable |
| Evals | both branches | a 2-pair golden at 0.500 → gate exit 1 naming run 4; a 1-pair golden at 1.000 → gate exit 0, "1/1 green" |
| GPU | four states | live 720 samples; skeleton 6 nodes; error with Retry; unavailable-only on a box with no driver |
| Console | seven routes | #/overview, #/traces, #/cockpit, #/evals, #/gpu, #/benchmarks, #/workflows — zero page errors |
| CLI | five modes | `--version`/`--version-json`, `--advise` measured *and* docs-fallback, `--route-replay`, `--score`, `--drift` |

Two defects were found and fixed in the same pass (commit *"Answer unknown routes in the one error
shape, and say when local is down"*): unmatched paths and wrong verbs fell through to `net/http`'s
plain-text 404/405, and a local backend that was down from boot failed over to the cloud with no
line at boot. The second one is why the sweep is worth running: `localhost` resolves to `::1` on
Windows while Ollama binds `127.0.0.1`, so "local-first" was silently not local.

Environment caveats, because they limit what "pass" means: Docker's engine dropped six times and
Ollama exited three times during the sweep (restarted each time); the fallback path was exercised
through the stub backend, not a real cloud provider; signing/attestations still need GitHub OIDC.

## 2. What this research pass adds

### 2.1 Reported routing headroom is inflated by evaluation artifacts

[Unsolvability Ceiling in Multi-LLM Routing](https://arxiv.org/abs/2605.07395) (2026) studies 206k
query-model pairs across six benchmarks and finds that a large share of the "no model can solve
this" mass is an artifact of **judge verbosity bias, truncation under fixed generation budgets, and
output-format mismatches**. When they control for those, standard routers **collapse to
majority-class prediction** — the smallest tier is optimal for ~79% of queries — and the cost is
13–17 percentage points of opportunity cost. Their fixes are concrete: **dual-judge validation,
exact-match anchoring, cost-sensitive objectives.**

This is the same failure our probe hit from the other side: a correct paraphrase scored 0.00. Before
any router work, our replay numbers need the same treatment.

### 2.2 There is a hard ceiling no router can beat: the co-failure rate

[Co-Failure Ceiling](https://huggingface.co/papers/2606.27288) (2026, 67 frontier models) proves
that for any policy whose output is one member answer, accuracy ≤ **1 − β**, where β is the rate at
which *every* candidate is wrong on the same query. Average pairwise error correlation cannot
identify β, which is why benchmarks keep reporting gains that do not reproduce.

Implication here: β is computable from our own store once two models have outcomes on the same
golden questions, and it is the **only honest upper bound** on what routing can buy. Report it
before promising any routing gain, and report "undefined — one model" rather than a number today.

### 2.3 Residency-aware routing is not novel; it is the model-selection form of cache-aware routing

`docs/RESEARCH-intent-routing.md` says "no paper in this space models that cost". That claim is too
strong and should be corrected. The serving literature models the same trade-off for *replicas*:

- [CacheRoute](https://www.alphaxiv.org/abs/2608.19677) (2026): prefix-affinity routing reached a
  93.2% KV-cache hit rate vs 64.1%, cutting p99 from 3.8–8.5 s to 1.8 s at 100 QPS. Critically, it
  **loses to a plain load balancer when prefix reuse is only 1–10%** — and the authors insist on a
  **shadow replay** before switching a fleet.
- [GORGO](https://arxiv.org/abs/2602.11688) and Ray's [prefix-aware routing](https://docs.ray.io/en/latest/serve/llm/user-guides/prefix-aware-routing.html):
  the decision is "which replica already holds the cache" vs "which replica has capacity".
- [A Year in LLM Serving](https://systems.seas.harvard.edu/blog/a-year-in-llm-serving): round-robin,
  sticky and cache-aware routing each win under different reuse rates; the metric that matters is
  latency per completed interaction, not hit rate.

Our swap term is the single-GPU, single-tenant version of exactly this. Two things follow: we can
borrow a **well-tested experimental design** (replay the policy offline, gate on shadow outcomes,
expect it to lose when reuse is low), and we should stop claiming the idea is unprecedented — the
claim that survives is narrower and better: *nobody applies it to choosing between models on one
consumer card, with the eviction counted in wall-clock seconds.*

### 2.4 Judge validity, not judge choice, is the binding constraint on every number here

- [Reliability without Validity](https://arxiv.org/abs/2606.19544) measured 21 judges / ~541k
  judgments; **Qwen3-8B — our production judge — has the highest position bias (0.192)**.
- [All Verdicts are Not Equal](https://arxiv.org/abs/2610.12083): a deterministic judge can be
  consistently wrong; report a trustworthy-verdict rate, not just agreement.
- RAG-style faithfulness (what we implement) is the best simple signal for grounded QA, but
  [RAGAS-style pipelines return *no* result on 58.9% of DROP and 83.5% of FinanceBench](https://cleanlab.ai/blog/rag-tlm-hallucination-benchmarking) —
  the same failure our "unjudged" fix addressed — and [decomposing answers into
  entailed/contradicted/baseless claims](https://arxiv.org/html/2603.27752v1) is the current
  increment over answer-level scores.

We already have the right shape: claim-level, fail-closed, `retrieval_miss` kept separate from
faithfulness. What is missing is **validity evidence**: a small human-labelled set to measure the
detector's precision/recall, and a second judge to measure agreement. Semantic entropy is not the
answer here — it needs several generations per query on a 4 GB card.

### 2.5 Traces are becoming portable, and trajectory grading is the new frontier

- The GenAI semantic conventions moved out of the core repo into
  [`semantic-conventions-genai`](https://github.com/open-telemetry/semantic-conventions-genai) on
  12 June 2026, and **every `gen_ai.*` attribute is still `Development`** (core chat attributes are
  the settled part; agent/tool semantics are not). Content capture is explicitly opt-in. So: export
  OTLP, but **dual-name and pin the semconv version** rather than betting a dashboard on one name.
- Agent evaluation moved to **outcome-state grading** ([τ-bench](https://taubench.com) compares the
  final database state, not the transcript) plus **trajectory metrics** — steps, tool-call count,
  token cost — and **pass^k** for reliability across repeats. Our eval harness scores single-shot
  RAG answers; the workflow runner has spans but no grading at all. That is the largest uncovered
  surface in the product *and* the thing observability vendors are weakest at.

### 2.6 MCP is a supply-chain surface now

Tool poisoning and rug pulls ([CVE-2025-54136](https://www.truefoundry.com/blog/blog-mcp-tool-poisoning-gateway-defense),
[OWASP ASI01/ASI02](https://labs.cloudsecurityalliance.org/agentic/agentic-mcp-security-best-practices-v1))
come through **tool descriptions**, not user text: the model treats metadata as authored by the
developer. We are an MCP *server* today, so the exposure is limited. The moment we proxy or consume
third-party MCP tools, we need description pinning by hash and a provenance record — and our
fail-closed policy must treat a tool result as untrusted input, not as context.

### 2.7 The name collides, confirmed independently

`agentops.ai` is a funded agent-observability product (free 5k events, Pro from $40/mo, MIT SDK,
self-host enterprise) and it owns the search term. `docs/MARKET.md` §9.1 already flagged it; the
research confirms it is not a small project. This is a positioning decision, not code: keep the
name with a qualifier, rename the product to Tower, or pick a new one — before any public launch.

## 3. The blocker, in numbers

| Fact | Reading | Consequence |
|---|---|---|
| GPU | GTX 1650, **4 GB** installed, 4 GB reported by the driver | 7B-Q4 (~4.5 GB) does not fit; no 7B/8B judge locally |
| Models pulled | `phi4-mini:3.8b`, `nomic-embed-text` | one chat model → no roster → nothing to route |
| v3 golden set | **72 pairs** | real size, but never judged |
| Corpus | **17 docs / 36 chunks** | retrieval ceiling is low; 72 questions over 36 chunks |
| v3 runs | 2, both **0/72 judged**, score 0 (now excluded from drift/gate) | the headline number is missing, not low |
| Judged runs | 2-pair probes only (0.5, 1.0) | no statistical basis for any claim |

The routing plan assumes an 8 GB box and a 3B/7B/8B roster (`docs/VRAM.md`). That machine is not
the one this code was tested on, so routing experiments cannot run here without thrashing or
lying.

## 4. Recommended order

| # | Work | Why now | Effort | Kill criterion |
|---|---|---|---|---|
| 1 | **Judge validity pack**: second judge (phi4-mini + any 7B/8B when available), agreement (κ) and AB/BA replicate on a 50-pair human-labelled gold set; report precision/recall of "refuted" | Every eval, drift alert and release gate currently rests on one weak judge; a faithful paraphrase scoring 0.00 is the symptom | 2–3 days | if κ < 0.4, stop tuning the judge and label more data |
| 2 | **A real v3 baseline + ceiling metrics**: score all 72 pairs with the corpus ingested; add `unsolvable` (retrieval miss or all claims refuted) and `smallest-tier-optimal` shares to the replay report | The project has no valid v3 number; the literature says headroom is an artifact until these are reported | 1–2 days | if ≥80% of questions are smallest-tier-optimal, routing is not the next feature |
| 3 | **3-way claim labels** (entailed / contradicted / baseless) with the evidence span | Turns "0.00" into an explanation and matches the current claim-level state of the art | 1–2 days | if the 3-way split does not change any verdict on the gold set, keep binary |
| 4 | **OTLP export, dual-named and pinned** (`gen_ai.*` + our names; prompts never exported) | Traces become usable in Langfuse/Phoenix/Datadog — distribution, not features; semconv is still Development, so pin it | 2 days | if the pinned mapping needs rework twice, wait for RC |
| 5 | **Trajectory eval for workflows**: per-step pass/fail, tool-call count, pass^k over repeats | The only uncovered product surface, and what agent-observability buyers are starting to ask for | 3–4 days | if pass^k needs more than 5 repeats to be stable, fix the workflow determinism first |
| 6 | **Routing Phase 1** (replay + four baselines + β), then the residency term | Blocked on a roster; gated by #2 | 1 week on the 8 GB box | β > 30% → the roster is the bottleneck, not the router |
| 7 | **Name decision** | Distribution, and it gets more expensive after launch | 1 hour of deciding | — |

If only one thing gets done: **#1**. The gateway, the traces and the gates are all in place; the
number they produce is not yet defensible, and every later claim inherits that.

## 5. What not to build

- **Semantic entropy** detection: several generations per query is the wrong cost model on a 4 GB
  card, and the gain over claim-level judging is unproven at this scale.
- **A learned router before β and the ceiling metrics**: the literature is explicit that this is how
  inflated headroom gets published.
- **Provider coverage** as a goal: LiteLLM has 100+, Bifrost 1,000+ through 20 providers, and both
  have teams; the early-2026 LiteLLM Rust gateway reports 0.7 ms p99 overhead. We cannot win that
  race and do not need to.
- **MCP tool proxying** without description pinning and an untrusted-tool-result policy.

## 6. Sources

Routing and evaluation: [Unsolvability Ceiling](https://arxiv.org/abs/2605.07395) ·
[Co-Failure Ceiling](https://huggingface.co/papers/2606.27288) ·
[LLMRouterBench](https://aclanthology.org/2026.findings-acl.1881.pdf) ·
[LLMRouter](https://arxiv.org/html/2608.06767v1) ·
[Reliability without Validity](https://arxiv.org/abs/2606.19544) ·
[All Verdicts are Not Equal](https://arxiv.org/abs/2610.12083) ·
[RT4CHART claim-level faithfulness](https://arxiv.org/html/2603.27752v1) ·
[RAG hallucination benchmark](https://cleanlab.ai/blog/rag-tlm-hallucination-benchmarking).

Serving and caching: [CacheRoute](https://www.alphaxiv.org/abs/2608.19677) ·
[GORGO](https://arxiv.org/html/2602.11688v1) ·
[Ray prefix-aware routing](https://docs.ray.io/en/latest/serve/llm/user-guides/prefix-aware-routing.html) ·
[A Year in LLM Serving](https://systems.seas.harvard.edu/blog/a-year-in-llm-serving).

Tracing and agents: [semantic-conventions-genai](https://github.com/open-telemetry/semantic-conventions-genai) ·
[OTel GenAI blog](https://opentelemetry.io/blog/2026/genai-observability) ·
[τ-bench](https://taubench.com) · [trajectory evaluation](https://decagon.ai/glossary/what-is-trajectory-evaluation).

Security and market: [MCP tool poisoning](https://www.truefoundry.com/blog/blog-mcp-tool-poisoning-gateway-defense) ·
[Cloud Security Alliance MCP guide](https://labs.cloudsecurityalliance.org/agentic/agentic-mcp-security-best-practices-v1) ·
[agentops.ai](https://www.agentops.ai) ·
[Bifrost vs LiteLLM](https://www.getmaxim.ai/resources/benchmarks) (vendor-run) ·
[LiteLLM Rust gateway benchmarks](https://docs.litellm.ai/blog/rust-ai-gateway-benchmarks) (vendor-run).
