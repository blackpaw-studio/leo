# leo mcp-server

Run the Leo MCP server on stdin/stdout.

!!! note "Internal command"
    You do not invoke this directly. Leo wires it into every supervised agent's and task's MCP config automatically. It is documented here so operators know what it does and where to look when debugging.

## Usage

```bash
leo mcp-server
```

## Description

Speaks the [Model Context Protocol](https://modelcontextprotocol.io) over stdin/stdout. Supervised Claude agents dispatch the universal channel slash commands — `/clear`, `/compact`, `/stop`, `/tasks`, `/agent`, `/agents` — by calling the `leo_*` tools this server exposes.

For asynchronous work, `leo_dispatch` accepts exactly one of `template` or
`role`, plus optional `model` and `effort`. A role is resolved through the
active [delegation profile](../configuration/delegation.md), and permission is
checked against the resolved template. `leo_delegation` is the read-only tool
for showing that active routing.

### Environment

`mcp-server` reads two variables injected by the Leo supervisor:

| Variable | Purpose |
|----------|---------|
| `LEO_PROCESS_NAME` | Identifies the calling agent or task so tool actions target the right one, e.g. `<agent-name>` for an agent (including one backing a `runtime: persistent` task) or `task:<name>` for a one-shot task. |
| `LEO_WEB_PORT` | Daemon HTTP API port used by `mcp-server` to drive agent/task actions. |

## See Also

- [`leo channels register-commands`](channels.md#leo-channels-register-commands) — publish the matching slash commands to channel autocomplete menus
