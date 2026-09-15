# Gateway features

The v1.0 gateway adds four things on top of routing. Streaming: a request with stream set
to true is answered as server-sent events in the OpenAI chunk shape, the final chunk
carries the usage, reason, backend and trace id, and the stream ends with a DONE marker.
A failure after the first delta is sent as an error event because the status code has
already been written.

Backends: every model runs on a backend that implements Generate, Stream and Health. The
local backend is Ollama; an OpenAI-compatible cloud backend such as Groq is registered
when GROQ_API_KEY is set. A health prober checks every backend every 15 seconds and
publishes the result as the router_backend_up gauge and in GET /v1/models.

Virtual API keys: a key is created on the command line with --create-key, which prints the
secret once and stores only its SHA-256 hash. Clients send it as a Bearer token. Each key
has a requests-per-minute limit enforced by a fixed one-minute window and a lifetime token
budget; exceeding them returns 429 rate_limited with a Retry-After header or 403
budget_exceeded. Setting REQUIRE_API_KEY to true rejects anonymous requests with 401.

The fail-closed rule: a request marked sensitive by the X-AgentOps-Sensitive header, by a
sensitive flag in the body, or by a keyword such as password or iban never has a cloud
candidate in its routing plan, even when every local backend is down, and asking for a
cloud model explicitly returns 403 sensitive_cloud_blocked. On SIGTERM the gateway stops
accepting connections, lets in-flight chats finish, flushes queued spans and exits.
