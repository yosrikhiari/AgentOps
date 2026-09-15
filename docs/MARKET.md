# AgentOps — market study (September 2026)

*What exists, what just happened to it, where AgentOps can actually win, and what to build to win it. Every external claim below has a source in §8; the code claims are from this repo.*

---

## 0. The one-paragraph answer

The "all-in-one LLMOps" market **consolidated hard in the first half of 2026**: TensorZero (the closest thing to AgentOps, $7.3M raised, 11.7k stars) was **archived on June 12** after its founders could not find a commercial fit and returned most of the seed; **Langfuse was bought by ClickHouse** (Jan 16), **Helicone by Mintlify** (Mar 3, now maintenance-mode), **Promptfoo by OpenAI** (Mar 9). The survivors are either enterprise gateways (LiteLLM, Portkey, Bifrost, Kong, AISIX) or eval/observability platforms that assume a cloud data plane. Meanwhile the local-model world grew up: Ollama is now a platform with `:cloud` models on the *same API*, Open WebUI ships arena evals and OTel export, and every serious tool speaks OpenTelemetry `gen_ai.*` spans.

**Nobody is winning the segment AgentOps already sits in: one binary, one Postgres, your own GPU, your data never leaves, every decision explainable.** That is not the market TensorZero died in (enterprise + VC), it is the market Ollama and Open WebUI created (individuals, small teams, regulated shops that cannot use a SaaS). "Dominate the market" means **become the default LLM-ops layer people put in front of Ollama** — the SQLite of LLM ops — not out-feature Portkey. The plan below is sized for that: five P0 items that close credibility gaps (one is a real security hole this study found), five P1 differentiators nobody in the OSS local space has, and a distribution plan.

## 1. The four day-two problems, and who solves them today

| Problem | Enterprise / VC answer | Local-first answer today | AgentOps today |
|---|---|---|---|
| Which model answers this request (routing, fallback, keys, budgets) | LiteLLM (Python, MIT), Portkey (gateway Apache-2.0 since Mar 2026), Bifrost (Go, 11 µs overhead at 5k RPS), Kong AI Gateway, AISIX (Rust single binary, OpenAI + Anthropic wire formats), OpenRouter (hosted), learned routers RouteLLM / Not Diamond | Ollama's own `:cloud` routing (no policy), Open WebUI (random load-balancing across Ollama instances, no rules) | Heuristic router with `reason`, fallback chain, virtual keys, RPM + token budgets, **fail-closed sensitive rule**, OpenAI shape + SSE, health probes |
| What happens when a multi-step agent crashes | Temporal (cluster), Restate (journal, exactly-once), Inngest (serverless), DBOS (Postgres library, `DBOSAgent` for Pydantic AI) | nothing in the Ollama ecosystem | Postgres-native row-status durability, `kill -9` proven, resume from UI/CLI |
| Is the model still telling the truth (evals, drift) | Promptfoo (→ OpenAI, CLI stays OSS), DeepEval (60+ metrics), RAGAS, Braintrust, Opik (Comet), LangSmith, Phoenix | Open WebUI arena/ELO (human votes, no ground truth) | Own claim-level faithfulness scorer, versioned golden sets with a validator, drift report + alert, judge bias guards, score history in the console |
| What happened to request X (traces, metrics) | Langfuse (→ ClickHouse; needs ClickHouse + Redis + S3 + Postgres to self-host), Phoenix, Opik, OpenObserve, Grafana LGTM, SigNoz Ollama dashboards | Open WebUI OTel export; Ollama `ollama-top` / LLMxRay dev diagnostics | Hand-written Prometheus + provisioned Grafana, OTel-*shaped* spans in Postgres, trace inspector, MCP `inspect_trace` |

**What no one else bundles for the local buyer:** all four, in one process, with prompts never persisted and a hard "sensitive never leaves the box" rule. Bifrost bundles gateway + MCP gateway but not evals/durability; LiteLLM bundles gateway + spend but is a Python service with Redis/Postgres; Langfuse bundles evals + traces but needs four data services.

## 2. Direct look-alikes, status verified (Sep 2026)

