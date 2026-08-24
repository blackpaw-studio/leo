<h1 align="center">🐈‍⬛ Leo</h1>

<p align="center">
  <em>Supervises Claude Code agents and schedules tasks.</em>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#what-leo-does">What it does</a> ·
  <a href="#cli">CLI</a> ·
  <a href="https://blackpaw-studio.github.io/leo/">Docs</a>
</p>

<p align="center">
  <a href="https://github.com/blackpaw-studio/leo/actions/workflows/ci.yml"><img src="https://github.com/blackpaw-studio/leo/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/blackpaw-studio/leo/releases/latest"><img src="https://img.shields.io/github/v/release/blackpaw-studio/leo" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
  <a href="https://goreportcard.com/report/github.com/blackpaw-studio/leo"><img src="https://goreportcard.com/badge/github.com/blackpaw-studio/leo?style=flat" alt="Go Report Card"></a>
</p>

---

<p align="center">
  <img src="docs/demo/leo-demo.gif" alt="leo demo" width="860">
</p>

Leo supervises long-running [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents — spawned from templates, restarted on crash — and runs cron-driven Claude tasks (which can inject into those agents). Manage it from the CLI, a browser, or any Claude Code channel plugin (Telegram, Slack, webhook, …).

## Install

**Homebrew** (recommended):

```bash
brew install --cask blackpaw-studio/tap/leo
```

**Shell installer:**

```bash
curl -fsSL leo.blackpaw.studio/install | sh
```

**Go:**

```bash
go install github.com/blackpaw-studio/leo/cmd/leo@latest
```

**Prerequisites:** authenticated [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code), `tmux` 3.2+ (required — Leo passes agent env via `new-session -e`, added in 3.2, and uses `display-popup`). Channel plugins (e.g. `claude plugin install telegram@claude-plugins-official`) are optional.

> Leo runs on its own tmux socket (`-L leo`) so your personal `tmux ls` stays clean. Inspect Leo's sessions directly with `tmux -L leo ls`. See [tmux Config](docs/guides/tmux-config.md) for recommended settings.

> **macOS Local Network privacy:** third-party tools spawned by an agent can be silently denied LAN access (connections fail with "no route to host") if macOS never got the chance to attribute the local-network operation to the signed `leo` binary and prompt for consent. Leo runs its tmux server in the foreground so agent processes inherit that consent grant once you've approved it. Run `leo doctor` to trigger the one-time Allow/Deny dialog and check the current grant state.

**Upgrading:** `leo update` replaces a tarball install in place and verifies the new release before swapping the binary. Homebrew users should run `brew upgrade --cask blackpaw-studio/tap/leo && leo service restart` instead — `leo update` detects the Homebrew install and prints these commands.

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
leo setup              # interactive: profile, workspace, first agent
leo service start      # start the daemon in the foreground
leo service start -d   # install as a launchd/systemd service
```

Open the dashboard at <http://127.0.0.1:8370>. For mobile or chat access, install a channel plugin and add its ID to the agent's `channels:` list.

## What Leo does

Two primitives, one daemon:

| Primitive | What it is |
|---|---|
| **Agents** | Spawned from reusable templates via CLI, web UI, or a channel — with or without a repo. Auto-restart with exponential backoff, own workspace/model/channels/permissions, each in its own tmux session. A long-lived assistant is just an agent that never stops: it persists in the agent store and auto-restores on daemon restart. |
| **Tasks** | Cron-driven non-interactive Claude runs. Prompt file + schedule. Optional retry, channel notify on failure. |

A web dashboard, a token-authed HTTP API, and a built-in MCP server (so every channel gets `/clear`, `/compact`, `/stop`, `/tasks`, `/agent`, `/agents` for free) all live in the same daemon.

### Agents / Templates

Templates are reusable blueprints — spawn an agent from one with or without a repo:

```yaml
templates:
  assistant:
    model: sonnet
    channels: [plugin:telegram@claude-plugins-official]
    harness_options:
      remote_control: true

  coding:
    model: sonnet
    workspace: ~/agents
    harness_options:
      permission_mode: auto
      remote_control: true
```

```bash
leo agent spawn assistant                                 # no repo — run the template as-is, agent named "assistant"
leo agent spawn coding                                    # same, for a repo-driven template
leo agent spawn coding --repo blackpaw-studio/leo --name demo
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/cache
leo agent attach demo                                     # full tmux attach
leo attach                                                # no name → interactive picker
leo attach demo --cc                                      # iTerm2 / WezTerm native tab via tmux control mode
leo agent stop feat-cache                                 # stop — always dormant, never deletes
leo agent delete feat-cache --delete-branch               # clean up the worktree + branch
```

A repo-less spawn (like `assistant` above) is how you run a long-lived, always-on assistant — it just keeps running, restarts on crash, and comes back after `leo service restart`.

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
    prod: { ssh: alice@leo.example.com }
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

Browser UI for agents, tasks, config, and cron previews. Binds to `127.0.0.1` by default.

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
| `leo status` | Overall snapshot — service, agents, tasks, templates, web |
| `leo validate` | Check config, prerequisites, workspace health |
| `leo doctor` | Diagnose local network and daemon health (macOS Local Network privacy) |
| `leo service start` / `stop` / `restart` / `logs` | Supervisor lifecycle |
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
