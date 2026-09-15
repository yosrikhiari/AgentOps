# Monorepo layout

The project is one Go module named `agentops` with five packages: `/router` for
routing and metrics, `/tracker` for durable agents, `/evals` for scoring and
corpus tooling, `/mcp` for the MCP server, and `/dashboard` for Grafana and
Prometheus assets. One `go build ./...` builds everything and `go test ./...`
tests everything. The only dependency outside the standard library is the pgx
Postgres driver.

Code inside each package follows handler, service, repository layering. HTTP
handlers speak HTTP only, services hold the logic, and the packages talk to
Postgres through small interfaces named Execer, Queryer, and Rows so that
`main.go` alone adapts the pgx driver. Configuration comes from environment
variables read at boot, each with a default, so the binary starts with no
environment set.

All errors share one JSON shape with a code, a message, and the request's trace
id. Only 429 and 5xx responses are retried. The API is versioned as `/v1` and
v1 is never broken; a v2 is added instead.
