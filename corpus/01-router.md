# Router

The AgentOps router is a Go HTTP service that forwards chat requests to local Ollama
models. It exposes POST /v1/chat/completions, which accepts a JSON body with a
`prompt` field and an optional `max_tokens` field, and answers with the generated
text, the model that handled it, the routing reason, and a trace id.

Routing uses a heuristic classifier, not round-robin. Prompts of 200 characters or
fewer without complexity keywords go to the fast model. Longer prompts, or prompts
containing words like analyze, compare, prove, contract, or summarize, go to the
quality model. Every routing decision carries a reason string: `short-simple-prompt`
or `long-or-complex-prompt`.

The router records Prometheus metrics for every request and serves them at
GET /metrics in Prometheus exposition format. It is built on the Go standard library
`net/http` package with no web framework and exposes exactly one JSON error shape:
an `error` object with a `code`, a `message`, and the request's `trace_id`.
