# Prometheus metrics

The router exposes three metrics at GET /metrics in Prometheus exposition format,
implemented with the Go standard library and no client dependency.

`router_requests_total` is a counter labeled by model. `router_tokens_total` is a
counter of input plus output tokens per model, using Ollama's reported counts.
`router_latency_seconds` is a histogram of end-to-end request latency including
the model call, with buckets from 0.05 seconds up to 120 seconds.

A Grafana dashboard in `dashboard/grafana/router.json` draws three panels from
these metrics: requests per second by model, p99 latency by model computed with
`histogram_quantile`, and total tokens by model. A k6 smoke script hits
`/health` at 50 requests per second for one minute and requires fewer than 1
percent failures with p99 under 100 milliseconds.
