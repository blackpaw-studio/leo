# CLI Reference

Leo provides commands for setup, process management, task scheduling, template management, and agent dispatch.

## Command Overview

| Command | Description |
|---------|-------------|
| [`leo setup`](setup.md) | Interactive setup wizard |
| [`leo service`](service.md) | Manage persistent Claude sessions and the daemon |
| [`leo process`](process.md) | List, add, remove, enable, or disable supervised processes |
| [`leo task`](task.md) | Manage scheduled tasks (list, add, remove, enable, disable, history, logs) |
| [`leo template`](template.md) | Inspect and remove agent templates |
| [`leo agent`](agent.md) | Spawn and control ephemeral agents (local or via SSH) |
| [`leo attach <name>`](#leo-attach) | Attach to a supervised process or running agent |
| [`leo run <task>`](run.md) | Run a scheduled task once |
| [`leo status`](#leo-status) | Show overall status (service, processes, tasks, templates, web UI) |
| [`leo validate`](#leo-validate) | Check config, prerequisites, and workspace health |
| [`leo config show`](#leo-config) | Display effective config with defaults applied |
| [`leo config edit`](#leo-config) | Edit config interactively |
| [`leo session list`](#leo-session) | List stored session mappings |
| [`leo session clear`](#leo-session) | Clear stored session(s) |
| [`leo logs`](#leo-logs) | Tail service or process logs |
| [`leo update`](#leo-update) | Self-update binary and refresh workspace files |
| [`leo completion`](#leo-completion) | Generate shell completion script (bash/zsh/fish) |
| [`leo version`](version.md) | Print version |

## Global Flags

```
-c, --config <path>       Path to leo.yaml
```

### Config Auto-Detection

If `--config` is not specified, Leo walks up from the current working directory looking for a `leo.yaml` file. If none is found, it falls back to `~/.leo/leo.yaml`.

## leo status

Display a summary of the service, daemon, web UI, processes (with per-process runtime state from the daemon), scheduled tasks, templates, and the next upcoming task run.

Pass `--json` to emit the structured `StatusReport` document for scripting.

## leo validate

Check that the config parses, prerequisites are installed (`claude`, `tmux`), workspaces exist, prompt files referenced by tasks resolve, and cron schedules are valid. Findings are ranked by severity (ERROR, WARN, INFO) and the tally is printed at the end. Critical warnings are also surfaced automatically when the service starts.

Pass `--json` to emit findings as a structured JSON document (handy for CI checks and scripting).

## leo config

- `leo config show` — display the effective config with defaults applied.
    - `--raw` prints the YAML file verbatim, skipping default resolution.
    - `--json` prints the resolved config as indented JSON (handy for piping into `jq`). Mutually exclusive with `--raw`.
- `leo config edit` — open `leo.yaml` in `$EDITOR` (or `vi`).

## leo session

- `leo session list` — show stored session ID mappings (process name to session UUID). Pass `--json` for a machine-readable array.
- `leo session clear <name>` — clear a specific session. Pass `--all` to clear every stored session, or `-y / --yes` to skip the confirmation prompt.

## leo logs

Tail service logs. Supports `-n/--tail` for line count and `-f/--follow` for streaming.

## leo update

Download the latest Leo binary from GitHub releases and refresh workspace template files (CLAUDE.md, skills, etc.).

## leo completion

Generate shell completion scripts for bash, zsh, or fish. Task, process, and template names support tab-completion across the CLI once completions are installed.

## leo attach

Shortcut for `leo process attach` or `leo agent attach`. Given a single name, Leo resolves against both namespaces:

- If only a configured process matches, Leo attaches to its tmux session.
- If only a running agent matches, Leo attaches to the agent's tmux session.
- If both match, Leo refuses to guess and asks you to use the explicit subcommand.
- If neither matches, Leo returns an error.

When `--host` targets a remote, Leo delegates the whole resolution to the server by shelling `ssh -t <host> leo attach <name>` — the client does not need to know the remote's process list.

See [`leo process attach`](process.md#leo-process-attach-name) and [`leo agent attach`](agent.md#leo-agent-attach-name) for the explicit forms.
