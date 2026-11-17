# Intent-aware routing: what the literature says, and what to build here

*Research pass, 2026-11-17. Every claim below has a source in §8; the code claims are from this repo.
Companion to `docs/MARKET.md` §9 (Tier 0) and the plan in `docs/plans/`.*

## 0. The short version

**Do not build "classify the intent, then send it to the model that is best at that intent" as the whole
router.** The evidence is unusually clear that intent/domain routing alone underperforms trivial baselines:
the strongest 2026 audit found six commercial routers, and **none beat a router that randomly picks between
two well-chosen models at matched cost** — some by more than ten points. The lesson is not "routing is
useless"; it is that **intent is one feature, not the policy**, and that the roster and the objective matter
more than the classifier.

What is worth building here, in order:

1. **Measure first.** A replay harness over our own traces and eval outcomes, with four baselines any router
   must beat: random-at-matched-cost, always-fast, always-quality, and today's heuristic. No router ships
   before it beats those on our corpus.
2. **Intent as a typed signal, not a decision.** A small closed taxonomy (8 labels below) carried on the span
   and the response, with a confidence. It feeds a policy that also uses difficulty, budget and — our unique
   input — **model residency and swap cost on this card**.
3. **A classifier ladder**, each rung gated by the previous one's measured value: heuristics → embedding kNN
   over labelled traces → small local model (qwen2.5:3b) with a parity gate → learned (query, model) scorer.
4. **A learned scorer beats an intent map once we have outcomes.** The trace/eval store is a free, growing
   training set; that is exactly the TRACER setup (production traces → surrogate → parity gate).

Our differentiator is not a better classifier. It is that we know **which model is resident in VRAM, how long
a swap costs, and what the GPU did during the last eval run** — no paper in this space models that cost. On a
4 GB card, "the model that would answer this best" and "the model that is already loaded and will not thrash"
are different answers, and the second one is often the correct product decision.

## 1. The four paradigms (and where we sit)

The EMNLP 2026 survey organises the field by **what the router does at decision time**:

| Paradigm | What happens | Examples | Serving overhead |
|---|---|---|---|
| **Predictive** | Score candidates before any answer exists; one cost-adjusted choice | classification (RouteLLM, GraphRouter, Hybrid LLM, MoDEM, Tryage), regression (CARROT, IPR), query similarity (EmbedLLM, UniRoute, RadialRouter), recommender (IRT-Router, RADAR) | lowest |
| **Adaptive** | Try a model, observe, then commit/defer/update | cascades (FrugalGPT, AutoMix, SATER, unified routing+cascading), bandits (BaRP, LLM Bandit, MixLLM, LinUCB) | medium (cascade) / low (bandit) |
| **Generative** | An LLM reasons about the decision | Arch-Router (1.5B SFT for domain+action), Router-R1, MasRouter | highest |
| **Infrastructure** | Benchmarks, attacks, serving systems, theory | RouterBench, RouterEval, LLMRouterBench, RouterArena, R2A, RerouteGuard | — |

The user-facing ask ("classify intent → best model") is **predictive classification routing**. The survey's
own conclusion is that classification is the most *published* family and cascades are the most *deployed* —
and that hybrid systems are the norm in practice, not the exception.

## 2. The three findings that should change the design

### 2.1 Intent/domain routing alone is weak, and the objective rewards the wrong thing

*Dynamic LLM Routers are Often Misguided* (2026) evaluates six commercial routers across 14 settings on eight
task categories. None beats random routing between two well-chosen models at matched cost. The authors trace
this to four patterns the standard cost-accuracy objective **rewards**: difficulty blindness, length reversal,
semantic matching, and roster suboptimality. Two implications for us:

- A router that maps "legal question" → "big model" is doing **semantic matching**, one of the named failure
  patterns. It looks smart and routes by topic instead of by difficulty.
- **Roster suboptimality**: with four local models of similar capability, there is little to gain. A
  well-chosen roster leaves little to route. Our roster (3b, 7b-q4, qwen3:8b, phi4-mini, dolphin-mistral) is
  not a set of specialists; it is a set of sizes. The honest gain is in **difficulty and cost**, not domain.

RouterBench reaches a compatible result from the other direction: simple KNN and MLP routers beat individual
models on some suites, but **rarely beat the "Zero router"** (a fixed probabilistic mix calibrated to cost) by
a significant margin, while cascades only win when the answer scorer's error rate is low.

### 2.2 The trace store is the training set; the classifier should learn from it

