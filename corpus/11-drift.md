# Drift alerts

The eval suite runs automatically on a timer and on demand. Each run logs its
faithfulness score to Prometheus. A drift alert fires when faithfulness drops
below the configured threshold, and the MCP tool `get_drift_report` returns the
old score, the new score, the delta, and the worst failing cases.

Drift usually means one of three things: the documents changed without a golden
bump, the judge model changed behavior, or retrieval quality regressed after a
chunking or embedding change. The report's worst-cases list tells the human
which one by showing the exact questions that started failing.

Cutting the scheduled suite is never allowed to fix a red dashboard. A failing
drift alert is investigated, then either the code is fixed or the golden set is
deliberately versioned forward with a recorded reason.
