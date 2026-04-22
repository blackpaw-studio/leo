#!/usr/bin/env bash
# Demo container entrypoint.
#
#   1. start `leo service` in the background (the daemon the demo talks to)
#   2. spawn a couple of ephemeral "coding" agents so `leo agent list`
#      looks populated
#   3. run asciinema against script.sh, writing the .cast to /home/demo/out
#   4. render a .gif from the cast via agg
#   5. leave both artifacts behind in /home/demo/out
#
# The script is idempotent — rerun the container to regenerate artifacts.

set -euo pipefail

OUT_DIR=${OUT_DIR:-/home/demo/out}
WINDOW_SIZE=${WINDOW_SIZE:-130x36}
FONT_SIZE=${FONT_SIZE:-13}

log() { printf '[demo] %s\n' "$*" >&2; }

# --- boot the supervisor ----------------------------------------------------
log "starting leo service --supervised ..."
# --supervised spawns claude inside tmux (provides a PTY); without it,
# `leo service` exec's claude directly and claude errors without a TTY.
leo service --supervised > /tmp/daemon.log 2>&1 &
DAEMON_PID=$!

# Wait for the daemon socket specifically — `leo status` parses config and
# succeeds even before the daemon is up, so it's not a reliable probe.
SOCKET=/home/demo/.leo/state/leo.sock
for i in $(seq 1 120); do
  if [[ -S "$SOCKET" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    break
  fi
  sleep 0.5
done
if [[ ! -S "$SOCKET" ]]; then
  log "daemon did not open socket; log follows:"
  cat /tmp/daemon.log >&2 || true
  exit 1
fi
if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
  log "daemon died before recording; log follows:"
  cat /tmp/daemon.log >&2 || true
  exit 1
fi
log "daemon ready (pid $DAEMON_PID, socket $SOCKET)"

# --- preload agents so the demo has something to show -----------------------
log "spawning preamble agents..."
leo agent spawn coding myapp    > /dev/null 2>&1 || true
leo agent spawn coding plugins  > /dev/null 2>&1 || true
leo agent spawn coding website  > /dev/null 2>&1 || true
# Let claude render inside each pane before the capture happens
sleep 5

# --- record ----------------------------------------------------------------
mkdir -p "$OUT_DIR"
CAST="$OUT_DIR/leo-demo.cast"
GIF="$OUT_DIR/leo-demo.gif"

log "recording asciinema cast -> $CAST"
asciinema rec "$CAST" \
  --overwrite \
  --window-size "$WINDOW_SIZE" \
  -c "bash /home/demo/script.sh"

log "rendering gif -> $GIF"
agg --font-size "$FONT_SIZE" --theme monokai "$CAST" "$GIF"

log "done"
ls -lh "$OUT_DIR"

# Best-effort cleanup (container will be discarded anyway)
kill "$DAEMON_PID" 2>/dev/null || true
