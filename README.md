<h1 align="center">🐈‍⬛ Leo</h1>

<p align="center">
  <em>A process supervisor and task scheduler for Claude Code.</em>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#what-leo-does">What it does</a> ·
  <a href="#cli">CLI</a> ·
  <a href="https://blackpaw-studio.github.io/leo/">Docs</a>
</p>

---

Leo keeps long-running [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions alive, runs cron-driven Claude tasks, and spawns on-demand coding agents from templates. Manage it from the CLI, a browser, or any Claude Code channel plugin (Telegram, Slack, webhook, …).

## Install

**Homebrew** (recommended):

```bash
brew install blackpaw-studio/tap/leo
```

**Shell installer:**

```bash
curl -fsSL leo.blackpaw.studio/install | sh
```

**Go:**

```bash
go install github.com/blackpaw-studio/leo/cmd/leo@latest
```

**Prerequisites:** authenticated [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code), `tmux`. Channel plugins (e.g. `claude plugin install telegram@claude-plugins-official`) are optional.

**Upgrading:** `leo update` replaces a tarball install in place and verifies the new release before swapping the binary. Homebrew users should run `brew upgrade blackpaw-studio/tap/leo && leo service restart` instead — `leo update` detects the Homebrew install and prints these commands.

<details>
<summary><strong>Verified install</strong> (Sigstore cosign)</summary>

Each release publishes `install.sh` with a `install.sh.sha256`:

```bash
VER=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
  https://github.com/blackpaw-studio/leo/releases/latest | awk -F/ '{print $NF}')
curl -fsSLO "https://github.com/blackpaw-studio/leo/releases/download/${VER}/install.sh"
curl -fsSLO "https://github.com/blackpaw-studio/leo/releases/download/${VER}/install.sh.sha256"
shasum -a 256 -c install.sh.sha256
sh install.sh
```

`leo update` itself verifies the release's [Sigstore cosign](https://docs.sigstore.dev/cosign/) signature against the release workflow's GitHub OIDC identity, then verifies the tarball SHA-256. Pre-signing releases can be installed with `--allow-unsigned` (or `LEO_ALLOW_UNSIGNED_RELEASE=1`); SHA-only verification with a warning. Will be removed once every supported release is signed.

Leo verifies the Fulcio keyless signature but does not consult Rekor. For transparency-log verification, run cosign manually:

```bash
VERSION=v0.3.2
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

</details>

## Quick Start

```bash
leo setup              # interactive: profile, workspace, first process
leo service start      # supervise processes in the foreground
leo service start -d   # install as a launchd/systemd service
```

Open the dashboard at <http://127.0.0.1:8370>. For mobile or chat access, install a channel plugin and add its ID to the process's `channels:` list.

## What Leo does

Three primitives, one daemon:

| Primitive | What it is |
|---|---|
| **Processes** | Long-running supervised Claude sessions. Auto-restart with exponential backoff. Each gets its own workspace, model, channels, and permissions. |
| **Templates → Agents** | Reusable blueprints for ephemeral agents. Spawn from CLI, web UI, or a channel. Each agent clones a repo into its own tmux session. |
| **Tasks** | Cron-driven non-interactive Claude runs. Prompt file + schedule. Optional retry, channel notify on failure. |

A web dashboard, a token-authed HTTP API, and a built-in MCP server (so every channel gets `/clear`, `/compact`, `/stop`, `/tasks`, `/agent`, `/agents` for free) all live in the same daemon.

### Processes

```yaml
processes:
  assistant:
    channels: [plugin:telegram@claude-plugins-official]
    remote_control: true
    enabled: true
```

### Agent templates

```yaml
templates:
  coding:
    model: sonnet
    permission_mode: auto
    workspace: ~/agents
    remote_control: true
```

```bash
leo agent spawn coding --repo blackpaw-studio/leo --name demo
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/cache
leo agent attach demo                                    # full tmux attach
leo agent stop feat-cache --prune --delete-branch        # stop + clean worktree
```

### Scheduled tasks

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

### Remote CLI

The same `leo` binary becomes a thin SSH client when `client.hosts` is set — manage agents on a remote leo host without leaving your laptop:

```yaml
client:
  default_host: prod
  hosts:
    prod: { ssh: evan@leo.example.com }
```

See the [Remote CLI guide](https://blackpaw-studio.github.io/leo/guides/remote-cli/).

### Channel plugins

Leo doesn't ship a messaging channel. Install any Claude Code channel plugin and reference its ID in `channels:`. The plugin owns its own auth and routing; Leo just hands the resolved list to the spawned Claude process via `--channels` flags.

For Telegram slash-command autocomplete:

```bash
leo channels register-commands telegram
```

### Web dashboard & API

```yaml
web:
  enabled: true
  port: 8370
```

Browser UI for processes, tasks, config, agents, and cron previews. Binds to `127.0.0.1` by default.

<details>
<summary><strong>Auth model</strong> (read this before exposing the daemon)</summary>

Two layered controls protect the daemon:

- **Host + Origin pinning** on every `/web/...` and `/api/...` route. Requests must target `127.0.0.1`, `localhost`, or `[::1]` on the configured port — or any hostname/IP listed in `web.allowed_hosts` (required when `web.bind` is non-loopback). Foreign `Host`/`Origin` → `403`. Blocks DNS rebinding and drive-by cross-origin POSTs.
- **Bearer-token auth** on every `/api/...` route. The daemon mints a 32-byte token on first start at `~/.leo/state/api.token` (mode `0600`). A valid token alone isn't enough — the request must also pass Host pinning.

> **Breaking change:** `/api/*` previously required no auth. Channel plugins must now send `Authorization: Bearer $(cat ~/.leo/state/api.token)` or get `401`.

```bash
TOKEN=$(cat ~/.leo/state/api.token)
curl -sH "Authorization: Bearer $TOKEN" http://127.0.0.1:8370/api/task/list
```

The token file is readable by any process running as the same Unix user — intentional, so co-tenant plugins can read it directly. Rotate by deleting the file and restarting the daemon.

</details>

## CLI

| Command | What it does |
|---|---|
| `leo setup` | Interactive setup wizard |
| `leo status` | Overall snapshot — service, processes, tasks, templates, web |
| `leo validate` | Check config, prerequisites, workspace health |
| `leo service start` / `stop` / `restart` / `logs` | Supervisor lifecycle |
| `leo process …` | `list`, `add`, `remove`, `enable`, `disable` |
| `leo task …` | `list`, `add`, `remove`, `enable`, `disable`, `history`, `logs` |
| `leo template …` | `list`, `show`, `remove` |
| `leo agent …` | `list`, `spawn`, `attach`, `stop`, `logs` (local or over SSH) |
| `leo run <task>` | Run a task once on demand |
| `leo config show` / `edit` | Inspect (`--raw`, `--json`) or edit the effective config |
| `leo update` | Self-update the binary |

Full reference: [blackpaw-studio.github.io/leo/cli](https://blackpaw-studio.github.io/leo/cli/).

## Documentation

- [Getting Started](https://blackpaw-studio.github.io/leo/getting-started/) — install, prereqs, first run
- [Configuration](https://blackpaw-studio.github.io/leo/configuration/) — full reference, workspace layout
- [CLI Reference](https://blackpaw-studio.github.io/leo/cli/) — every command and flag
- [Guides](https://blackpaw-studio.github.io/leo/guides/) — tasks, agents, scheduling, remote
- [Development](https://blackpaw-studio.github.io/leo/development/) — contributing, architecture, releases

## Development

```bash
make build      # → bin/leo
make test       # go test -race -cover ./...
make lint       # go vet + staticcheck
```

## License

MIT

---

Named for my void Leo. He's a good kitty.
