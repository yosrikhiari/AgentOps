# Traces

Every request carries a trace id: a 16-character hex string generated per
request and returned in the X-Trace-ID response header. Spans are stored in Postgres with
`trace_id`, `span_id`, an optional `parent_id`, a name, a start timestamp, and a JSONB
attributes column. This matches the Langfuse and OpenTelemetry span shape so a real
observability tool could ingest the traces later.

A router chat emits three chained spans. `route.decide` records the tier, the reason,
whether the request was sensitive, the requested model and the candidate chain.
`model.generate` records the model, the backend, the latency in seconds, the prompt and
completion token counts, and any backends that failed before it. `router.respond` closes
the chain. Each span's parent is the previous span. A tracker workflow emits one span per
step named researcher, drafter, and reviewer, using the workflow id as the trace id. Router
spans are written by a background goroutine through a buffered channel so a slow or dead
database never delays a chat, and the queue is drained on shutdown. Prompts are never
written into span attributes.

Spans are read back through GET /v1/traces/{id}, the `--trace` command-line flag, the
`inspect_trace` MCP tool and the Tower console's trace inspector; all of them redact
prompt, input, text, and output attributes. An unknown trace id returns HTTP 404 with the
error code `trace_not_found`. Span rows are retained for 30 days and eval scores are kept
forever.
