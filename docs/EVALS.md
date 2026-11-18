# Evals: what the numbers mean, and the current baseline

The suite exists so a release can be blocked by evidence instead of opinion. This page
records the protocol, the judge's own measured reliability, and the baseline every later
number is compared with. `docs/API.md` documents the endpoints; `docs/plans/` holds
scratch plans.

## What is measured

For each golden pair (question, frozen answer, `doc_ids`):

1. **Retrieval** — embed the question, top-5 vector search, `precision@5` and `recall@5`
   against the golden `doc_ids`. `recall = 0` is a **retrieval miss**: the judge is never
   called and the row stores `faithfulness NULL`, because "we looked in the wrong place"
   is not "the model lied".
2. **Faithfulness** — split the answer into claims at sentence boundaries, judge each claim
   against the retrieved context, and score `entailed / claims`. A pair whose claims are all
   refuted is **solvable-but-wrong**; a miss is **unsolvable**.

The suite line prints all of it:

```
suite n=72 scored=70 misses=2 refuted=6 no-claims=0 unsolvable=8 judge=phi4-mini:3.8b prompt=faithfulness-v2 faith=0.914 precision=0.325 recall=0.972
```

`unsolvable` = misses + fully refuted. It is the ceiling any routing policy is measured
against: no router can rescue a pair the corpus never supplied. `no-claims` counts answers
too short to split into claims — the judge never saw them, so they are reported apart from a
refuted verdict. `--route-replay` prints this share next to the decision mix, and prints
**β / smallest-tier-optimal as undefined** until at least two models have outcomes on the same
questions — one rung cannot support a co-failure rate, and a zero there would read like a
measurement.

## The judge, and its own reliability

- Local judge via Ollama, `JUDGE_MODEL`, **temperature 0** so the same claim gets the same
  verdict on a rerun (`evals/judge_temperature_test.go`). The production judge is Qwen3-8B,
  which does not fit the 4 GB dev card; runs record which model judged them.
- Prompt versions: `faithfulness-v1` asked supported/refuted; `faithfulness-v2` asks
  **entailed / contradicted / baseless** and only `entailed` counts. `eval_runs.judge_prompt`
  stores the version, drift reports `prompt_changed`, and the release gate refuses a window
  that straddles a change — a different prompt is a different judge.
- Validity is measured, not assumed: `--judge-audit` runs a hand-labelled 26-item set with
  retrieval out of the loop. On 2026-11-18 `phi4-mini:3.8b` scored **15 TP / 0 FN / 0 FP /
  11 TN**, with **1/16 label flips and 1/16 decision flips** under a context-order probe (item
  `s5`, the 4xx/429 retry rule, moved entailed→baseless).
- A **second judge** is available without a bigger card: `--judge2-model openai/gpt-oss-20b
  --judge2-backend groq` (needs `GROQ_API_KEY`). On the same set it also scored 15/0/0/11 with
  **κ = 1.00** on the supported/unsupported decision, and its own label flip (`h11`, baseless vs
  contradicted). Agreement that high on a small, clean set is weak evidence — the two judges can
  share a bias — but it rules out the opposite failure, a second opinion that disagrees at random.
- Groq retired `llama-3.1-8b-instant` in 2026, so the Groq judge and cloud defaults are
  `openai/gpt-oss-20b` (`GROQ_JUDGE_MODEL` overrides the judge; the audit prints the first error
  verbatim when a judge fails instead of only counting it).

**Read a perfect audit as a ceiling effect, not a verdict.** The seed set is small and its
contexts are short and clean. The honest claims it supports are narrow: the schema works, the
parser fails closed, and both judges handled the easy cases. A larger, noisier set is still the
next step for validity — not another judge.

## Run lifecycle

- The header is written as `status = running`, every pair is stored as it is judged, and
  `status = complete` plus the score are set only at the end.
- A judge crash marks the run `failed`; the next `--score` for the same golden set, judge
  and prompt **resumes it** and judges only the missing pairs.
- Drift and the release gate read `status = 'complete'` only. An interrupted suite keeps its
  pairs as evidence and the console shows an `incomplete` chip — never a stale score.

## Baseline — v3, 2026-11-17

| Field | Value |
|---|---|
| Golden set | `evals/golden/v3.jsonl`, 72 pairs |
| Corpus | 17 docs / 36 chunks (`evals/corpus/clean`), ingested with `nomic-embed-text` |
| Judge | `phi4-mini:3.8b`, prompt `faithfulness-v2`, temperature 0 |
| Judged | 70 / 72 |
| Retrieval misses | 2 |
| Refuted (all claims unsupported) | 6 |
| **Unsolvable** | **8 / 72 = 11.1%** |
| Faithfulness | **0.914** (threshold 0.70, no alert) |
| Precision@5 / Recall@5 | 0.325 / 0.972 |
| Release gate (k=1) | pass |

Two honest caveats on this run: the prober was stopped (the gateway was down to free VRAM),
so the GPU window says "no samples — comparability unknown"; and 70 pairs judged by a 3.8B
judge is a small sample with a weak judge. The number is a baseline to improve, not a claim
of quality.

What would move it: a bigger corpus (36 chunks for 72 questions is thin), hybrid
BM25+vector retrieval (two whole questions are currently unreachable), and the Qwen3-8B
judge on an 8 GB card.

## Workflow trajectories

Single-shot eval pairs grade an *answer*. A workflow needs its *path* graded: did every step
land, and did anything have to be retried? `--trajectory <id>` and the `#/workflows` table
report one run's shape from the `steps` rows — steps, attempts, retries — and mark it
**clean** when the workflow completed, every step is `done`, and no step took more than one
attempt.

That predicate is deliberately narrow, and the page says so: the toy
researcher→drafter→reviewer workflow has **no ground truth for its content**, so "clean"
measures orchestration reliability, not answer quality. It is still the property a durable
workflow actually claims — a retry loop that silently hides a flaky step is exactly what this
metric makes visible.

**pass^k** (tau-bench's reliability metric) needs repeats:

```bash
go run . --workflow-repeats 5 --tracker-input "what does tower do?"
```

With `n` runs and `c` clean ones, `pass^k = C(c,k)/C(n,k)` — the chance that all `k`
independent trials succeed, estimated without pretending the trials are independent when
they are not. `k=1` is the plain success rate; `k>n` is undefined and printed as nothing
rather than a zero. A workflow that is 90% reliable per run is only ~59% reliable across two
(`0.9²`), which is the number a retry policy has to be argued against.

## Reproduce

```bash
go run . --migrate
EMBED_MODEL=nomic-embed-text go run . --ingest
JUDGE_MODEL=phi4-mini:3.8b EMBED_MODEL=nomic-embed-text go run . --score --golden-version v3
go run . --judge-audit --judge-model phi4-mini:3.8b     # the judge's own accuracy
go run . --release-gate --golden-version v3 --gate-k 1
```
