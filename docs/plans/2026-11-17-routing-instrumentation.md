# Phase 0 — route instrumentation and replay

*Companion to `docs/RESEARCH-intent-routing.md`. This phase changes **no routing decision**.
It records what the current router already decides, adds the intent/difficulty signal as
labels, and ships the replay command that later phases must beat.*

## Why this first

The literature pass (§2 of the research doc) is unambiguous: a router that cannot beat
random-at-matched-cost should not ship, and the only way to know is to have the decisions and
the outcomes in a table. Today the decision lives in a span attribute that expires with the
trace retention; the intent signal does not exist at all.

Phase 0 therefore:

- labels every request with `intent`, `intent_confidence`, `difficulty`, `policy`;
- records one row per decision in `route_decisions` (migration `0011`);
- adds `--route-replay`, which reports the decision mix and the baselines it must beat, and
  says plainly which outcome data is still missing;
- keeps the heuristic exactly as it is, so a regression in this phase is impossible by
  construction.

## Task 1 — the intent signal (`router/intent.go`)

- `Intent` type with eight labels: `extract`, `summarise`, `classify`, `code`, `math`,
  `reason`, `write`, `chat`.
- `Analyse(prompt) Signal{Intent, Confidence, Difficulty, Method}` — ordered bilingual
  keyword rules over the **last user turn**, plus structural hints (code fence, JSON request,
  numbered-list request, multiple question marks).
- `Difficulty` is a separate 0–1 scalar (length, complex keywords, output-structure
  requirements), because the audit says difficulty — not topic — is what predicts the
  strong-model win.
- No model calls, no allocation-heavy work: this runs on the request path and costs
  microseconds.
- Tests: one case per label, French coverage for the existing bilingual keywords, monotonic
  difficulty (long > short, structured > unstructured), confidence in `[0.5, 0.95]`.

## Task 2 — the decision record (`router/decision.go`, `migrations/0011_route_decisions.sql`)

- `Decision` carries `trace_id, intent, intent_confidence, difficulty, policy, tier, reason,
  chosen, candidates[], sensitive, at`.
- `Server.DecisionSink func(Decision)` — called once per routed request, after `Plan`, before
  the first backend. A nil sink is a no-op (tests, stdio MCP).
- The `route.decide` span gains the same labels; no prompt text, per the existing policy.
- Table `route_decisions` with indexes on `created_at` and `intent`. The writer in `main.go`
  mirrors `spanWriter`: buffered queue, one goroutine, drop-and-count on a dead database, so
  a Postgres outage costs the request nothing.
- Tests: the sink receives intent + chosen + candidate chain for an auto request; an explicit
  model still records `reason=explicit-model`; a sensitive request records `sensitive` and no
  cloud candidate; a nil sink does not panic; `Plan` returns byte-identical tier/reason for a
  table of golden prompts (the "no behaviour change" pin).

## Task 3 — replay (`--route-replay`)

Reads the last 30 days and prints:

1. decisions by intent × tier × chosen, with mean confidence and difficulty;
2. the four baselines as shares: always-fast, always-quality, today's heuristic split, and
   the random-at-matched-cost share (the strict baseline from the audit);
3. a clear line: **"outcome labels: none yet — AIQ/PGR need Phase 1's shadow outcomes"**,
   rather than inventing a quality number.

Flags: `--route-replay [--route-window 720h]`. Documented in README (policy test), and
`docs/API.md` gains the table's shape.

## Acceptance

- `go test ./router/ ./` green, including the golden "Plan unchanged" table.
- `go test ./policy/` green; `--route-replay` documented.
- Live: boot with Postgres, send three prompts (one short, one code, one long), then
  `--route-replay` shows three rows with the expected intents and the current fast/quality
  split — and the same answers as before the change.
- No new dependency, no new env var, no behaviour change.

## Out of scope (Phase 1+, deliberately)

Scoring candidates, changing the tier, intent-aware tie-breaks, the parity gate, shadow
outcomes, bandit adaptation. Those land only after the replay corpus exists.
