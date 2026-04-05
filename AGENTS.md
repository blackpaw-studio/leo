# AGENTS.md

Leo is a Go CLI for setting up and managing Claude Code agents. Read PLAN.md for the full spec.

## Key Points

- Go, single binary, no runtime dependencies beyond `claude` CLI and `curl`.
- Not a daemon. System cron/launchd runs `leo run <task>`, which invokes `claude -p`.
- Config in `<workspace>/leo.yaml` — agent settings, telegram creds, per-task overrides (model, max-turns, topic, silent mode).
- `leo run` assembles prompts dynamically: silent preamble (optional) + prompt file + telegram curl protocol. The agent file itself does NOT contain the telegram protocol — it's injected only for scheduled tasks.
- Outbound Telegram uses `curl` to Bot API (injected into prompt). Inbound uses official channel plugin.
- Agent identity is a standard Claude Code subagent `.md` at `~/.claude/agents/<n>.md` with `memory: user`.
- Templates embedded via `embed.FS`.

## Build Order

1. Go module, main.go, subcommand dispatch (cobra or just flag sets)
2. Config types + YAML loading (`leo.yaml`)
3. `leo run <task>` — prompt assembly + claude invocation (core runtime)
4. `leo chat` — build and exec the interactive claude command
5. `leo cron install/remove/list` — crontab management with markers
6. `leo task list/add/enable/disable` — config manipulation
7. `leo setup` — interactive wizard
8. `leo migrate` — OpenClaw migration
9. Templates (embed)
10. README, Makefile, goreleaser config

## Style

- Use cobra for subcommands
- gopkg.in/yaml.v3 for YAML
- Embed templates with `embed.FS`
- Colored terminal output (fatih/color or similar)
- Interactive prompts: bufio.Scanner + simple helpers, or survey/huh library
- No external process management — just `os/exec` to shell out to `claude`
- Tests for config parsing, prompt assembly, cron generation

## OpenClaw Migration Reference

Migration reads from `~/.openclaw/` or `/Volumes/*/.openclaw/`:
- `workspace/SOUL.md`, `IDENTITY.md`, `AGENTS.md`, `TOOLS.md` → merge into agent `.md`
- `workspace/USER.md`, `MEMORY.md`, `HEARTBEAT.md` → copy
- `workspace/memory/*.md` → `daily/`
- `workspace/reports/*.md` → `reports/`, rewrite paths
- `cron/jobs.json` → parse, convert to tasks in leo.yaml
- `credentials/telegram-*.json`, `channels/telegram/.env` → telegram config
- All `.md` files: find/replace old workspace path with new
