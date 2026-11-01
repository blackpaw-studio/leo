# Implementation plan: published Docker image

**Spec:** `docs/specs/2026-10-31-docker-image.md` (the source of truth) · **Branch:** `feat/docker-image`

## Verified facts this plan relies on
- **Credentials should reach agents without a Go change.** In the container, `leo service` is PID 1's child and keeps the full container environment. The tmux server (`tmux/server.go:149`) inherits it, and so does the `new-session` client (`createCmd.Env = os.Environ()`, `service/process.go`). tmux seeds each new session from the server's global env. `env.Capture()` (`internal/env`) only feeds launchd/systemd unit generation, so it is not on this path. Step 2 checks this against a real pane rather than a mocked seam.
- **Setting `CLAUDE_CONFIG_DIR=/home/leo/.claude` does not close the trust gap.** `preLaunch` (`harness/claude/hooks.go`) returns early when the dir equals `$HOME/.claude`. Any other value moves the state file to `$CLAUDE_CONFIG_DIR/.claude.json`, which breaks the spec's `~/.claude.json` layout. Step 1 decides between the two remaining fixes.
- **goreleaser 2.15.3 supports `dockers_v2`.** It takes `images`, `tags`, `platforms`, `labels`, `annotations`, `build_args`, `extra_files`, `disable` and `sbom`. It does a single buildx multi-platform build, so `dockers` + `docker_manifests` aren't needed. Binaries land in the build context at `$TARGETPLATFORM/leo`. The action's `~> v2` already resolves to ≥ 2.12.
- **`release.yml` also runs on prerelease tags (`v*-…`), so the docker block needs to be disabled for those.** Snapshot builds use `.goreleaser.prerelease.yaml` and `.goreleaser.unstable.yaml`, which never get a docker block. `ci.yml` is `workflow_call` only, and prerelease (every PR), unstable (main) and release (tags) all call it.
- **The `leo service` foreground path is supervised mode,** so the old demo's `--supervised` flag isn't needed. Debian bookworm ships tmux 3.3a (≥ 3.2), but its `nodejs` is 18, which is end-of-life.

## Steps

**1. Probe Claude's trust lookup (explore dispatch, read-only, no code).**
- Question: does `hasTrustDialogAccepted` on a parent dir (`/home/leo`) cover subdirectories? Grep the installed `@anthropic-ai/claude-code` bundle for the trust check and see whether it walks parent paths.
- If yes (fix A): the seed in step 4 trusts `/home/leo` and step 5 is skipped. No Go change.
- If no (fix B): step 5 extends `preLaunch`. This is a behaviour change on every host, so it needs sign-off (see open question 1).
- Check: a written finding with the file and the matching snippet.

**2. Write the smoke test first: `docker/smoke-test.sh` (+ `docker/smoke/leo.yaml`).**
- The script builds `linux/$(go env GOARCH)/leo` with `CGO_ENABLED=0` into a temp context, copying the layout `dockers_v2` produces. It then runs `docker build -f docker/Dockerfile`.
- It fails now because there is no Dockerfile.
- Phase A, fresh named volume with `--user 1000:1000`:
  - an `init.d` script that writes a marker file exists before the daemon socket appears
  - `~/.leo/leo.yaml` is created with `bind 0.0.0.0`, `port 8370`, and `allowed_hosts` taken from `LEO_ALLOWED_HOSTS=foo,localhost`
  - `~/.claude.json` is seeded
  - `curl -fsS localhost:8370/login` returns 200 (`-H Host: localhost`)
  - `docker exec … tmux -L leo ls` exits 0 (the tmux server is up)
  - `docker restart`, then the `leo.yaml` sha256 is unchanged and the marker script ran again
  - a non-executable `init.d` file is named in the logs
- Phase B, new volume: a failing `init.d` script (`exit 7`) makes the container exit with code 7, and the daemon never starts.
- Phase C: run with dummy `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`, and pre-place `docker/smoke/leo.yaml`, which has one claude template. Spawn the agent and read `/proc/$(tmux display -p -t … '#{pane_pid}')/environ`, retrying while the pane is alive. All three vars must be present.
- Verify: `bash docker/smoke-test.sh`. It is red until steps 3–4 land, and phase C may stay red until step 6.

