# ADR-0005 — Per-question eval rows

**Status:** accepted (2026-09-14) · **Context:** the drift report must name the worst questions without re-running judges.

**Decision.** `eval_runs` is the run header (golden version, judge model, average score); `eval_pair_scores(run_id, question, faithfulness, precision, recall)` holds one row per pair. Header and rows are written in one transaction. The drift report compares the last two runs of a golden version, flags `judge_changed`, alerts below `EVAL_THRESHOLD`, and lists the three lowest pairs.

**Consequences.** A crashed run writes nothing; the console's score history and worst-case table are plain queries. `question` is the row key, so `--freeze-golden` rejects duplicate questions.
