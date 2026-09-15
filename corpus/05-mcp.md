# MCP server

The same Go binary runs a second mode as an MCP server over stdio, selected with
the `--mcp` flag. It speaks JSON-RPC 2.0 with newline-delimited messages and
implements the MCP handshake: `initialize`, `notifications/initialized`,
`tools/list`, and `tools/call`. A malformed line gets JSON-RPC error -32700
instead of being silently dropped.

Five tools are exposed. `list_models` returns the fast and quality model names with
their tiers. `get_stats` returns request and token counts per model since the
process started, read from the same in-memory metrics as the HTTP path.
`route_test_request` takes a `prompt` argument, sends it through the identical
router chat path including metrics recording, and returns the text, model, reason,
and trace id. `inspect_trace` takes a `trace_id` and returns every span of that
trace with prompts redacted, or error -32004 when the trace does not exist.
`get_drift_report` returns the last two golden eval runs with their delta, alert
flag, and worst cases.

Claude Desktop connects to the binary with `command` set to the compiled path and
`args` set to `["--mcp"]`, plus environment variables for `OLLAMA_URL` and the two
model names.
