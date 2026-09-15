# ADR-0001 — One module, one binary

**Status:** accepted (2026-09-13) · **Context:** solo project, four tools that share types and one database.

**Decision.** One Go module `agentops`, packages `router/ tracker/ evals/ mcp/ console/`, one `main.go` that wires them and selects the mode by flag (`--mcp`, `--score`, `--run-tracker`, …). One Docker image. Packages talk to Postgres through tiny interfaces (`Execer/Queryer/Rows`) and never import `pgx`; only `main.go` adapts the driver.

**Consequences.** `go test ./...` covers the product; the MCP server and the HTTP gateway share the same `Chat()` path so stats cannot diverge; adding a mode is one flag and one function. Cost: `main.go` is the wiring hub and has grown to ~800 lines; a second binary would only be justified by a second deployable.
