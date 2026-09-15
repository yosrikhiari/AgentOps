# Prometheus metrics

The gateway exposes its metrics at GET /metrics in Prometheus exposition format,
implemented with the Go standard library and no Prometheus client dependency. There are
eight metric families.

router_requests_total counts successful chats per model. router_errors_total counts chats
that failed before a model answered, per model. router_tokens_total sums input plus output
tokens per model using Ollama's reported counts. router_latency_seconds is a histogram of
end-to-end latency including the model call, with cumulative buckets from 0.05 seconds up
to 120 seconds plus +Inf. router_backend_up is a gauge set by the health prober, 1 when a
backend's last probe succeeded. router_key_requests_total and router_key_rejected_total
count accepted and rejected requests per API key. eval_faithfulness is a gauge holding the
latest golden faithfulness score, loaded from the database at startup.

The Grafana dashboard in dashboard/grafana/router.json draws five panels: requests per
second by model, p99 latency by model computed with histogram_quantile, total tokens by
model, the eval faithfulness gauge, and error rate by model. The dashboard and its
Prometheus datasource are provisioned from files when the monitoring compose profile
starts. A k6 script in load-tests/router.js hits /health at 50 requests per second for one
minute and requires fewer than 1 percent failures with p99 under 100 milliseconds; the
recorded run had zero failures out of 2725 requests with a p99 of 70 milliseconds.
