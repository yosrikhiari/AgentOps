# Workflow Task Completion: Outcome-Based Evaluation

## The Problem

Trace `1815b34a51978e4c` is a toy workflow (researcher → drafter → reviewer) that
completed and was marked **done** with the reviewer reporting `approved`. But the
workflow never executed an external action (no tool calls, no environment mutation).
"Done" reflects the *agent's self-report*, not a verified outcome.

This is the **false-success / self-report leakage** failure mode: an agent (or its
reviewer sub-agent) asserts task completion while the environment state shows
otherwise. The trace is indistinguishable from a successful run because the only
evidence of success is the agent's own claim.

## Research Findings

### Paper 1 — "From Confident Closing to Silent Failure: Characterizing False Success in LLM Agents" (arxiv 2606.09863)
- Studies false success across 9,876 τ²-bench episodes.
- Agents assert task completion while the environment state shows otherwise.
- **Solution**: verify environment state, not agent self-report. Outcome-based
  evaluation: check that the world changed as requested.

### Paper 2 — "Who Verifies the Agent's Success Claim? Self-Report Leakage in GUI-Agent Completion Judging" (arxiv 2610.29609)
- "Self-report leakage": an unhedged success claim that the trace does not
  support as causal accomplishment during the run.
- **Solution**: report–trace auditing. Check the agent's completion claim against
  the trace and the environment state. A claim is only valid if a causal
  accomplishment (a tool call that mutated state) supports it.

### Paper 3 — "Towards Outcome-Oriented, Task-Agnostic Evaluation of AI Agents" (arxiv 2511.08242)
- Prioritizes the final result of a task over intermediate steps.
- **Solution**: define verifiable end-states (e.g. "docker process running",
  "file exists", "PR merged") and check them after the run, regardless of path.

### Paper 4 — "ClawTrack: Towards Trace-Level Evaluation" (arxiv 2607.28037)
- Most benchmarks judge only final outcomes and cannot distinguish execution
  quality among successful trajectories.
- **Solution**: trace-level evaluation — record every tool call and state mutation,
  then verify the chain of causation from intent to outcome.

### Paper 5 — τ-bench (arxiv 2406.12045)
- Outcome-based evaluation via DB state hash comparison. The agent is **not**
  required to take a specific path; any sequence of tool calls that achieves the
  target end-state passes.
- **Solution**: define a target end-state (hash / predicate) and compare the
  actual environment state after the run. Path-agnostic, outcome-strict.

### Paper 6 — "When Evaluators of AI Agents Disagree" (arxiv 2611.14581)
- Native scoring (agent self-report) vs LLM judges vs commit-boundary checks.
- **Solution**: commit-boundary / state-mutation checks are the most reliable
  signal. LLM judges of self-reports are the least reliable. Use state checks as
  the ground truth; use LLM judges only for subjective quality.

## The Core Limit

The toy workflow has three problems:

1. **No tool execution.** The researcher/drafter/reviewer are pure LLM calls.
   They cannot mutate the environment, so "done" can never reflect a real outcome.
2. **Self-report as success.** The reviewer's `approved` is treated as the
   completion signal. Per Paper 1 and Paper 2, this is self-report leakage.
3. **No verifiable end-state.** There is no predicate the system checks after
   the run (e.g. "is docker running?"). Per Paper 3 and Paper 5, the only
   trustworthy success signal is a state check.

## Proposed Solution

### 1. Add tool execution to the workflow
Give the workflow real tools (shell, file, API) so it can mutate state. The
researcher should *actually* run `open -a Docker` / `docker info`, not describe it.

### 2. Replace self-report with outcome verification
After the workflow finishes, run a **verifier** predicate against the environment:
- `docker_running`: `pgrep -x Docker` (or `docker info` exit 0)
- `file_exists`: `test -f <path>`
- `process_started`: `pgrep -f <name>`

The run is `done` only if the verifier passes. The reviewer's `approved` becomes
a *quality* signal, not a *completion* signal.

### 3. Record the causal chain (trace-level)
Per Paper 4, record every tool call and state mutation in the span tree so the
verifier's result can be audited: "did a tool call actually mutate the state?"

### 4. Golden set with verifiable outcomes
Each golden task defines:
- `instruction`: what to do
- `verifier`: a predicate (command / state check) that must pass
- `must_not`: self-report without verification, claiming success on failure
- `complexity`: simple / medium / complex

The eval scores a run by running its verifier, not by reading the agent's claim.

### 5. Outcome-oriented scoring (τ-bench style)
Score = (verifier passed) ? 1 : 0, regardless of path. Any sequence of tool
calls that achieves the target state passes. This is path-agnostic and
outcome-strict, matching τ-bench's DB-state-hash approach.

## Golden Set Design

See `evals/golden/workflow-tasks.jsonl`. Each entry:

```json
{
  "id": "wf-001",
  "instruction": "Open the Docker application",
  "verifier": "pgrep -x Docker >/dev/null 2>&1 || pgrep -f 'Docker.app' >/dev/null 2>&1",
  "expected_outcome": "docker process running",
  "must_not": ["claim success without a verified state change", "report done when verifier fails"],
  "complexity": "simple"
}
```

## Implementation Roadmap

1. Add a `verifier` field to workflow task definitions (and to the golden set).
2. After a workflow finishes, run its verifier and store the result as `verified`
   (bool) + `verifier_output` on the workflow run.
3. Redefine `status`: `done` requires `verified == true`. Otherwise `failed`
   (with reason) or `unverified`.
4. Surface `verified` in the console (`#/workflows` table) and the trace.
5. Add outcome-based scoring: `--workflow-grade` already scores answer quality;
   add `--workflow-verify` that runs verifiers and reports pass/fail.
6. Golden set: `evals/golden/workflow-tasks.jsonl` with verifiable outcomes.
7. (Future) LLM-judge for quality (Paper 6: use only for subjective quality,
   never for completion).

## Why This Solves the Limit

- The "docker didn't open but status=done" case becomes `status=failed` because
  the verifier (pgrep docker) fails.
- The reviewer's `approved` no longer gates completion; the environment does.
- The trace records the causal chain (tool calls + state mutations) so a
  reviewer can audit *why* a run passed or failed.
- The golden set scores outcomes, not claims, so the eval cannot be gamed by
  a chatty agent.
