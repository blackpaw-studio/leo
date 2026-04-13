# Remote CLI

Leo's `agent` subcommand is designed to be run from a laptop against a persistent leo host. No new daemon, listener, or auth layer — Leo shells out to `ssh` for every remote call and reuses whatever SSH setup you already have (`~/.ssh/config`, agent forwarding, MFA, jump hosts).

This guide walks through turning a fresh laptop into a client.

## Prerequisites

On the **server** (the machine that runs `leo service`):

- `leo` already installed and authenticated with Claude.
- `leo service start` running (so the daemon and web UI are up).
- SSH reachable from the client machine.

On the **client** (your laptop):

- `leo` binary installed (same version is safest).
- Working SSH access to the server — `ssh <host> echo ok` should print `ok`.

Leo does not need to be set up or have a daemon running on the client. The client `leo.yaml` only needs the `client` section below.

## Configure hosts

Edit `~/.leo/leo.yaml` on the client (create the file if it doesn't exist):

```yaml
client:
  default_host: prod
  hosts:
    prod:
      ssh: evan@leo.example.com
      ssh_args: ["-p", "2222"]
    dev:
      ssh: evan@devbox.local
```

- `ssh` is passed verbatim as the SSH target. Anything SSH itself resolves (Host aliases, ProxyJump, IdentityFile) works.
- `ssh_args` inserts extra flags between the target and the remote command. Handy for non-default ports or explicit identity files.
- `leo_path` overrides the remote binary path. Defaults to `$HOME/.local/bin/leo` (matches `install.sh`). Set this if the remote installed leo elsewhere, or if you see `command not found: leo` over SSH — non-interactive SSH shells don't source `.zshrc` so PATH additions there don't apply.
- `tmux_path` overrides the remote `tmux` path used by `agent attach` and `agent logs --follow`. Defaults to `tmux`. For macOS arm64 homebrew remotes set to `/opt/homebrew/bin/tmux`; for intel homebrew, `/usr/local/bin/tmux`. Same reason as `leo_path` — homebrew paths live in `.zprofile`/`.zshrc` which ssh command-mode doesn't load.
- `default_host` is optional — if set, commands without `--host` use it. Otherwise the first host in sorted order wins.

Verify the config parses:

```bash
leo config show
```

## First spawn

```bash
# On the client
leo agent spawn coding --repo blackpaw-studio/leo --name demo
# → spawned leo-demo (workspace: /home/evan/agents/demo)
#   attach with: leo agent attach leo-demo

leo agent list
# NAME      TEMPLATE  WORKSPACE            STATUS   RESTARTS
# leo-demo  coding    /home/evan/agents/demo  running  0

leo agent attach leo-demo
# (full tmux attach — same Claude TUI as running locally on the server)
# prefix + d to detach

leo agent logs leo-demo -n 50
leo agent stop leo-demo
```

Every non-attach subcommand runs `ssh <host> leo agent <subcommand>` under the hood. Attach runs `ssh -t <host> tmux attach -t leo-<name>` so terminal resizing and scrollback work normally.

## Attaching to supervised processes

Configured processes (the ones managed by `leo service` on the server) expose the same attach and logs commands:

```bash
leo process attach primary        # ssh -t <host> tmux attach -t leo-primary
leo process logs primary -n 100
leo process logs primary --follow
```

And `leo attach <name>` resolves against both processes and agents — handy when you don't want to remember which namespace a name lives in:

```bash
leo attach primary     # process? agent? both? Leo figures it out.
```

When a name exists in both namespaces, Leo errors rather than guessing and points you at the explicit `leo process attach` / `leo agent attach` forms. For remote hosts the resolution is deferred to the server so the client never needs to know the remote's process list.

## Overriding the target host

Precedence when resolving which host to talk to:

1. `--host NAME` flag
2. `LEO_HOST` environment variable
3. `client.default_host` in `leo.yaml`
4. First entry in `client.hosts` (sorted by key)
5. Localhost — only if no hosts are configured

If any hosts are configured, Leo treats the machine as a client. To bypass and dispatch through the local daemon socket, pass `--host localhost` explicitly.

```bash
leo agent list --host dev        # talk to dev
LEO_HOST=dev leo agent list      # same thing via env
leo agent list --host localhost  # force the local daemon
```

## Web UI and Telegram parity

The daemon on the server owns agent lifecycle. The CLI, web UI, and Telegram `/agent` command are all clients of the same manager — so an agent spawned from the CLI appears in the web dashboard immediately, and vice versa.

## Troubleshooting

**"no leo.yaml found — run 'leo setup' first"**
The client's `leo.yaml` doesn't exist. Create it with just the `client` section — no other setup needed on a client-only machine.

**"host \"prod\" not defined in client.hosts"**
Typo in `--host`, `LEO_HOST`, or `default_host`. Check `leo config show`.

**Attach hangs or shows "no sessions"**
The agent name is wrong, or the supervisor already reaped the tmux session. Check `leo agent list --host <host>`.

**Remote `leo` not on `$PATH`**
Leo expects `leo` to be on the server's default login PATH. Either install it there or add a symlink — SSH non-interactive shells do not load `.zshrc` by default.

## See Also

- [`leo agent`](../cli/agent.md) — subcommand reference
- [Agents guide](agents.md) — template authoring and Telegram/web parity
