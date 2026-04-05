# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Leo

Leo is a Go CLI that sets up and manages Claude Code agents as persistent, proactive personal assistants. It handles workspace setup, Telegram integration, and cron/launchd scheduling. After setup, system cron runs `claude` directly — Leo manages the config and cron entries, not a daemon.

Two runtime modes (both invoke stock `claude` CLI):
- **Interactive** (`leo chat`): long-running Telegram session via channel plugin
- **Scheduled** (`leo run <task>`): cron invokes claude with an assembled prompt; outbound Telegram uses curl (injected into prompt at runtime, not in agent file)

## Build & Test Commands

```bash
make build          # Build binary to bin/leo
make install        # go install
make test           # go test -race -cover ./...
make lint           # go vet + staticcheck
make snapshot       # goreleaser snapshot

# Run a single test
go test -race -run TestFunctionName ./internal/config/

# Coverage report
go test -race -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

Version is injected via ldflags: `-X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)`

## Architecture

```
cmd/leo/main.go          → cli.Execute() entry point
internal/cli/             → Cobra command definitions (root.go wires all subcommands)
internal/config/          → Config types + YAML loading/saving (leo.yaml)
internal/run/             → Task runner: prompt assembly (silent preamble + prompt file + telegram protocol) + claude invocation
internal/cron/            → Crontab management with marker-delimited blocks per agent
internal/telegram/        → Telegram Bot API helpers (test message, getUpdates polling)
internal/prompt/          → Interactive terminal helpers (colored prompts, yes/no, choice parsing)
internal/templates/       → embed.FS templates for agent personas, heartbeat, user profile
internal/setup/           → Setup wizard
internal/migrate/         → OpenClaw migration
internal/onboard/         → Onboarding flow
internal/prereq/          → Prerequisite checks (claude CLI, etc.)
```

Key design patterns:
- **Testability seams**: `run.execCommand` and `cron.readCrontab`/`cron.writeCrontab` are package-level vars replaced in tests
- **Config resolution**: `FindConfig()` walks up from cwd; task settings cascade from `defaults` with per-task overrides for model and max_turns
- **Cron markers**: blocks delimited by `# === LEO:<agent> ===` / `# === END LEO:<agent> ===` allow multiple agents in one crontab
- **Templates**: embedded via `//go:embed *.md` in `internal/templates/`, rendered with `text/template`

## Config

Workspace config lives at `<workspace>/leo.yaml`. Key sections: `agent` (name, workspace, agent_file), `telegram` (bot_token, chat_id, group_id, topics), `defaults` (model, max_turns), `tasks` (per-task schedule, prompt_file, overrides).

## Dependencies

- cobra for CLI subcommands
- gopkg.in/yaml.v3 for config
- fatih/color for terminal output
- Runtime: `claude` CLI (authenticated), `curl` (for Telegram in agent prompts)
