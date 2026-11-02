# Published Docker image

**Status:** draft · 2026-10-31

## Goal

Publish a generic, multi-arch image at `ghcr.io/blackpaw-studio/leo` so anyone can run a leo daemon plus supervised agents with `docker run` / compose. No deployment-specific config (Nomad, Vault, k8s) ships with it.

## Non-goals (v1)

- sshd in the image (remote attach, follow-lead, `leo agent move/copy` will not reach a containerized host unless the user adds it — documented).
- Language toolchains (Go, Rust, build-essential) — users extend via `FROM`.
- Unstable/nightly image tags.
- Per-agent sandboxing (agents in their own containers).

## Image contents

- Base: `debian:bookworm-slim`. leo binary from the goreleaser build (no in-image Go compile).
- PID 1: `tini`, so signals reach the daemon and tmux/agent zombies are reaped.
- Runtime: `tmux` (≥ 3.2), `git`, `gh`, `curl`, `jq`, `ripgrep`, `openssh-client`, `ca-certificates`, Node.js LTS + npm, `python3`.
- Harnesses: `claude`, `codex`, `opencode` CLIs — latest at release build time; auto-updaters disabled via env; installed versions recorded as OCI labels (`studio.blackpaw.leo.claude-version`, etc.).
- Non-root user `leo` (uid/gid 1000), `HOME=/home/leo`, `LANG=C.UTF-8` (tmux sanitizes output without a UTF-8 locale).
- One volume: `/home/leo` — holds `~/.leo` (config, state, chat DB), `~/.claude`, `~/.claude.json`, codex/opencode config, and agent workspaces.
- `EXPOSE 8370`.

## Entrypoint (`docker/entrypoint.sh`)

On every start:
1. If `~/.leo/leo.yaml` is missing, write a starter config: `web.enabled: true`, `web.bind: 0.0.0.0`, `web.port: 8370`, `web.allowed_hosts` from `LEO_ALLOWED_HOSTS` (comma-separated; default `localhost,127.0.0.1`), no tasks/templates. Existing files are never touched.
2. If `~/.claude.json` is missing, seed it with onboarding completed (same approach as `docs/demo/claude.seed.json`) so a fresh volume does not stall agents on first-run screens.
3. Run each executable file in `~/.leo/init.d/` in lexical order, as the `leo` user (non-executable files are skipped and named in a log line). A non-zero exit stops startup and the container exits with that code. Scripts run on every start, so they must be idempotent; installs into `$HOME` persist on the volume.
4. `exec leo service` (foreground; never `start --daemon`).

### User SDKs / toolchains

- The image ships `mise`, configured to install under `$HOME` so tools persist on the volume. Users can add `mise use -g go@1.25` to an init script or run it once with `docker exec`. No root is needed.
- System packages (`apt`) need root, so they belong in a `FROM` layer, not in init scripts. The docs say so explicitly. Init scripts never run as root.

Workspace-trust: agent workspaces under `/home/leo` get `hasTrustDialogAccepted` on first spawn. If leo's existing pre-accept only runs for a non-default `CLAUDE_CONFIG_DIR`, the plan must close that gap for the image (either set `CLAUDE_CONFIG_DIR=/home/leo/.claude` in the image or extend the pre-accept) — whichever is the smaller change; no new user-facing config.

## Auth

- Claude, subscription (Pro/Max), either: (a) `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`, passed as container env; or (b) interactive `docker exec -it <ctr> claude` → `/login`, credentials persisted in `~/.claude/.credentials.json` on the volume. API billing: `ANTHROPIC_API_KEY`. Codex: `OPENAI_API_KEY` or the user's mounted login. opencode: provider env per its docs.
- Must verify these reach agent processes (daemon env → tmux server → agent). If any does not, add it to leo's env forwarding (`internal/env`) with a test — this is the only expected Go change.

## Publishing

- goreleaser builds `linux/amd64` + `linux/arm64` images and a multi-arch manifest on each stable release.
- Tags: `vX.Y.Z`, `vX.Y`, `latest`.
- Images signed with cosign keyless, matching the existing checksum signing.
- `release.yml` gains `packages: write`, GHCR login, QEMU + buildx setup.

## Docs

`docs/install/docker.md` (or the repo's equivalent location): `docker run` and compose examples, the volume, env vars (auth, `LEO_ALLOWED_HOSTS`), web token login, extending with `FROM` (e.g. adding Go), and the no-sshd limitation.

## Verification

- CI smoke test (no credentials): build the image, run it with an empty volume, assert the starter `leo.yaml` is written, an `init.d` script runs before the daemon (and a failing one stops startup), the web port answers, the tmux server is up, and a second start leaves the config untouched. It must also pass with uid 1000 on a fresh volume.
- Live check before the first release (manual, with a real token): spawn a claude agent in the container, send it a message, and confirm it replies; restart the container and confirm the agent resumes its session with `--resume`; repeat once for codex. Exercise both Claude subscription paths (token env and in-container `/login` surviving a restart).

## Acceptance

`docker run -e CLAUDE_CODE_OAUTH_TOKEN=… -v leo:/home/leo -p 8370:8370 ghcr.io/blackpaw-studio/leo` starts a working daemon. An agent created through the web UI runs and replies, and it survives a container restart.
