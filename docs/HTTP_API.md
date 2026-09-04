# HTTP API

`prism serve` exposes the same tools as the MCP server over plain HTTP, for
clients that don't speak JSON-RPC stdio (curl, custom automation, CI glue).

```sh
prism serve [--port 8888] [dir]
```

Port resolution: `--port` flag > `port:` in `prism.yaml` > `8888`. If the
chosen port is busy, up to 5 free alternatives are offered (non-interactive
runs take the first automatically). The server indexes the project on startup.

## Security

The listener binds to `127.0.0.1` only and has **no authentication, no TLS,
and no rate limiting**. It is a local-machine convenience surface. Do not
port-forward or reverse-proxy it to anything you wouldn't hand a shell on the
repository — tools read arbitrary files under the project root.

## Routes

Two fixed GET routes:

| Route | Returns |
|---|---|
| `GET /health` | `{"status":"ok","version":"vX.Y.Z"}` |
| `GET /status` | The token-savings ledger (alias for `prism_savings`) |

Every dispatchable tool is a POST route named after the tool — the route list
derives from the same source of truth as MCP dispatch
(`mcp.DispatchableTools()`), so the two surfaces cannot drift:

```
POST /prism_query        POST /prism_change_impact           POST /prism_index
POST /prism_read         POST /prism_missing_implementations POST /prism_drift
POST /prism_search       POST /prism_dead_code               POST /prism_compact
POST /prism_lookup       POST /prism_rename_plan             POST /prism_savings
POST /prism_node         POST /prism_map                     POST /prism_feedback
POST /prism_references   POST /prism_cycles
POST /prism_resolve      POST /prism_verify
POST /prism_edges        POST /prism_arch_check
```

Note this is the *dispatch* surface (21 tools) — wider than the 6-tool menu
advertised to agents over MCP `tools/list` (`prism_query`, `prism_read`,
`prism_search`, `prism_lookup`, `prism_change_impact`, `prism_verify`). The
other 15 routes (`prism_node`, `prism_references`, `prism_resolve`,
`prism_edges`, `prism_missing_implementations`, `prism_dead_code`,
`prism_rename_plan`, `prism_map`, `prism_cycles`, `prism_arch_check`,
`prism_index`, `prism_drift`, `prism_compact`, `prism_savings`,
`prism_feedback`) are CLI/operator tools kept off the agent menu — a long
menu measurably mis-routes the tools that matter — but callable by name
here.

## Request / response contract

- **Request body**: a single JSON object holding the tool's arguments — the
  same argument names as the MCP `inputSchema` for that tool (see
  `tools/list` output or the CLI help for each command). An empty body is
  allowed for tools with no required arguments.
- **Responses**:
  - `200` — tool result as JSON (shape identical to the MCP result payload)
  - `400` — request body was not valid JSON: `{"error":"..."}`
  - `500` — the tool returned an error (bad arguments included):
    `{"error":"..."}`

There are no other status codes; argument validation errors surface as `500`
with the tool's own message.

## Examples

```sh
# Complete change set for a signature change
curl -s localhost:8888/prism_change_impact \
  -d '{"query": "ResponseWriter.Status"}'

# Task context with anchor terms (terms is REQUIRED)
curl -s localhost:8888/prism_query \
  -d '{"task": "fix the savings ledger rollover", "terms": ["ledger"]}'

# Pure grep over the repo
curl -s localhost:8888/prism_search \
  -d '{"query": "TODO", "scope": "text", "limit": 50}'

# Diff completeness gate (nonzero "missed" means incomplete)
curl -s localhost:8888/prism_verify -d '{"base": "HEAD~1"}'

# No-argument tools take an empty body
curl -s -X POST localhost:8888/prism_savings
```
