# AgentOps MCP server

One binary, three ways in. HTTP router by default, MCP stdio server with `--mcp`,
and MCP over HTTP at `POST /mcp` when the gateway is running. The protocol is
plain MCP (`2024-11-05` JSON-RPC) — Claude Desktop, Cursor, Windsurf, VS Code,
or any generic MCP client can use it; nothing here is Claude-specific.

## Client config (any MCP client)

Build once, then point your MCP client at the binary (Claude Desktop shown;
Cursor / VS Code / Windsurf take the same command + args):

```json
{
  "mcpServers": {
    "agentops": {
      "command": "/path/to/agentops",
      "args": ["--mcp"],
      "env": {
        "OLLAMA_URL": "http://localhost:11434",
        "FAST_MODEL": "qwen2.5:3b-instruct",
        "QUALITY_MODEL": "qwen2.5:7b-instruct-q4_K_M",
        "POSTGRES_DSN": "postgres://USER:PASSWORD@localhost:5432/agentops"
      }
    }
  }
}
```

Dev alternative without building: `command: go`, `args: ["run", ".", "--mcp"]`,
`cwd: /path/to/AgentOps_Project_Plan`.

Remote agents that cannot pipe stdio use the gateway instead:

```bash
curl localhost:8080/mcp -H 'Content-Type: application/json' -d \
  '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# one request per POST, answered by the same handler as stdio — the two
# transports cannot drift. With REQUIRE_API_KEY=true a Bearer key is required.
```

## Tools

- `list_models` — every served model with tier, backend and last health probe, plus per-backend status.
- `get_stats` — request + token counts per model since the process started.
- `route_test_request` (`prompt` required) — routes one prompt, returns text, model,
  reason, trace id. Goes through the same path as HTTP, so it shows up in stats.
- `inspect_trace` (`trace_id` required) — every span for one trace, prompts redacted.
  Works for router chats (trace id from a chat response) and tracker workflows (workflow
  id). Unknown id → JSON-RPC error `-32004`. Needs Postgres.
- `get_drift_report` — last two golden eval runs: `score_then/score_now/delta/alert/
  judge_changed/worst_cases`. Needs Postgres and at least one `--score` run.

Every `tools/call` also emits one redacted `mcp.tool` span (`tool`/`ok`/`code`
only — never argument values, so prompts cannot leak through telemetry), visible
in `GET /v1/traces/{id}` and on the Tower `#/live` rail.

## Try it

Ask: "which model handled the most traffic today" — Claude calls `get_stats` and answers
from real counters.

## Notes

- `inspect_trace` and `get_drift_report` read Postgres (`POSTGRES_DSN`); the other three work without it.
- `route_test_request` goes through the same gateway path as HTTP — anonymous routing, the fail-closed sensitive rule, span emission — so a call from Claude shows up in the Tower console's recent requests.
- The same information is available to humans at `http://localhost:8080` (Tower console) when the binary runs in HTTP mode.

