# Router

The AgentOps router is the heart of the gateway: a Go HTTP service that forwards chat
requests to language models. It exposes POST /v1/chat/completions in the OpenAI shape, a
JSON body with a messages array and optional model, max_tokens and stream fields, and it
still accepts the older body with a single prompt field. The response is an OpenAI
chat.completion object plus AgentOps extensions: text, reason, backend and trace_id.

Routing uses a heuristic classifier, not round-robin. When the model field is auto or
absent, prompts of 200 characters or fewer without complexity keywords go to the fast
model and longer prompts, or prompts containing words like analyze, compare, prove,
contract, or summarize, go to the quality model. The reason string is short-simple-prompt
or long-or-complex-prompt. A client may also name a served model directly, which gives the
reason explicit-model; an unknown model name returns 400 unknown_model.

The router plans a chain of candidates once per request: the chosen tier, then the other
local tier, then a cloud model if one is configured. If a backend fails, the next candidate
is tried and the response's fallback field records what happened. Backends the health
prober last saw down are skipped. The router is built on the Go standard library net/http
package with no web framework and uses exactly one JSON error shape: an error object with
a code, a message, and the request's trace_id.
