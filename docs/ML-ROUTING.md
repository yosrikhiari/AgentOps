# ML in Tower — recommended addition: learned routing

This is a design note, not an implementation. It answers: given Tower's
codebase, data, and constraints, what ML should be added, how, and in
what order. Research basis: RouteLLM (UC Berkeley, ICLR 2025) and
FrugalGPT (Stanford, TMLR 2024), both of which target exactly the
problem Tower already has — choosing among heterogeneous LLMs under a
cost/quality constraint.

## 1. What Tower already has (the honest starting point)

- **Routing is heuristic.** `router.ClassifyThread` picks fast vs quality
  from prompt length, keyword lists, and a few reasoning words. It is the
  single decision that determines cost and quality for every request.
- **The data is already there.** `route_decisions` stores intent,
  confidence, difficulty, tier, reason, planned, chosen, fallback, failed,
  candidates, sensitive, created_at. `spans` stores model, backend,
  latency, tokens, error. `eval_runs`/`eval_pair_scores` store faithfulness
  per model per golden question. That is a labelled training set waiting
  to be used.
- **The measurement is already there.** `--score`, `--drift`, the golden
  sets, and the faithfulness judge give a held-out evaluation. Any new
  router can be scored against the same golden set and compared.
- **Constraints.** Single Go binary, no Python runtime in production,
  Ollama local, Postgres+pgvector. ML must be pure-Go, ONNX, or
  delegated to Ollama.

## 2. What to add (and what not to)

**Add: a learned routing classifier (RouteLLM-style).** This is the
highest-value ML addition because routing is the core decision, and Tower
already records everything needed to train and evaluate one. Expected
gain: RouteLLM reports ~2x cost reduction at ~95% quality on MT Bench by
routing easy queries to a weak model.

**Do not add (yet):**
- Full ML platform (MLflow, feature store, Kubernetes) — overkill for a
  single binary.
- Deep learning in Go — no ecosystem; use ONNX or delegate to Ollama.
- Online learning — Tower has no user feedback loop; offline training on
  eval data is the honest path.
- Anomaly detection as "ML" — EWMA / z-score on spans covers it; no
  model needed.
- Cost prediction — trivial regression, low value, not worth the infra.

## 3. Architecture — three phases, increasing commitment

### Phase 1: LLM-as-classifier via Ollama (1 day, behind a flag)

Replace the heuristic with a classification call to a small local model
(phi4-mini or the existing quality model) using a short prompt:
"Classify this request as fast or quality. Reply with one word." Fall back
to the heuristic on timeout/error. Gate with `ROUTER_POLICY=learned|heuristic`
(default heuristic).

- **Pros:** zero new infra, leverages existing Ollama, measurable via
  `--score`.
- **Cons:** +~100ms latency per request, Ollama dependency on the request
  path.
- **Measure:** run `--score` with each policy against the golden set;
  compare faithfulness and cloud-call rate.

### Phase 2: offline-trained lightweight classifier (3-5 days)

Train a logistic regression (or small gradient-boosted tree) in Python on
`route_decisions` joined with outcomes:

- **Features:** prompt length, intent (one-hot over the 8 intents),
  difficulty, has-code (bool), sensitive (bool), hour-of-day.
- **Label:** which tier/model gave better faithfulness for that prompt
  type, derived from `eval_pair_scores` (model A vs model B faithfulness)
  or, absent eval data, lower latency for the same faithfulness band.
- **Train:** Python script in `ml/` (new dir), scikit-learn, cross-validated.
- **Export:** weights to `models/router-weights.json` (coefficients +
  intercept + feature schema + version hash).
- **Serve:** pure-Go logistic regression (~microseconds per request) in
  `router/learned.go`; `ClassifyThread` calls it when weights are present,
  else heuristic. Log the model version in span attrs.

- **Pros:** no Ollama dependency on the request path, sub-millisecond,
  interpretable (coefficients).
- **Cons:** needs enough labelled data (~thousands of route_decisions with
  outcomes); risk of overfit on a small set.
- **Measure:** held-out golden set; compare APGR-style cost/quality tradeoff
  against the heuristic and the Phase 1 LLM classifier.

### Phase 3: FrugalGPT-style cascade (optional, ~1 week)

Train a quality scorer (DistilBERT or a small regressor) on
(prompt, answer, correct?) triples from `eval_pair_scores`. Use it to
early-exit the candidate chain: if the weak model's answer scores above a
threshold, skip the strong model. This is the FrugalGPT pattern and is
where the big cost savings come from, but it needs in-domain labelled
answers (the "same distribution" caveat from the paper).

## 4. Data pipeline

- `--export-routing-data` dumps `route_decisions` joined with spans and
  eval scores to JSONL (new flag, ~1 day).
- `ml/train_router.py` reads the export, trains, writes
  `models/router-weights.json`.
- `ml/README.md` documents the pipeline and the feature schema.
- Versioning: `ROUTER_WEIGHTS` env var points at the file; the hash is
  logged per request in span attrs and shown in `/v1/models` or the console.

## 5. Metrics to add

- `router_learned_decisions_total`, `router_heuristic_decisions_total`
- `router_policy` label on the decision record (heuristic vs learned)
- Faithfulness delta vs baseline (already tracked by drift)
- Cost ratio (cloud calls avoided) — new, derived from `route_decisions`

## 6. What not to do, and why

- **No feature store / MLflow / Kubernetes.** Tower is a single binary;
  these add operational surface for no gain at this scale.
- **No online learning.** There is no user feedback signal; offline
  training on eval data is honest. A "learned from thumbs-up" system
  would be gaming itself.
- **No cascade before Phase 2.** FrugalGPT's scoring function needs
  in-domain labels; Tower has them (eval_pair_scores), but only after the
  router is already measured. Phase 2 first.

## 7. Recommended starting point

Start with Phase 1 behind `ROUTER_POLICY=learned|heuristic` (default
heuristic), run `--score` against the golden set, and compare
faithfulness-per-cost. If the LLM classifier beats the heuristic, invest
in Phase 2 (offline-trained, sub-millisecond). Phase 3 only if cascade
savings are needed and in-domain labels are available.

## References

- RouteLLM: Learning to Route LLMs with Preference Data (Ong et al.,
  ICLR 2025) — arxiv 2406.18665. Binary routing from preference data;
  BERT/causal-LLM/MF/SW-ranking; data augmentation; generalizes across
  model pairs; ~2x cost reduction at 95% quality.
- FrugalGPT: How to Use Large Language Models While Reducing Cost and
  Improving Performance (Chen, Zaharia, Zou, TMLR 2024) — arxiv
  2305.05176. LLM cascade with a DistilBERT scoring function; 50-98%
  cost savings; requires in-distribution labelled data.
- Tower's own: `router/intent.go` (Analyse, Signal, ClassifyThread),
  `router/decision.go` (Decision, DecisionSink), `evals/drift.go`
  (faithfulness scoring), `migrations/0011_route_decisions.sql`.
