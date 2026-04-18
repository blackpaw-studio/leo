# leo run

Execute a scheduled task once.

## Usage

```bash
leo run <task>             # run the task
leo run <task> --dry-run   # print the assembled prompt and claude args without executing
```

## Description

`leo run` is the entry point for scheduled task execution. It reads the config, finds the named task, assembles a prompt, and invokes `claude -p` in non-interactive mode. This is the same code path the daemon's in-process scheduler uses when a task fires.

You can also run it manually to test a task:

```bash
leo run heartbeat
```

## Flags

| Flag | Description |
|------|-------------|
| `--dry-run` | Print the assembled prompt, resolved claude arguments, and the environment that would be passed (sensitive values redacted). Does not invoke `claude`. Useful when debugging prompt assembly or inspecting effective model/max-turns after the override cascade. |

## Prompt Assembly

Leo builds the final prompt by combining up to two parts:

1. **Silent preamble** (if `task.silent: true`) — instructs the agent to work without narration and output `NO_REPLY` if there's nothing to report
2. **Prompt file content** — the task's `prompt_file`, read from the workspace

The agent is responsible for delivering its final message via whatever channel plugin(s) are configured (exposed via the `LEO_CHANNELS` env var). Leo does not inject a messaging protocol into the prompt.

## Claude Arguments

```
claude -p "<assembled prompt>" \
       --model <effective-model> \
       --max-turns <effective-max-turns> \
       --output-format text \
       --dangerously-skip-permissions \  # only if bypass_permissions: true
       --mcp-config <workspace>/config/mcp-servers.json  # if exists
```

The effective model and max turns are resolved via the [override cascade](../configuration/config-reference.md#override-cascade).

## Output

- Task output is logged to `<workspace>/state/<task>.log`
- If the agent outputs `NO_REPLY`, no external message is sent
- Otherwise, the agent delivers its final message via a configured channel plugin (e.g. Telegram, Slack) using the plugin's own MCP tool
- If `task.notify_on_fail: true` and the task exits non-zero, Leo spawns a short child `claude` invocation asking the agent to notify the task's configured channels of the failure

## See Also

- [Writing Tasks](../guides/writing-tasks.md) — how to create custom task prompts
- [Scheduling](../guides/scheduling.md) — cron expressions and timezone handling
- [`leo task`](task.md) — listing, adding, and inspecting scheduled tasks
