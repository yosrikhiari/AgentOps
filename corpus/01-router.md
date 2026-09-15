# Router

The AgentOps router is a Go HTTP service that forwards chat requests to local Ollama
models. It exposes POST /v1/chat/completions, which accepts a JSON body with a
`prompt` field and an optional `max_tokens` field.

Routing uses a heuristic classifier, not round-robin. Prompts of 200 characters or
fewer without complexity keywords go to the fast model. Longer prompts, or prompts
containing words like analyze, compare, prove, contract, or summarize, go to the
quality model. Every routing decision logs a reason string: `short-simple-prompt`
or `long-or-complex-prompt`.

The router records Prometheus metrics for every request: a per-model request
counter, a per-model token counter, and a latency histogram. Metrics are served at
GET /metrics in Prometheus exposition format.
