# Monorepo layout

The project is one Go module named `agentops` with five packages: `/router` for
routing and metrics, `/tracker` for durable agents, `/evals` for scoring and
corpus tooling, `/dashboard` for Grafana assets, and `/mcp` for the MCP server.
One `go build ./...` builds everything and `go test ./...` tests everything.

Code inside each package follows handler, service, repository layering. HTTP
handlers speak HTTP only, services hold the logic, repositories talk to
Postgres, Ollama, or Groq. SQL never appears in a handler. Configuration comes
exclusively from environment variables validated at boot, and the service fails
loudly on a missing value instead of starting half-configured.

All errors share one JSON shape with a code, a message, and the request's trace
id. Only 429 and 5xx responses are retried. The API is versioned as `/v1` and
v1 is never broken; a v2 is added instead.