| Project | What it is | Status | What it teaches AgentOps |
|---|---|---|---|
| **TensorZero** (Rust, Apache-2.0) | gateway + observability + evals + experimentation, one stack | **Archived 2026-06-12**; ~11.7k stars; code forkable, unmaintained | The bundle is right; the business (VC + enterprise) is wrong. Being the "focused version of this pattern" is now the *only* version being maintained. Migration guides ("TensorZero alternative") are live search traffic |
| **Bifrost** (Go, Apache-2.0, Maxim) | gateway, 23+ providers incl. Ollama/vLLM, MCP gateway, governance | Active, well-funded | The Go gateway bar: 11 µs overhead, provider breadth, MCP tool orchestration. AgentOps will not beat it on providers; it can beat it on evals + durability + zero infra |
| **LiteLLM** (Python, MIT) | 100+ providers, proxy, virtual keys, spend, guardrails, admin UI | Active, de-facto standard | Feature checklist people expect from a "gateway": keys, budgets, spend per key, model aliases, fallbacks, guardrails, admin UI |
| **Portkey** | gateway (Apache-2.0 since Mar 2026) + managed platform | Active | Semantic cache, guardrails, observability "out of the box" is the SaaS pitch; self-hosters get the gateway only |
| **Helicone** (Apache-2.0) | one-line proxy + observability | **Maintenance mode** after Mintlify acquisition (2026-03-03): security/bug fixes/new models only; customers being migrated | ~16k orgs need a home; "moving off Helicone" is live search traffic. The one-line-proxy onboarding is the standard to match |
| **Langfuse** (MIT) | traces, evals, prompt mgmt | Acquired by ClickHouse (2026-01-16); stays OSS/self-hostable; v3 requires ClickHouse | Everyone accepts Langfuse's trace shape → **OTLP export to Langfuse/Phoenix** is table stakes for "plays with your stack" |
| **Promptfoo** (MIT) | CLI evals + red-teaming | Acquired by OpenAI (2026-03-09); CLI stays OSS; 130k MAU | The golden-set/CI-gating workflow is standard; AgentOps should **import Promptfoo test files** rather than compete on breadth |
| **Open WebUI** | the local chat UI; arena evals, OTel export, multi-Ollama balancing | Very active | The distribution channel: every local-model user runs it. AgentOps should be *the gateway Open WebUI points at* |
| **Ollama** | local runner → platform: `:cloud` models on the same API, `ollama launch` agents, web search | Very active | **Risk:** a `qwen3:cloud` model looks local to any gateway that trusts the Ollama host. **Opportunity:** Ollama has no policy layer; AgentOps is it |
| **AISIX** (Rust, Apache-2.0, API7) | single-binary gateway, OpenAI + Anthropic wire formats | New | Single static binary is now a recognised category; Anthropic Messages API compatibility is expected alongside OpenAI's |
| **DBOS** | durable execution as a Postgres library | Active, AI-agent focus | Validates ADR-0003's "rows, not a cluster"; DBOS is what to compare against in Track D |
| **RouteLLM / Not Diamond / OpenRouter Auto** | learned or aggregator routing, 26–85 % cost savings claims | Active | The routing bar is "quality-per-dollar with evidence". AgentOps's shadow-test promotion (Track F) is the local, explainable version of this |
| **MCP gateways** (Bifrost, IBM ContextForge, MCPX, Microsoft's K8s gateway) | governed entry point for tool calls, tool-call observability | New category, "observability mandatory in 2026" | Tool-call spans and per-key tool allow-lists are the next expected feature of anything called a gateway |

## 3. Feature matrix — AgentOps vs the field

✓ has it · ◐ partial · ✗ no · (n/a) not their job

| Capability | AgentOps | LiteLLM | Bifrost | Portkey OSS | Langfuse | Promptfoo | Open WebUI |
|---|---|---|---|---|---|---|---|
| OpenAI-compatible chat + streaming | ✓ | ✓ | ✓ | ✓ | n/a | n/a | ✓ (as client) |
| Anthropic Messages API compat | ✗ | ✓ | ✓ | ✓ | n/a | n/a | ✗ |
| Providers | 2 (Ollama + any OpenAI-style) | 100+ | 23+ | many | n/a | many | Ollama + OpenAI-style |
| Rule-based routing with a stated reason | ✓ | ◐ (fallbacks, weights) | ◐ | ◐ | n/a | n/a | ✗ |
| Learned / shadow-tested routing | ✗ (Track F) | ✗ | ✗ | ✗ | n/a | n/a | ✗ |
| Virtual keys, RPM, token budget | ✓ | ✓ | ✓ | ✓ | n/a | n/a | ◐ (users) |
| Spend in currency per key | ✗ (tokens only) | ✓ | ✓ | ✓ | ✓ | n/a | ✗ |
| Fail-closed data-sovereignty rule | ✓ | ✗ | ✗ | ◐ (guardrails) | n/a | n/a | ✗ |
| Prompt/response capture | never (by policy) | ✓ | ✓ | ✓ | ✓ | n/a | ✓ |
| Durable multi-step workflows | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Own faithfulness scorer + golden versioning | ✓ | ✗ | ✗ | ✗ | ◐ (LLM-judge evals) | ✓ | ◐ (arena) |
| Drift alert on score history | ✓ | ✗ | ✗ | ✗ | ◐ | ◐ | ✗ |
| Traces (OTel shape) | ◐ (shape, no OTLP export) | ◐ | ✓ | ✓ | ✓ | n/a | ✓ (export) |
| Prometheus metrics + Grafana | ✓ | ✓ | ✓ | ✓ | ◐ | n/a | ✓ |
| MCP server (tools for an assistant) | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| MCP *gateway* (tool-call governance) | ✗ | ◐ | ✓ | ✗ | ✗ | ✗ | ✗ |
| Web console | ✓ (read-mostly) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Infra to self-host | **1 binary + Postgres** | Python + Postgres + Redis | 1 binary (+ store) | Node | Postgres + ClickHouse + Redis + S3 | CLI | Python + SQLite/Postgres |
| Direct deps | 1 | dozens | some | some | many | many | many |
| Tests / policies in CI | 68 + 11 policies | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

**Where AgentOps is genuinely ahead of everything in its infra class:** durability + evals + fail-closed + MCP tools, in one process. **Where it is behind table stakes:** providers, Anthropic API, spend in currency, OTLP export, hybrid retrieval, and — found by this study — the `:cloud` hole.

## 4. Findings from this study that change the code

1. **Security: Ollama `:cloud` models break the fail-closed rule.** Since 2026 Ollama serves hosted models (`kimi-k2.6:cloud`, `deepseek-v4-pro:cloud`) through the *same* local API. `OllamaClient.Local()` returns `true` for every model, so `FAST_MODEL=qwen3:cloud` would make a sensitive request leave the machine while every log says "local". Fix: locality is a property of the **model ref**, not the backend — a `:cloud` suffix (or an explicit `local:false` on the ref) makes it non-local; `TestPolicySensitiveHasNoCloudCandidate` gets a `:cloud` case. **P0, done alongside this study.**
2. **Retrieval: the first vector-only miss.** Golden v3 pair 12 ("what error code when all candidate backends fail") retrieved the router's *candidate chain* chunk, not the Ollama chunk holding `502 ollama_unavailable` — recall 0, faithfulness 0, with a true answer. At 36 chunks vector-only search already loses on vocabulary overlap; every competitor with RAG evals ships hybrid retrieval. **P0: BM25 + RRF (the parked GED weights), and report "retrieval miss" separately from "unfaithful".**
3. **Interop: OTel `gen_ai.*` is the lingua franca**, still "Development"-stability but universally ingested (Langfuse, Phoenix, OpenObserve, Grafana Tempo, SigNoz). AgentOps stores OTel-*shaped* spans but cannot export them. **P0: OTLP/HTTP exporter with `gen_ai.*` attributes, dual-named until the conventions stabilise.**
4. **The market moved from "which tool" to "which tool survives".** Three acquisitions and one shutdown in six months; buyers of a self-hosted tool now ask "what happens when you get bought". A single-binary Apache-2.0 tool with no company behind it is, for once, the *safer* answer — if it is visibly maintained (CI, releases, changelog — all present) and if it exports its data in standard formats (OTLP, Promptfoo test files) so nobody is locked in.

## 5. Positioning

**Beachhead:** individuals and small teams running models on their own hardware (Ollama, vLLM, LM Studio) who need a gateway with keys and budgets, want to know their RAG answers are grounded, and are not allowed — or not willing — to send prompts to a SaaS. Concretely: solo builders, agencies shipping AI features to SMB clients, regulated shops (health, legal, finance, public sector) in markets like Tunisia/EU where data residency is a legal question, and every Helicone/TensorZero user looking for a home.

**One-line pitch:** *"The LLM-ops layer for Ollama: one binary in front of your models — routing, keys, budgets, evals, drift, traces — your prompts never leave the box."*

**Moats that are real:**
1. **Zero-infra bundle** — gateway + evals + durability + traces on one Postgres. Langfuse alone needs four services; LiteLLM needs Redis; AgentOps needs nothing new.
2. **Fail-closed sovereignty as a first-class policy**, tested, with a `:cloud`-aware definition of "local". No gateway in the OSS list has this as a rule; Portkey sells guardrails.
3. **Explainable routing + explainable evals** — a `reason` on every request, a justification on every judge verdict, a golden set with a hash. The audit story regulated buyers ask for.
4. **Independence** — one direct dependency; if every vendor above disappears, the binary still builds.

**What not to compete on:** provider count (Bifrost/LiteLLM own it; support "any OpenAI-style + Anthropic" and stop), enterprise RBAC/SSO, semantic caching, hosted anything, learned routers trained on someone else's data.

## 6. Roadmap to win the segment

Effort in solo days. Ordered so that each step is demoable and each closes a gap named above.

### P0 — credibility (≈ 6 days)
| # | Item | Why | Effort |
|---|---|---|---|
| 1 | `:cloud`-aware locality on `ModelRef`; policy test | Security hole found by this study | 0.5 (done) |
| 2 | Hybrid retrieval: Postgres FTS (BM25-ish `ts_rank`) + vector, RRF k=60, weights 0.6/0.4; `--search` shows both scores | Pair-12 miss; every RAG eval tool has it | 1.5 |
| 3 | Scorer reports `retrieval_miss` (recall 0) separately from unfaithful; console shows it | A true answer scored 0 is a lie about the system | 0.5 |
| 4 | OTLP/HTTP span exporter (`OTEL_EXPORTER_OTLP_ENDPOINT`), `gen_ai.*` attributes with dual names; verified into Langfuse and Grafana Tempo | Interop = "plays with your stack"; also the Langfuse/Helicone migration story | 2 |
| 5 | Anthropic Messages API on `/v1/messages` (request/response translation, streaming) | Half the client libraries; AISIX/Bifrost/LiteLLM all have it | 1.5 |

### P1 — differentiators (≈ 12 days)
| # | Item | Why | Effort |
|---|---|---|---|
| 6 | **Shadow-test auto-promotion** (Track F): sample N % of quality-tier traffic to the fast model too, judge both, promote per task type at n ≥ 100, p < 0.05, with a console page showing the evidence | The local, explainable answer to RouteLLM/Not Diamond; nobody OSS-local has it | 4 |
| 7 | **Sovereignty policy engine**: per-key `data_policy` (`local_only` / `cloud_ok`), PII detectors beyond keywords (IBAN/CIN/phone/email regex, French + Arabic keyword sets), audit line per blocked request, `router_sensitive_blocked_total` | The moat, made product-shaped for regulated buyers | 2 |
| 8 | **Spend in currency** per key/model (price table per model, Ollama = electricity estimate optional), budget alerts, key management in the console | Table stakes vs LiteLLM/Bifrost; the console's biggest missing page | 2 |
| 9 | **Opt-in capture** per key: store prompts/answers encrypted at rest (AES-GCM, key from env), retention days, redaction on read stays default | Debuggability without abandoning the privacy default; Helicone refugees expect it | 2 |
| 10 | **MCP tool-call spans + per-key tool allow-list** in the gateway (proxy MCP servers, record `execute_tool` spans) | The 2026 "gateway" definition now includes tools; Bifrost bundles it | 2 |

### P2 — distribution (≈ 6 days)
| # | Item | Why | Effort |
|---|---|---|---|
| 11 | `curl -fsSL … \| sh`, Homebrew tap, `winget`, GHCR image, `agentops init` (writes compose + first key + opens the console) | Time-to-first-trace < 5 min is the Helicone/Ollama bar | 1.5 |
| 12 | Open WebUI recipe: point it at AgentOps, keys per user, arena results flow into `eval_runs` | The channel where local-model users already are | 1 |
| 13 | Import Promptfoo test files → golden set; export `eval_runs` as Promptfoo/JSONL | Migration path in, no lock-in out | 1 |
| 14 | "Migrating from TensorZero / Helicone" guides + the 2-minute demo recording + a docs site (the lessons are already the material) | Live search intent; both are open right now | 1.5 |
| 15 | Multi-replica correctness: shared key counters in Postgres, `FOR UPDATE SKIP LOCKED` resume lease, eval lock row (Track D scope) | Small teams have two boxes; honesty list → done list | 1 |

### Explicitly not on the roadmap
Semantic cache, prompt registry/versioning UI, RBAC/SSO, hosted control plane, Kubernetes operator, provider count race. Each is where the acquired or archived companies spent their money.

## 7. What "winning" measures

Vanity metrics (stars) are excluded; the segment is judged by adoption you can count without telemetry (the privacy rule forbids phoning home).

| Measure | 6-month target | How counted |
|---|---|---|
| Time from `curl … \| sh` to first trace in the console | < 5 min on a fresh machine | timed install on a clean VM, recorded in the plan |
| Release downloads (binaries + GHCR pulls) | 1,000 / month | GitHub release + GHCR stats |
| Issues and PRs from people who are not the author | 20 issues, 3 merged external PRs | GitHub |
| "Open WebUI + AgentOps" and "TensorZero alternative" documented setups linked from outside | 5 external mentions | search |
| Golden faithfulness on the shipped corpus, every release | ≥ 0.95, retrieval misses reported separately | `eval_runs` |
| Router overhead | p99 < 100 ms at 50 RPS on a laptop, unchanged by P0–P1 | k6 gate |
| Every P0/P1 item shipped with a policy or test and a CHANGELOG line | 100 % | CI |

## 8. Sources

Market events: [TensorZero repo (archived 2026-06-12)](https://github.com/tensorzero/tensorzero) · [TensorZero shutdown analysis](https://byteiota.com/tensorzero-shuts-down-what-oss-llmops-cant-survive/) · [Routeplane on the TensorZero archive](https://routeplane.ai/blog/tensorzero-alternative-migration/) · [Mintlify acquires Helicone](https://www.mintlify.com/blog/mintlify-acquires-helicone) · [Helicone maintenance mode](https://chatforest.com/reviews/helicone-llm-observability-gateway/) · [ClickHouse acquires Langfuse (Orrick)](https://www.orrick.com/en/News/2026/01/Open-source-LLM-Observability-Langfuse-Acquired-by-ClickHouse-Inc) · [Langfuse discussion #11593](https://github.com/orgs/langfuse/discussions/11593) · [OpenAI to acquire Promptfoo](https://openai.com/index/openai-to-acquire-promptfoo/) · [Promptfoo joining OpenAI](https://www.promptfoo.dev/blog/promptfoo-joining-openai/).

Gateways: [LLM gateway comparison 2026 (FloTorch)](https://www.flotorch.ai/blogs/llm-gateway-comparison-2026) · [7 open-source LiteLLM alternatives (API7 / AISIX)](https://api7.ai/litellm-alternative) · [LiteLLM vs Portkey vs Bifrost](https://builderai.tools/blog/llm-gateway-comparison-litellm-portkey-bifrost) · [Best open-source gateways for self-hosted (Maxim)](https://www.getmaxim.ai/articles/5-best-open-source-llm-gateways-for-self-hosted-deployments-in-2026/) · [Self-hosted LLM gateway guide (API7)](https://api7.ai/self-hosted-llm-gateway).

Evals: [DeepEval alternatives 2026 (Braintrust)](https://www.braintrust.dev/articles/deepeval-alternatives-2026) · [Best LLM & RAG eval tools 2026](https://agentscamp.com/guides/evaluation/best-llm-eval-tools-2026) · [Promptfoo vs DeepEval vs RAGAS](https://genai.qa/blog/promptfoo-vs-deepeval-vs-ragas/).

Durable execution: [Temporal/Inngest/Restate/Prefect for AI agents](https://comuvia.ai/articles/durable-execution-for-ai-agents-temporal-vs-inngest-vs-restate-vs-prefect) · [Durable AI agents 2026 incl. DBOS](https://www.reactify-solutions.com/articles/durable-ai-agents-2026).

Routing: [Best LLM routers 2026 (Braintrust)](https://www.braintrust.dev/articles/best-llm-routers-2026) · [RouteLLM benchmarks](https://klymentiev.com/blog/llm-router) · [awesome-ai-model-routing](https://github.com/Not-Diamond/awesome-ai-model-routing).

Ollama and local UIs: [Ollama in 2026: from local runner to platform](https://angelo-lima.fr/en/ollama-2026-state-of-the-art-en/) · [What is Ollama (Sep 2026)](https://www.thundercompute.com/blog/what-is-ollama-run-ai-models-locally) · [Open WebUI evaluations](https://docs.openwebui.com/features/administration/evaluation/) · [Open WebUI OpenTelemetry](https://docs.openwebui.com/reference/monitoring/otel/) · [Open WebUI routing discussion](https://github.com/open-webui/open-webui/discussions/23593).

MCP gateways: [Best open-source MCP gateways 2026 (Maxim)](https://www.getmaxim.ai/articles/best-open-source-mcp-gateways-in-2026/) · [Best open-source MCP gateways (Lunar)](https://www.lunar.dev/post/the-best-open-source-mcp-gateways-in-2026).

Standards: [OTel GenAI conventions are not stable yet (Jul 2026)](https://dev.to/azena-ai/opentelemetrys-genai-semantic-conventions-are-not-stable-yet-heres-what-actually-shipped-in-2026-3mke) · [OTel GenAI semantic conventions (Dash0)](https://www.dash0.com/knowledge/opentelemetry-genai-semantic-conventions-explained) · [OTel GenAI agent tracing in production](https://veraexmachina.com/tech/opentelemetry-genai-agent-observability-production/).
