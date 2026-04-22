# syntax=docker/dockerfile:1.7
# Demo image: builds leo from source, bundles tmux + asciinema + agg + a
# stock Claude Code install, and runs a scripted demo against a controlled
# leo.yaml to produce a reproducible .cast and .gif.

# --- stage 1: build leo -----------------------------------------------------
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=demo
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X github.com/blackpaw-studio/leo/internal/cli.Version=${VERSION}" \
      -o /out/leo ./cmd/leo

# --- stage 2: runtime -------------------------------------------------------
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    TERM=xterm-256color \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      tmux \
      jq \
      procps \
      locales \
      xz-utils \
      fonts-dejavu-core \
      nodejs \
      npm \
    && sed -i 's/^# *\(en_US.UTF-8\)/\1/' /etc/locale.gen \
    && locale-gen \
    && rm -rf /var/lib/apt/lists/*

# asciinema v3 (Debian's `asciinema` package is v2 and lacks --window-size)
RUN ARCH=$(dpkg --print-architecture) \
    && case "$ARCH" in \
         amd64)  ASCIINEMA_TARGET=x86_64-unknown-linux-gnu ;; \
         arm64)  ASCIINEMA_TARGET=aarch64-unknown-linux-gnu ;; \
         *) echo "unsupported arch: $ARCH"; exit 1 ;; \
       esac \
    && curl -fsSL -o /usr/local/bin/asciinema \
        "https://github.com/asciinema/asciinema/releases/download/v3.2.0/asciinema-${ASCIINEMA_TARGET}" \
    && chmod +x /usr/local/bin/asciinema

# agg (asciinema -> gif)
RUN ARCH=$(dpkg --print-architecture) \
    && case "$ARCH" in \
         amd64)  AGG_TARGET=x86_64-unknown-linux-gnu ;; \
         arm64)  AGG_TARGET=aarch64-unknown-linux-gnu ;; \
         *) echo "unsupported arch: $ARCH"; exit 1 ;; \
       esac \
    && curl -fsSL -o /usr/local/bin/agg \
        "https://github.com/asciinema/agg/releases/download/v1.7.0/agg-${AGG_TARGET}" \
    && chmod +x /usr/local/bin/agg

# Claude Code CLI — demo runs it unauthenticated; leo spawns it and tmux
# shows whatever it renders on first launch.
RUN npm install -g @anthropic-ai/claude-code \
    && rm -rf /root/.npm

# Demo user (non-root so workspace paths look normal in the recording)
RUN useradd --create-home --shell /bin/bash demo
USER demo
WORKDIR /home/demo

# leo binary + demo config
COPY --from=builder --chown=demo:demo /out/leo /home/demo/.local/bin/leo
ENV PATH="/home/demo/.local/bin:${PATH}"

COPY --chown=demo:demo docs/demo/leo.yaml         /home/demo/.leo/leo.yaml
COPY --chown=demo:demo docs/demo/prompts/         /home/demo/.leo/prompts/
COPY --chown=demo:demo docs/demo/script.sh        /home/demo/script.sh
COPY --chown=demo:demo docs/demo/entrypoint.sh    /home/demo/entrypoint.sh

# Pre-accept Claude Code's onboarding (theme picker + per-workspace trust
# dialogs) so the spawned `claude` processes land directly on the REPL.
COPY --chown=demo:demo docs/demo/claude.seed.json /home/demo/.claude.json

# Workspaces referenced by leo.yaml
RUN mkdir -p /home/demo/workspace/assistant \
             /home/demo/workspace/watchdog \
             /home/demo/agents \
             /home/demo/out

VOLUME ["/home/demo/out"]

ENTRYPOINT ["/home/demo/entrypoint.sh"]
