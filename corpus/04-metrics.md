# Prometheus metrics

The router exposes five metrics at GET /metrics in Prometheus exposition format,
implemented with the Go standard library and no Prometheus client dependency.

`router_requests_total` counts successful chats per model. `router_errors_total`
counts chats that failed before a model answered, per model. `router_tokens_total`
sums input plus output tokens per model using Ollama's reported counts.
`router_latency_seconds` is a histogram of end-to-end latency including the model
call, with cumulative buckets from 0.05 seconds up to 120 seconds plus +Inf.
`eval_faithfulness` is a gauge holding the latest golden faithfulness score, loaded
from the database at startup.

The Grafana dashboard in `dashboard/grafana/router.json` draws five panels: requests
per second by model, p99 latency by model computed with `histogram_quantile`, total
tokens by model, the eval faithfulness gauge, and error rate by model. A k6 script in
`load-tests/router.js` hits `/health` at 50 requests per second for one minute and
requires fewer than 1 percent failures with p99 under 100 milliseconds.
