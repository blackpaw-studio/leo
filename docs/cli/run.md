# leo run

Execute a scheduled task once.

## Usage

```bash
leo run <task>
```

## Description

`leo run` is the entry point for scheduled task execution. It reads the config, finds the named task, assembles a prompt, and invokes `claude -p` in non-interactive mode. This is the same code path the daemon's in-process scheduler uses when a task fires.

You can also run it manually to test a task:

```bash
leo run heartbeat
```

## Prompt Assembly

Leo builds the final prompt by combining up to three parts:

1. **Silent preamble** (if `task.silent: true`) — instructs the agent to work without narration and output `NO_REPLY` if there's nothing to report
2. **Prompt file content** — the task's `prompt_file`, read from the workspace
3. **Telegram notification protocol** — injected at runtime with `curl` commands the agent uses to send messages, including topic routing if configured

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
- If the agent outputs `NO_REPLY`, no Telegram message is sent
- Otherwise, the agent uses the injected `curl` template to POST to the Telegram Bot API

## See Also

- [Writing Tasks](../guides/writing-tasks.md) — how to create custom task prompts
- [Scheduling](../guides/scheduling.md) — cron expressions and timezone handling
- [`leo task`](task.md) — listing, adding, and inspecting scheduled tasks