TRACER (2026) trains a lightweight surrogate on an LLM endpoint's **own production traces** and gates
deployment with a **parity gate**: the surrogate serves traffic only when its agreement with the teacher
exceeds α on a held-out shadow split. On a 77-class intent benchmark it reached 83–100% surrogate coverage
depending on α; on a 150-class benchmark the surrogate fully replaced the teacher; on an NLI task the gate
**correctly refused deployment** because frozen embeddings could not separate the classes.

That is precisely our situation: every chat already writes spans, every eval writes per-pair scores, and the
gateway can serve a classifier locally at zero marginal cost. The parity-gate discipline (never deploy a
classifier below its measured agreement) is the same shape as our existing release gate, and it prevents the
most common failure: a confident classifier that is wrong 30% of the time.

### 2.3 A small local model is a viable front door — with a measured ceiling

A 2026 pre-registered study put a six-label classifier behind three local models: **Qwen2.5-3B reached 0.783
exact-match at 988 ms median, zero marginal cost**; Phi-4-mini 0.518; DeepSeek-V3 0.830 but 2.3 s P95. **No
model met the authors' 0.85 accuracy / 2 s P95 bar.** The classifier is cheap enough to be free and fast
enough to be invisible, but its accuracy is 6–8 points short of the bar they set — which is exactly why it
must be combined with a parity gate and a fallback to the heuristic, not trusted outright.

We already have qwen2.5:3b pulled. On the 1650 a 3B classifier competes with the answer model for 4 GB — which
is the residency problem again, and the reason the classifier must be either (a) run when the card is free,
or (b) replaced by an embedding kNN whose cost is one embedding, not one generation.

## 3. What "best model" should mean here

The literature optimises quality per dollar. We must optimise **quality per second and per swap** on one
consumer card, with a hard sovereignty constraint. Define the routing objective as:

```
choose m ∈ candidates(query)
  maximising   E[quality | query, m, intent]  −  λ_cost · cost(m)  −  λ_lat · latency(m)  −  λ_swap · swap_cost(m)
  subject to   m respects the fail-closed policy (sensitive ⇒ local only)
               resident model preferred when quality ties within ε
```

`swap_cost(m)` is ours alone: from `gpu.Snapshot().Resident` and the `gpu_samples` history we know whether the
model is loaded, how much VRAM it holds, and what a swap did to the last eval run. A 7B that must evict the
resident 3B is not "cheaper at the margin" on a 4 GB card; it costs tens of seconds and makes the current
conversation slower. Papers model token cost; they do not model eviction.

**Intent enters as a prior, not as the decision.** For each intent label we keep an estimated quality vector
over models (from eval outcomes, then from shadow traffic), and the decision is the argmax above. When the
intent is uncertain, its prior is down-weighted toward the roster-wide average — a Bayesian shrink, not a
hard branch.

## 4. Taxonomy

Eight labels, chosen so that they are (a) separable by a 3B model or embeddings, (b) plausibly different in
model strength, and (c) already visible in our traces:

| Intent | Signal we can verify it with |
|---|---|
| `extract` | "list", "pull out", JSON output, short answer from a long input |
| `summarise` | "summarise", "tl;dr", input ≫ output |
| `classify` | "which category", "is this X or Y", single-label output |
| `code` | code blocks, stack traces, "function", "bug", "compile" |
| `math` | digits + operators, "prove", "calculate", word problems |
| `reason` | multi-step, "why", "compare", "trade-off", long deliberation |
| `write` | creative/formatting, "draft", "rewrite", "email", tone instructions |
| `chat` | greetings, short small talk, no task constraint |

Difficulty is a **separate** scalar (length, constraint count, required output structure, retrieval presence),
because §2.1 says difficulty is what actually predicts the strong-model win. Intent without difficulty is the
failure mode we are trying to avoid.

## 5. Classifier ladder (each rung gated by measurement)

| Rung | Method | Cost per request | Data needed | Gate to promote |
|---|---|---|---|---|
| 0 | Current heuristic (`len > 200` or keyword → quality) | ~0 | none | baseline, always available |
| 1 | Heuristic + intent keywords + output-shape detection | ~0 | none | beats rung 0 on replay AIQ/PGR |
| 2 | Embedding kNN over labelled traces (pgvector, nomic-embed-text) | 1 embedding (~10 ms) | ~200 labelled traces | kNN agreement ≥ α on shadow split; beats rung 1 |
| 3 | qwen2.5:3b constrained-JSON classifier with parity gate | one 3B generation (0.5–2 s, competes for VRAM) | ~500 traces | agreement ≥ α and beats rung 2 at matched latency; run only when the card is free |
| 4 | Learned scorer `P(quality win | query, model)` (RouteLLM-style matrix factorisation / KNN over (query, model) outcomes) | ~0 | eval outcomes + shadow labels | beats rung 3 and the four baselines on RouterBench-style replay |

