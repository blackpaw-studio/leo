# Docker

A multi-arch (`linux/amd64`, `linux/arm64`) image is published to GitHub Container Registry on every stable release:

```text
ghcr.io/blackpaw-studio/leo
```

Tags: `vX.Y.Z`, `vX.Y`, and `latest`. Prerelease and nightly builds are not published as images.

The image runs the Leo daemon (web UI, cron, agent supervision) as a non-root `leo` user (uid/gid 1000). It bundles `tmux`, `git`, `gh`, `curl`, `jq`, `ripgrep`, `openssh-client`, Node.js LTS, Python 3, [mise](https://mise.jdx.dev), and the `claude`, `codex` and `opencode` CLIs. Harness versions are the latest at release build time and are recorded as OCI labels (`studio.blackpaw.leo.claude-version`, `…codex-version`, `…opencode-version`). Their built-in auto-updaters are disabled (Codex via a seeded `~/.codex/config.toml`); pull a newer image to update.

## Quick start

```bash
docker run -d --name leo \
  -e CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-… \
  -v leo:/home/leo \
  -p 8370:8370 \
  ghcr.io/blackpaw-studio/leo
```

Open `http://localhost:8370` and sign in with the web token:

```bash
docker exec leo cat /home/leo/.leo/state/api.token
```

=== "docker compose"

    ```yaml
    services:
      leo:
        image: ghcr.io/blackpaw-studio/leo:latest
        restart: unless-stopped
        ports:
          - "8370:8370"
        volumes:
          - leo:/home/leo
        environment:
          CLAUDE_CODE_OAUTH_TOKEN: ${CLAUDE_CODE_OAUTH_TOKEN}
          # Every host or IP you browse from (see below).
          LEO_ALLOWED_HOSTS: localhost,127.0.0.1,leo.lan

    volumes:
      leo:
    ```

!!! warning "Publish the same port the daemon listens on"
    The web UI checks the `Host` header, including its port, against `localhost` and `LEO_ALLOWED_HOSTS` on the daemon port (8370). Map `8370:8370`. A different host port (`-p 9000:8370`) makes the browser send `Host: …:9000`, and the request is rejected with 403.

## The volume

Everything lives in one volume mounted at `/home/leo`: Leo config, state and chat database (`~/.leo`), `~/.claude`, `~/.claude.json`, Codex and opencode config, mise-installed toolchains, and agent workspaces. Keep the volume and the container is disposable.

On every start the entrypoint:

1. writes a starter `~/.leo/leo.yaml` **only if it is missing** (web UI on, bound to `0.0.0.0:8370`, `allowed_hosts` from `LEO_ALLOWED_HOSTS`, no tasks or templates). An existing file is never touched.
2. seeds `~/.claude.json` **only if it is missing**, marking onboarding complete and trusting `/home/leo` (Claude applies that trust to its subdirectories), so agents do not stall on first-run screens. It also seeds `~/.codex/config.toml` (update check off) if that is missing.
3. runs the scripts in `~/.leo/init.d/` (below).
4. starts `leo service` in the foreground under `tini`.

Add templates and tasks through the web UI or by editing `leo.yaml`, the same as on a host.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `LEO_ALLOWED_HOSTS` | Comma-separated hostnames/IPs the web UI accepts (default `localhost,127.0.0.1`). **Must include every host or IP you browse from**, such as a LAN IP. It is only read when the starter config is first written; afterwards edit `web.allowed_hosts` in `leo.yaml`. |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude Pro/Max subscription token from `claude setup-token`. |
| `ANTHROPIC_API_KEY` | Claude API billing instead of a subscription. On every start the entrypoint adds the key's last 20 characters to `customApiKeyResponses.approved` in `~/.claude.json` (nothing else is removed), so Claude does not stop on its "use this API key?" dialog. |
| `OPENAI_API_KEY` | Codex with an OpenAI API key. Codex's TUI ignores the variable unless it is logged in, so when `~/.codex/auth.json` is missing the entrypoint runs `codex login --with-api-key` with the key (nothing is logged). Existing credentials are never replaced. |
| opencode provider env | Whatever your opencode provider needs, per its documentation. |

The daemon passes its environment to the tmux server, so every agent inherits these.

## Signing in to Claude

Pick one:

- **Token:** run `claude setup-token` on any machine with a browser, then pass the result as `CLAUDE_CODE_OAUTH_TOKEN`.
- **Interactive login:** `docker exec -it leo claude`, then `/login`. Credentials are stored in `~/.claude/.credentials.json` on the volume and survive restarts.

For Codex, either set `OPENAI_API_KEY` (the entrypoint logs Codex in with it on first start, as described above) or run `docker exec -it leo codex login` for an interactive login. Both are stored in `~/.codex/auth.json` on the volume and survive restarts; a login already there is kept even if `OPENAI_API_KEY` is set.

## Init scripts and toolchains

Each executable file in `~/.leo/init.d/` runs in lexical order as the `leo` user on **every** container start, after config seeding and before the daemon. Non-executable files are skipped and named in the log. A script that exits non-zero stops startup, and the container exits with that code.

Scripts run on every start, so make them idempotent. Installs into `$HOME` persist on the volume:

```bash
#!/bin/sh
# ~/.leo/init.d/10-go.sh  (chmod +x)
mise use -g go@1.25
```

`mise` is preinstalled. It keeps its data under `~/.local/share/mise` (on the volume) and its shims are on `PATH`, so no root is needed. You can also run `docker exec leo mise use -g node@20` once by hand.

!!! note "System packages need a derived image"
    Init scripts never run as root, so `apt-get install` does not work there. Anything that needs root belongs in a `FROM` layer (below).

## Extending the image

Add system packages or pre-baked toolchains with your own Dockerfile:

```dockerfile
FROM ghcr.io/blackpaw-studio/leo:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends golang-go build-essential \
 && rm -rf /var/lib/apt/lists/*
USER leo
```

Keep `USER leo` last; the entrypoint expects to run as the non-root user.

## Limitations

- **No sshd.** The image does not run an SSH server, so remote attach, [follow-lead](../configuration/follow-lead.md) and [`leo agent move/copy`](../configuration/agent-move.md) cannot reach a containerized host unless you add one in a derived image. Attach locally with `docker exec -it leo leo attach …`.
- **No language toolchains** (Go, Rust, build-essential) are built in; use mise or a `FROM` layer.
- **Agents share the container.** There is no per-agent sandbox; agents are separate tmux sessions under the same user.
