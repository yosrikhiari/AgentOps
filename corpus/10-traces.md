# Traces

Every request carries a trace id: a 16-character hex string generated per
request. Spans are stored in Postgres with `trace_id`, `span_id`, an optional
`parent_id`, a name, start and end timestamps, and a JSONB attributes column.
This matches the Langfuse and OpenTelemetry span shape so a real observability
tool could ingest the traces later.

Router calls, retrieval steps, judge calls, and tracker steps each emit a span
with the same trace id. The dashboard trace view picks one request and shows
every step it went through: which model handled it and why, what was retrieved,
and what each score was.

Span rows are retained for 30 days. Eval scores are kept forever. Prompts are
redacted in the `inspect_trace` MCP tool output by default.