The ladder is deliberately boring: every rung is a **scorer**, and the policy consumes scores. That means the
policy does not change when the classifier does, and the parity gate (TRACER) is one function applied to any
rung.

## 6. Where it plugs into this repo

No new dependency; all tests offline.

- `router/intent.go` — taxonomy, `Intent` type, `Classifier` interface, heuristic + embedding + model
  implementations behind it. The heuristic is always the fallback when a classifier errors or is uncalibrated.
- `router/route.go` — the objective above. `Plan` keeps its current shape; the policy returns candidates with
  scores and a human-readable `reason`, and the span gains structured attributes (`intent`, `intent_conf`,
  `policy`, `candidate_scores`) — labels only, never prompt text (policy test already enforces that).
- `gpu` — expose `Resident()`/`SwapCost(model)` from the existing prober; the router consumes it through a
  small interface so tests inject a fake.
- `migrations/0011_route_decisions.sql` — one row per decision: `trace_id, intent, intent_conf, policy,
  candidates_scored jsonb, chosen, cost_estimate, created_at`. This is the replay corpus and the audit trail.
- `--route-replay` — reads `eval_pair_scores` + `route_decisions`, replays every policy against every baseline,
  reports **AIQ**, **PGR**, **CPT(x%)**, cost and latency per intent. Refuses to promote a policy that does not
  beat the baselines without overlapping confidence intervals.
- `ROUTER_POLICY=heuristic|intent|learned` (default `heuristic`) — the same rollback discipline as the eval
  gate: a policy that regresses is switched off by an env var, not a revert.
