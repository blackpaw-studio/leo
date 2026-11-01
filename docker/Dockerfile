# Published Leo image. Built by goreleaser (dockers_v2): the leo binary for
# each platform is in the build context at $TARGETPLATFORM/leo.
FROM node:22-bookworm-slim AS node

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# gh comes from GitHub's own apt repo; bookworm's package is years old.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git jq openssh-client procps python3 ripgrep tini tmux \
 && mkdir -p -m 755 /etc/apt/keyrings \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
 && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

# Node LTS + npm, copied from the official image (multi-arch, no extra apt repo).
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Harness CLIs. The release workflow resolves and passes exact versions so
# both architectures match and the image labels record what was installed.
#
# opencode is installed with --ignore-scripts and its binary placed by hand:
# its postinstall runs the binary to verify it, and that segfaults when this
# layer is built under QEMU (the arm64 half of the release build runs on an
# amd64 runner). The postinstall also picks the AVX2 or baseline x64 build from
# the *build* host's CPU; an image has to run anywhere, so x64 uses baseline.
ARG CLAUDE_VERSION=latest
ARG CODEX_VERSION=latest
ARG OPENCODE_VERSION=latest
ARG TARGETARCH
RUN npm install -g \
      "@anthropic-ai/claude-code@${CLAUDE_VERSION}" \
      "@openai/codex@${CODEX_VERSION}" \
 && npm install -g --ignore-scripts "opencode-ai@${OPENCODE_VERSION}" \
 && oc=/usr/local/lib/node_modules/opencode-ai \
 && case "$TARGETARCH" in \
      amd64) pkg=opencode-linux-x64-baseline ;; \
      arm64) pkg=opencode-linux-arm64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && install -m 755 "$oc/node_modules/$pkg/bin/opencode" "$oc/bin/opencode.exe" \
 && npm cache clean --force

# mise manages user toolchains; its data dir (~/.local/share/mise) lives on
# the volume.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh

RUN useradd --create-home --uid 1000 --user-group --shell /bin/bash leo

ENV HOME=/home/leo \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TERM=xterm-256color \
    DISABLE_AUTOUPDATER=1 \
    OPENCODE_DISABLE_AUTOUPDATE=1 \
    PATH=/home/leo/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/leo /usr/local/bin/leo
COPY --chmod=755 docker/entrypoint.sh /usr/local/bin/leo-entrypoint
COPY docker/claude.seed.json /usr/local/share/leo/claude.seed.json

VOLUME /home/leo
EXPOSE 8370
USER leo
WORKDIR /home/leo
ENTRYPOINT ["tini", "--", "/usr/local/bin/leo-entrypoint"]
