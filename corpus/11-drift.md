# Drift alerts

The eval suite runs on demand with `--score`, on a timer with `--schedule-evals`, or from
the Tower console's Run eval suite button, which allows one run at a time and answers 409
while a run is in progress. Each run stores its average score and a row per question and
logs its faithfulness score to Prometheus. A drift alert fires when faithfulness drops
below the configured threshold, and the MCP tool `get_drift_report` returns the
old score, the new score, the delta, whether the judge model changed, and the worst
failing cases.

Drift usually means one of three things: the documents changed without a golden
bump, the judge model changed behavior, or retrieval quality regressed after a
chunking or embedding change. The report's worst-cases list tells the human
which one by showing the exact questions that started failing.

A run is bounded by the EVAL_TIMEOUT setting, ninety minutes by default, because the judge
and the chat models share one 8GB GPU and a run slows down sharply while live traffic is
being served. A run that fails writes nothing. Cutting the scheduled suite is never allowed
to fix a red dashboard: a failing drift alert is investigated, then either the code is
fixed or the golden set is deliberately versioned forward with a recorded reason.