- Console — one routing panel: decision mix by intent, chosen model, and the counterfactual ("quality model
  would have been chosen for 12% of traffic; 3 of those were swaps"). This is the explainability the rest of
  the product already promises.

## 7. Experiment plan (what "it works" means)

1. **Baseline, week 1.** Instrument the decision and replay today's heuristic. Compute AIQ/PGR for:
   random-at-matched-cost, always-fast, always-quality, heuristic. If heuristic already sits near the
   quality-roster frontier, stop — the honest answer is "the roster has little to route" (the 2026 finding).
2. **Intent value, week 2.** Rung 1 + static intent priors. Report per-intent win rates: if no intent shows a
   ≥5-point win-rate difference between models with non-overlapping intervals, intent does not earn its
   complexity and we keep it as a label only.
3. **Classifier, weeks 3–4.** Rung 2 (embeddings) then rung 3 (3B) with parity gate. Track agreement, coverage,
   latency, and the swap it causes.
4. **Learned scorer, weeks 5–6.** Rung 4 in shadow mode: it scores every request, we answer with the heuristic,
   and we compare offline. Promote only on a replay win with a 95% interval.
5. **Residency policy, throughout.** Measure the swap cost term separately: how many choices would the quality
   term make that the swap term vetoes, and what did those choices cost in wall-clock time. This is the part
   that is ours, so it gets its own ablation.

**Kill criteria** (write these down before starting): if no policy beats random-at-matched-cost by more than its
confidence interval, ship the instrumentation + intent labels and stop. If intent adds nothing beyond
difficulty, keep difficulty only. If the 3B classifier's agreement never clears α, keep the kNN rung.

## 8. Reading list

### Routing — directly relevant

| Paper | Why it matters here |
|---|---|
| [A Systematic Survey of LLM Routing](https://aclanthology.org/2026.findings-emnlp.2072/) (EMNLP 2026) | The map: predictive / adaptive / generative / infrastructure; use it to name what we build |
| [Dynamic LLM Routers are Often Misguided](https://arxiv.org/abs/2610.02762) (2026) | The caution: none of six commercial routers beat random-at-matched-cost; four failure patterns |
| [RouteLLM](https://arxiv.org/abs/2406.18665) (ICLR 2025) | Binary strong/weak router from preference data; PGR/CPT metrics; generalises to unseen pairs |
| [FrugalGPT](https://arxiv.org/abs/2305.05176) (2023) | The cascade every later paper compares to; 98% cost reduction with a good scorer |
| [RouterBench](https://arxiv.org/abs/2403.12031) (2024) | 405k outcomes; the Zero-router baseline that kills naive claims |
| [TRACER](https://arxiv.org/abs/2604.14531) (2026) | Traces → surrogate → **parity gate**; the exact flywheel this repo can run |
| [SLM front-door routing](https://arxiv.org/abs/2604.02367) (2026) | Qwen2.5-3B at 0.783 / 988 ms; no model hit 0.85 — calibrate expectations |
| [Arch-Router](https://arxiv.org/abs/2506.16655) (2025) | 1.5B SFT "domain + action" router — the closest published thing to the user's ask |
| [SEAR](https://arxiv.org/abs/2603.26728) (2026) | Schema-based evaluation and routing **for LLM gateways**, with human-interpretable routing explanations — the same product surface we have |
| [Select-then-Route](https://scholar.google.com/scholar?q=Select-then-Route:+Taxonomy+guided+routing+for+LLMs+Shah) (EMNLP 2025) | Taxonomy narrows the pool, then a confidence cascade inside it |
| [Unified routing and cascading](https://scholar.google.com/scholar?q=A+unified+approach+to+routing+and+cascading+for+LLMs+Dekoninck) (ICML 2025) | Proves when a cascade is Pareto-optimal vs a single choice |
| [RouterEval](https://arxiv.org/abs/2503.10657) / [LLMRouterBench](https://arxiv.org/abs/2601.07206) / [RouterArena](https://scholar.google.com/scholar?q=RouterArena) | Benchmarks and their metric definitions (AIQ and friends) |
| [EmbedLLM](https://scholar.google.com/scholar?q=EmbedLLM) / [UniRoute](https://arxiv.org/abs/2502.08773) | Query-similarity routing; the kNN rung's theoretical backing |
| [BaRP](https://arxiv.org/abs/2510.07429) / [LLM Bandit](https://arxiv.org/abs/2502.02743) / [LinUCB routing](https://scholar.google.com/scholar?q=Online+multillm+selection+via+contextual+bandits) | The online rung, when offline labels run out |
| [R2A](https://arxiv.org/abs/2604.15022) / [RerouteGuard](https://arxiv.org/abs/2601.21380) / [Router fragility](https://scholar.google.com/scholar?q=How+robust+are+router-LLMs) | Routers are an attack surface: adversarial suffixes redirect to expensive models; off-the-shelf routers route jailbreaks to weak alignment |
| [Routing collapse](https://arxiv.org/abs/2602.03478) | The training pathology to watch for in rung 4 |
| [Dynamic model routing and cascading: a survey](https://arxiv.org/abs/2603.04445) (2026) | Second survey; useful cross-check of taxonomy and open problems |

### Evaluation reliability — for the repo's judge and gates

| Paper | Why it matters here |
|---|---|
| [Reliability without Validity](https://arxiv.org/abs/2606.19544) (2026) | 21 judges, ~541k judgments; **Qwen3-8B has the highest position bias (0.192)** — that is our judge. Adopt κ, AB+BA, ≥3 replicates |
| [Scoring bias in LLM-as-a-Judge](https://arxiv.org/abs/2506.22316) (2025) | Rubric order / score-ID / reference-answer bias in absolute scoring |
| [Position bias in rubric judges](https://arxiv.org/abs/2602.02219) (2026) | Balanced permutation; concrete mitigation for our rubric |
| [All Verdicts are Not Equal](https://arxiv.org/abs/2610.12083) (2026) | Trustworthy-verdict rate; why deterministic judges can be consistently wrong |
| [Neither Valid nor Reliable?](https://arxiv.org/abs/2508.18076) (2025) | The position paper every eval claim should answer |

### Adjacent, useful for the rest of the project

- [Cascadia](https://arxiv.org/abs/2506.04203) — cascade **serving** system; reading for latency budgeting.
- [vLLM semantic router / workload-router-pool](https://arxiv.org/abs/2603.21354) — where gateway routing is heading; a direct competitor shape.
- [Efficient LLM scheduling by learning to rank](https://scholar.google.com/scholar?q=Efficient+LLM+scheduling+by+learning+to+rank+Fu) — length prediction for throughput; relevant to the local box.
- [Whisper Leak](https://arxiv.org/abs/2511.03675) / [InputSnatch](https://arxiv.org/abs/2411.18191) — side channels on shared inference; relevant to the sovereignty story.
- [Routing, cascades, and user choice](https://arxiv.org/abs/2602.09902) — provider-side incentives; useful for the market note.

## 9. Decisions this pass locks in

1. **Intent is a label and a prior, never the whole policy.** Difficulty, budget and residency stay in the
   objective. This is a direct response to the 2026 audit, not a stylistic choice.
2. **No policy without a replay harness and four baselines.** The instrument lands first; the router is
   promoted by measurement, exactly like the eval release gate.
3. **The classifier ladder is gated by a parity threshold**, and the heuristic remains the fallback forever.
4. **Residency/swap cost is a first-class term.** It is the one input no paper models and we already measure.
5. **The router is a security boundary.** Fail-closed runs *before* scoring; intent can never widen the
   candidate set to a non-local model for a sensitive request. Router attacks (R2A) are on the threat list.
