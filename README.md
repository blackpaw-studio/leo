<h1 align="center">Leo</h1>

<p align="center">
  <em>A process supervisor and task scheduler for Claude Code</em>
</p>

<p align="center">
  <a href="#install">Install</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#features">Features</a> &middot;
  <a href="https://blackpaw-studio.github.io/leo/">Documentation</a>
</p>

---

Leo manages persistent [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions, schedules autonomous tasks, and lets you spawn on-demand coding agents. Bring your own messaging channel — Leo is channel-agnostic and works with any Claude Code channel plugin (Telegram, Slack, webhook, etc.). A built-in web dashboard lets you manage everything from a browser.

## Install

Each release publishes `install.sh` with a matching `install.sh.sha256`. The verified flow:

```bash
VER=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/blackpaw-studio/leo/releases/latest | awk -F/ '{print $NF}')
curl -fsSLO "https://github.com/blackpaw-studio/leo/releases/download/${VER}/install.sh"
curl -fsSLO "https://github.com/blackpaw-studio/leo/releases/download/${VER}/install.sh.sha256"
shasum -a 256 -c install.sh.sha256
sh install.sh
```

This is the quick path. For a verified install, see above.

```bash
curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
```

Or with Homebrew:

```bash
brew install blackpaw-studio/tap/leo
```

Or with Go: `go install github.com/blackpaw-studio/leo@latest`

**Prerequisites:** [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) (authenticated), `tmux`. Optionally, any Claude Code channel plugin you want Leo to surface messages through (e.g. `claude plugin install telegram@claude-plugins-official`).

**Upgrading:** `leo update` replaces a tarball install in place. If you installed via Homebrew, run `brew upgrade blackpaw-studio/tap/leo && leo service restart` instead — `leo update` detects the Homebrew install and prints these commands for you. Workspace templates (`CLAUDE.md`, `skills/*.md`) re-sync automatically on every daemon start, so the `--workspace-only` flag from v0.1.0 has been removed.

Before replacing the binary, `leo update` verifies the release's [Sigstore cosign](https://docs.sigstore.dev/cosign/signing/signing_with_blobs/) signature against the release workflow's GitHub OIDC identity, then verifies the SHA-256 of the downloaded tarball. Releases published before signing was introduced can still be installed by passing `--allow-unsigned` (or setting `LEO_ALLOW_UNSIGNED_RELEASE=1`); this falls back to SHA-only verification with a warning and will be removed once every supported release is signed.

Leo verifies the Fulcio keyless signature but does not consult Rekor (the Sigstore transparency log). If a past signing key was ever exposed while its certificate was valid, a replayed signature would still verify here. Manual verification with `cosign verify-blob --rfc3161-timestamp` (or `cosign verify-blob` with an explicit `--rekor-url`) adds that additional check if you want it.

To verify a release manually with the [`cosign` CLI](https://docs.sigstore.dev/cosign/installation/):

```bash
VERSION=v0.5.0
curl -fsSL -O https://github.com/blackpaw-studio/leo/releases/download/$VERSION/checksums.txt
curl -fsSL -O https://github.com/blackpaw-studio/leo/releases/download/$VERSION/checksums.txt.sig
curl -fsSL -O https://github.com/blackpaw-studio/leo/releases/download/$VERSION/checksums.txt.pem

cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/$VERSION" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt
```

## Quick Start

```bash
leo setup          # interactive wizard — profile, workspace, first process
leo service start  # start supervised processes
```

If you want mobile access, install a channel plugin separately (e.g. Telegram) and add its ID to your process `channels:` list.

Run `leo service start --daemon` to install as a system service that persists across reboots.

## Features

### Processes

Long-running Claude sessions supervised with auto-restart and exponential backoff. Each process gets its own workspace, model, channel plugin list, and permissions. Enable remote control for claude.ai/code.

```yaml
processes:
  assistant:
    channels: [plugin:telegram@claude-plugins-official]
    remote_control: true
    enabled: true
```

### Agent Templates

Define reusable blueprints for spawning ephemeral coding agents. Dispatch them from a channel plugin (if it exposes agent commands) or from the web UI. Agents clone the repo, run in their own tmux session, and show up in claude.ai with a named session.

```yaml
templates:
  coding:
    model: sonnet
    remote_control: true
    permission_mode: bypassPermissions
    workspace: ~/agents
```

### Remote CLI

The `leo` binary is dual-purpose. On a server it supervises processes and runs tasks. On a laptop, with a `client.hosts` section in `leo.yaml`, it becomes a thin remote client that manages and attaches to agents on a leo host over SSH.

```yaml
client:
  default_host: prod
  hosts:
    prod:
      ssh: evan@leo.example.com
```

```bash
leo agent spawn coding --repo blackpaw-studio/leo --name demo
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/cache  # dedicated git worktree
leo agent attach demo     # full tmux attach to the remote Claude TUI
leo agent list
leo agent stop feat-cache --prune --delete-branch                        # stop + clean up worktree
```

See the [Remote CLI guide](https://blackpaw-studio.github.io/leo/guides/remote-cli/).

### Scheduled Tasks

Cron-driven tasks that invoke Claude in non-interactive mode. Write a prompt file, set a schedule, and Leo handles the rest. Tasks can run silently, retry on failure, and notify a configured channel on failure (via `notify_on_fail`).

```yaml
tasks:
  daily-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: prompts/daily-briefing.md
    model: opus
    channels: [plugin:telegram@claude-plugins-official]
    notify_on_fail: true
    enabled: true
```

### Web Dashboard

Monitor processes, manage tasks, edit config, spawn agents, and preview cron schedules from a browser on your LAN.

```yaml
web:
  enabled: true
  port: 8370
```

#### Web UI and API authentication

**Breaking change:** `/api/*` previously required no auth. Channel plugins must now send `Authorization: Bearer $(cat ~/.leo/state/api.token)` or they will receive a 401.

The web UI binds to `127.0.0.1` by default. To prevent a malicious webpage from driving the Claude REPL or mutating config via the browser, Leo enforces two controls:

- **Host + Origin pinning on every `/web/...` route.** Requests must have `Host` and (when present) `Origin` pointing at `127.0.0.1`, `localhost`, or `[::1]` on the configured port. Requests with a foreign `Host` or `Origin` header get `403 forbidden host` / `403 forbidden origin`. This blocks DNS-rebinding and drive-by cross-origin POSTs.
- **Bearer-token auth on every `/api/...` route.** On first start, the daemon mints a 32-byte random token and writes it to `~/.leo/state/api.token` (mode `0600`). The same Host pinning also applies to `/api/...`, so a valid token alone is not enough — the request must still look local.

To hit the API from a channel plugin or another local tool:

```bash
TOKEN=$(cat ~/.leo/state/api.token)
curl -sH "Authorization: Bearer $TOKEN" http://127.0.0.1:8370/api/task/list
```

The token file is readable by any process running as the same Unix user, which is intentional — channel plugins that need API access simply read it themselves. Rotate the token by deleting the file and restarting the daemon; a new one is generated automatically.

### Channel Plugins

Leo itself does not ship a messaging channel. Install any Claude Code channel plugin and reference its ID in a process or task `channels:` list. Leo passes the list to the spawned Claude process via `LEO_CHANNELS`; the plugin owns its own auth and routing.

Popular channel plugins:

- `telegram@claude-plugins-official` — Telegram bot with reply/reaction/topic tools
- (plus any other Claude Code plugin that exposes messaging tools)

### Built-in slash commands (every channel)

Leo ships an MCP server that gives every channel plugin a universal command set — no plugin changes required:

| Command       | Effect                                                    |
|---------------|-----------------------------------------------------------|
| `/clear`      | Clear the supervised Claude's conversation context        |
| `/compact`    | Compact the conversation context                          |
| `/stop`       | Interrupt the current operation                           |
| `/tasks`      | List scheduled tasks                                      |
| `/agent`      | Pick a template and spawn an ephemeral agent              |
| `/agents`     | List running ephemeral agents                             |

The supervised Claude recognizes these inbound from any channel and dispatches them via the `leo_*` MCP tools. Stock channel plugins (Anthropic's official Telegram, future Slack, etc.) work unmodified.

For Telegram autocomplete, register the commands once with the Bot API:

```
leo channels register-commands telegram
```

(Resolves the bot token from `TELEGRAM_BOT_TOKEN` or the plugin's `.env` file.)

## CLI

| Command | Description |
|---|---|
| `leo setup` | Interactive setup wizard |
| `leo service start` / `stop` / `restart` / `logs` | Manage the supervisor |
| `leo process list` / `add` / `remove` / `enable` / `disable` | Manage supervised processes |
| `leo task list` / `add` / `remove` / `enable` / `disable` | Manage scheduled tasks |
| `leo task history` / `logs` | Inspect task runs and log output |
| `leo template list` / `show` / `remove` | Inspect and remove agent templates |
| `leo agent list` / `spawn` / `attach` / `stop` / `logs` | Spawn and control ephemeral agents (local or over SSH) |
| `leo run <task>` | Run a task once |
| `leo status` | Overall status (service, processes, tasks, templates, web UI) |
| `leo validate` | Check config, prerequisites, and workspace health |
| `leo config show` | Display effective config (supports `--raw`, `--json`) |
| `leo config edit` | Edit config interactively |
| `leo update` | Self-update binary |

See the [CLI reference](https://blackpaw-studio.github.io/leo/cli/) for full details.

## Documentation

- [Getting Started](https://blackpaw-studio.github.io/leo/getting-started/) &mdash; installation, prerequisites, first run
- [Configuration](https://blackpaw-studio.github.io/leo/configuration/) &mdash; full config reference and workspace structure
- [CLI Reference](https://blackpaw-studio.github.io/leo/cli/) &mdash; every command and flag
- [Guides](https://blackpaw-studio.github.io/leo/guides/) &mdash; writing tasks, agents, scheduling, background mode
- [Development](https://blackpaw-studio.github.io/leo/development/) &mdash; contributing, architecture, releasing

## Development

```bash
make build      # Build to bin/leo
make test       # Run tests with race detector
make lint       # go vet + staticcheck
```

## License

MIT
