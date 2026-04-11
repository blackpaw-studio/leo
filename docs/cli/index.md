# CLI Reference

Leo provides commands for setup, process management, task scheduling, and agent dispatch.

## Command Overview

| Command | Description |
|---------|-------------|
| [`leo setup`](setup.md) | Interactive setup wizard |
| [`leo onboard`](onboard.md) | Guided first-time setup with prerequisite checks |
| [`leo service`](service.md) | Manage persistent Claude sessions and the daemon |
| [`leo run <task>`](run.md) | Run a scheduled task once |
| [`leo task`](task.md) | Manage scheduled tasks (list, add, remove, enable, disable, history) |
| [`leo process list`](#leo-process-list) | Show process states |
| [`leo status`](#leo-status) | Show overall status (service, processes, tasks, web UI) |
| [`leo validate`](#leo-validate) | Check config, prerequisites, and workspace health |
| [`leo config show`](#leo-config) | Display effective config with defaults applied |
| [`leo config edit`](#leo-config) | Edit config interactively |
| [`leo telegram topics`](#leo-telegram) | Discover forum topic IDs from recent messages |
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

## leo process list

Show the current state of all supervised processes (running, restarting, stopped) including restart counts.

## leo status

Display a summary of the service, all processes, scheduled tasks (with next run times), and web UI status.

## leo validate

Check that the config is valid, prerequisites are installed (claude CLI, tmux), and the workspace is healthy.

## leo config

- `leo config show` — display the effective config with defaults resolved
- `leo config edit` — open an interactive config editor

## leo telegram

- `leo telegram topics` — poll recent messages to discover forum topic IDs for your group
- `leo telegram test` — send a test message to verify bot connectivity

## leo session

- `leo session list` — show stored session ID mappings (process name to session UUID)
- `leo session clear [name]` — clear a specific session or all sessions

## leo logs

Tail service logs. Supports `-n/--tail` for line count and `-f/--follow` for streaming.

## leo update

Download the latest Leo binary from GitHub releases and refresh workspace template files (CLAUDE.md, skills, etc.).

## leo completion

Generate shell completion scripts for bash, zsh, or fish. Follow the printed instructions to install.