**3. `docker/Dockerfile` (no test-first: it's packaging, checked by the smoke test).**
- Base and packages:
  - `FROM debian:bookworm-slim`
  - apt installs `tini tmux git gh curl jq ripgrep openssh-client ca-certificates python3 procps`
- Node: `COPY --from=node:22-bookworm-slim` to get Node LTS + npm (multi-arch, no NodeSource repo).
- Harness CLIs:
  - `ARG CLAUDE_VERSION=latest CODEX_VERSION=latest OPENCODE_VERSION=latest`
  - `npm i -g @anthropic-ai/claude-code@$CLAUDE_VERSION @openai/codex@$CODEX_VERSION opencode-ai@$OPENCODE_VERSION`
- mise: installed to `/usr/local/bin/mise`. Its data dir defaults to `~/.local/share/mise`, which is on the volume.
- User and environment:
  - `useradd -u 1000 -U leo`
  - `ENV HOME=/home/leo LANG=C.UTF-8 LC_ALL=C.UTF-8 TERM=xterm-256color`
  - auto-updater env: `DISABLE_AUTOUPDATER=1`, `OPENCODE_DISABLE_AUTOUPDATE=1`, and codex's equivalent (open question 3)
  - `PATH` includes `~/.local/share/mise/shims`
- leo binary: `ARG TARGETPLATFORM` then `COPY $TARGETPLATFORM/leo /usr/local/bin/leo`.
- Entrypoint files: `COPY docker/entrypoint.sh docker/claude.seed.json` (shipped via `extra_files`).
- Runtime: `VOLUME /home/leo`, `EXPOSE 8370`, `USER leo`, `ENTRYPOINT ["tini","--","/usr/local/bin/leo-entrypoint"]`.

**4. `docker/entrypoint.sh` + `docker/claude.seed.json`.**
- Uses `set -euo pipefail`.
- Writes `leo.yaml` only if it is missing: a heredoc, with `LEO_ALLOWED_HOSTS` split on commas into a YAML list, and every host validated against `^[A-Za-z0-9.:\[\]-]+$`.
- Copies the seed to `~/.claude.json` only if that file is missing. The seed is the demo seed minus the `/home/demo` paths, plus `/home/leo` if step 1 chose fix A.
- Runs `init.d` with `for f in "$HOME"/.leo/init.d/*` in glob (lexical) order: executable files run and abort startup with their exit code, and other files are logged as skipped.
- Ends with `exec leo service`.
- Verify: `bash docker/smoke-test.sh`. Phases A and B go green.

**5. (Fix B only) Trust pre-accept for the default account.**
- Files: `internal/harness/claude/hooks.go` `preLaunch`, plus `hooks_test.go`.
- First write a RED test: a default-config-dir env whose workspace is untrusted. Today it is left untrusted; after the change its `hasTrustDialogAccepted` should be true.
- Verify: `go test -race -run TestPreLaunch ./internal/harness/claude/`.

**6. (Only if phase C is red) Env forwarding.**
- Add the missing key wherever the real path drops it. Expected: nothing to add.
- Per the spec, use `internal/env` plus a test there. If the drop is actually in `sessionEnvArgs`, fix it there and report back.

**7. Publishing.**
- `.goreleaser.yaml`:
  - `dockers_v2: [{ids:[leo], images:[ghcr.io/blackpaw-studio/leo], dockerfile: docker/Dockerfile, platforms:[linux/amd64,linux/arm64], tags:["v{{.Version}}","v{{.Major}}.{{.Minor}}","latest"], disable: "{{ .Prerelease }}", extra_files:[docker/entrypoint.sh, docker/claude.seed.json], build_args:{CLAUDE_VERSION: "{{ .Env.CLAUDE_VERSION }}", …}, labels:{studio.blackpaw.leo.claude-version: "{{ .Env.CLAUDE_VERSION }}", …codex/opencode, org.opencontainers.image.source/version/revision}}]`
  - `docker_signs: [{cmd: cosign, artifacts: <images|manifests — open question 4>, args: ["sign","${artifact}@${digest}","--yes"]}]`
- `release.yml`:
  - add `packages: write`
  - pinned-SHA `docker/setup-qemu-action`, `docker/setup-buildx-action` and `docker/login-action` (`ghcr.io`, `GITHUB_TOKEN`)
  - a "Resolve harness versions" step that runs `npm view <pkg> version` for each CLI and writes the results to `$GITHUB_ENV` before goreleaser. Both architectures then get the same pinned versions, and the labels match what was installed.
- Verify:
  - `goreleaser check`
  - `goreleaser release --snapshot --clean --skip=sign` with the three version env vars set locally, then `docker image ls` shows both architectures
  - after the first tag: `cosign verify ghcr.io/blackpaw-studio/leo:vX.Y.Z --certificate-identity-regexp 'github.com/blackpaw-studio/leo/.github/workflows/release.yml@.*' --certificate-oidc-issuer https://token.actions.githubusercontent.com`

**8. CI smoke job.**
- New `docker-smoke` job in `ci.yml` on ubuntu-latest (amd64 only, no credentials). It runs `bash docker/smoke-test.sh` after `setup-go` and `setup-buildx`.
- Because it lives in `ci.yml`, it runs on every PR (prerelease), every push to main (unstable), and gates `release.yml` through `needs: ci`.
- Use the buildx gha cache for the apt/npm layers.

**9. Docs.**
- New page `docs/getting-started/docker.md`, the repo's equivalent of the spec's `docs/install/`.
- Add a nav entry in `mkdocs.yml` under Getting Started → "Docker: getting-started/docker.md", after Installation, and a link from `installation.md`.
- Content:
  - `docker run` and compose examples, and the volume
  - environment variables: the two Claude subscription paths, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, opencode provider env, and `LEO_ALLOWED_HOSTS` (it must include the host or IP you browse from)
  - web token login
  - `init.d` and mise (idempotent scripts; apt goes in a `FROM` layer, not in init scripts)
  - extending with `FROM` (adding Go as the example)
  - the no-sshd limitation (remote attach, follow-lead and agent move/copy don't reach the container)
- Verify: `mkdocs build --strict` (as in `docs.yml`).

**10. Gates.**
- Fresh-context `review` of `git diff main...HEAD`. Bar: P0/P1 plus anything a realistic `docker run` user hits.
- `make test` and `make lint`. Run `make e2e` only if step 5 or 6 changed Go code, and only via `env -i … TMUX_TMPDIR=$(mktemp -d) make e2e`, never from a dispatch pane.
- Before the first release, a manual live check with a real token, per the spec: claude reply, restart and `--resume`, codex once, both subscription paths.

## Risks and open questions (decisions the spec didn't settle)
1. **Trust fix (Step 1 decides).** Fix A, seeding `/home/leo`, needs no Go change but depends on Claude's parent-walk behaviour, which is undocumented and could change. Fix B auto-trusts agent workspaces on every non-container host too, which is a security-relevant behaviour change. If step 1 lands on B, it needs Evan's sign-off before step 5.
2. **Node source.** Bookworm's Node 18 is EOL and codex may need ≥ 22. The plan uses `node:22-bookworm-slim` (Node 22 LTS), which the spec doesn't name.
3. **Codex auto-update.** I couldn't confirm codex has an env switch for this. It may need `check_for_update_on_startup = false` in `~/.codex/config.toml`, written by the entrypoint only if the file is missing. That is a small addition beyond the spec's "via env".
4. **cosign artifact kind with `dockers_v2`.** It's unclear whether `docker_signs` should target `images` or `manifests` for a buildx multi-platform push. Confirm against the goreleaser 2.15 docs before tagging; the post-release `cosign verify` is the backstop.
5. **`extra_files` path layout.** I haven't confirmed that the build context preserves `docker/…` paths. The snapshot build in step 7 will show it.
6. **Phase C flakiness.** Claude may exit quickly with a dummy token, and the supervisor's restart loop then replaces the pane. The test reads `environ` in a retry loop, and asserting on the tmux global env is the fallback.
7. **Defaults that might surprise users.** The default `allowed_hosts` (`localhost,127.0.0.1`) rejects LAN-IP access unless `LEO_ALLOWED_HOSTS` is set, so the docs must say so prominently. arm64 is only built at release time; CI smoke covers amd64 only.
8. **`gh` version.** Bookworm's `gh` is 2.23, which is old. The spec doesn't pin it, so the plan uses the apt package; GitHub's own apt repo is the alternative if it proves too old.

## Orchestrator decisions
- Q2: use `node:22-bookworm-slim` as the Node source. Accepted, since the spec only requires Node LTS.
- Q3: if codex has no env switch, the entrypoint seeds `~/.codex/config.toml` only when it is missing. Accepted.
- Q8: install `gh` from GitHub's official apt repo, not bookworm's 2.23.
- Q1: if step 1 rules out fix A, STOP and report before step 5. Fix B changes behaviour on every host, so it needs Evan's sign-off.
- Q4/Q5: settle them using the goreleaser docs and the snapshot build in step 7.
