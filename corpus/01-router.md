# Router

The AgentOps router is the heart of the gateway: a Go HTTP service that forwards chat
requests to language models. It exposes POST /v1/chat/completions in the OpenAI shape, a
JSON body with a messages array and optional model, max_tokens and stream fields, and it
still accepts the older body with a single prompt field. The response is an OpenAI
chat.completion object plus AgentOps extensions: text, reason, backend, tier, candidates
and trace_id.

Routing uses a heuristic classifier, not round-robin. When the model field is auto or
absent, the router classifies the last user message. A prompt that asks about the gateway
itself, such as which model is used, why it was chosen, or how routing works, goes to the
quality tier with the reason gateway-question and is answered by the router from the last
model-answered turn and the registered model roster, without calling a model; that
response carries answered_by set to router. A prompt containing a reasoning word such as
why, explain, compare or reason goes to the quality tier even when it is short, with the
reason reasoning-prompt. Otherwise prompts of 200 characters or fewer without complexity
keywords go to the fast model with the reason short-simple-prompt, and longer prompts, or prompts containing words like
analyze, prove, contract or summarize, go to the quality model with the reason
long-or-complex-prompt.

A client may also name a served model directly, which gives the reason explicit-model, or
name a tier such as fast, quality or cloud, which gives the reason explicit-tier and picks
the model that serves that tier. An unknown model name returns 400 unknown_model.

Before a model is called the gateway prepends a system message that names the registered
models with their tier and whether they are local or cloud, states the routing rule, and
names the model and reason this turn was routed to. It also tells the model to answer
questions about its own routing from those facts rather than guessing. The gateway builds
this message for every request and never stores it.

The router plans a chain of candidates once per request: the chosen tier, then the other
local tier, then a cloud model if one is configured. If a backend fails, the next candidate
is tried and the response's fallback field records what happened. Backends the health
prober last saw down are skipped. The router is built on the Go standard library net/http
package with no web framework and uses exactly one JSON error shape: an error object with
a code, a message, and the request's trace_id.
